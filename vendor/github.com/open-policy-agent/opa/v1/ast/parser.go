// Copyright 2020 The OPA Authors.  All rights reserved.
// Use of this source code is governed by an Apache2
// license that can be found in the LICENSE file.

package ast

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/open-policy-agent/opa/v1/ast/internal/scanner"
	"github.com/open-policy-agent/opa/v1/ast/internal/tokens"
	astJSON "github.com/open-policy-agent/opa/v1/ast/json"
	"github.com/open-policy-agent/opa/v1/ast/location"
)

// DefaultMaxParsingRecursionDepth is the default maximum recursion
// depth for the parser
const DefaultMaxParsingRecursionDepth = 100000

// ErrMaxParsingRecursionDepthExceeded is returned when the parser
// recursion exceeds the maximum allowed depth
var ErrMaxParsingRecursionDepthExceeded = errors.New("max parsing recursion depth exceeded")

var RegoV1CompatibleRef = Ref{VarTerm("rego"), InternedTerm("v1")}

// RegoVersion defines the Rego syntax requirements for a module.
type RegoVersion int

const DefaultRegoVersion = RegoV1

const (
	RegoUndefined RegoVersion = iota
	// RegoV0 is the default, original Rego syntax.
	RegoV0
	// RegoV0CompatV1 requires modules to comply with both the RegoV0 and RegoV1 syntax (as when 'rego.v1' is imported in a module).
	// Shortly, RegoV1 compatibility is required, but 'rego.v1' or 'future.keywords' must also be imported.
	RegoV0CompatV1
	// RegoV1 is the Rego syntax enforced by OPA 1.0; e.g.:
	// future.keywords part of default keyword set, and don't require imports;
	// 'if' and 'contains' required in rule heads;
	// (some) strict checks on by default.
	RegoV1
)

func (v RegoVersion) Int() int {
	if v == RegoV1 {
		return 1
	}
	return 0
}

func (v RegoVersion) String() string {
	switch v {
	case RegoV0:
		return "v0"
	case RegoV1:
		return "v1"
	case RegoV0CompatV1:
		return "v0v1"
	default:
		return "unknown"
	}
}

func RegoVersionFromInt(i int) RegoVersion {
	if i == 1 {
		return RegoV1
	}
	return RegoV0
}

// Note: This state is kept isolated from the parser so that we
// can do efficient shallow copies of these values when doing a
// save() and restore().
type state struct {
	s         *scanner.Scanner
	lastEnd   int
	skippedNL bool
	tok       tokens.Token
	tokEnd    int
	lit       string
	loc       Location
	errors    Errors
	hints     []string
	comments  []*Comment
	wildcard  int
}

func (s *state) String() string {
	return fmt.Sprintf("<s: %v, tok: %v, lit: %q, loc: %v, errors: %d, comments: %d>", s.s, s.tok, s.lit, s.loc, len(s.errors), len(s.comments))
}

func (s *state) Loc() *location.Location {
	cpy := s.loc
	return &cpy
}

func (s *state) Text(offset, end int) []byte {
	bs := s.s.Bytes()
	if offset >= 0 && offset < len(bs) {
		if end >= offset && end <= len(bs) {
			return bs[offset:end]
		}
	}
	return nil
}

// Parser is used to parse Rego statements.
type Parser struct {
	r                 io.Reader
	s                 *state
	po                ParserOptions
	cache             parsedTermCache
	recursionDepth    int
	maxRecursionDepth int
}

type parsedTermCacheItem struct {
	t      *Term
	post   *state // post is the post-state that's restored on a cache-hit
	offset int
	next   *parsedTermCacheItem
}

type parsedTermCache struct {
	m *parsedTermCacheItem
}

func (c parsedTermCache) String() string {
	s := strings.Builder{}
	s.WriteRune('{')
	var e *parsedTermCacheItem
	for e = c.m; e != nil; e = e.next {
		s.WriteString(e.String())
	}
	s.WriteRune('}')
	return s.String()
}

func (e *parsedTermCacheItem) String() string {
	return fmt.Sprintf("<%d:%v>", e.offset, e.t)
}

// ParserOptions defines the options for parsing Rego statements.
type ParserOptions struct {
	Capabilities      *Capabilities
	ProcessAnnotation bool
	AllFutureKeywords bool
	FutureKeywords    []string
	SkipRules         bool
	// RegoVersion is the version of Rego to parse for.
	RegoVersion        RegoVersion
	unreleasedKeywords bool // TODO(sr): cleanup
}

// EffectiveRegoVersion returns the effective RegoVersion to use for parsing.
func (po *ParserOptions) EffectiveRegoVersion() RegoVersion {
	if po.RegoVersion == RegoUndefined {
		return DefaultRegoVersion
	}
	return po.RegoVersion
}

// NewParser creates and initializes a Parser.
func NewParser() *Parser {
	p := &Parser{
		s:                 &state{},
		po:                ParserOptions{},
		maxRecursionDepth: DefaultMaxParsingRecursionDepth,
	}
	return p
}

// WithMaxRecursionDepth sets the maximum recursion depth for the parser.
func (p *Parser) WithMaxRecursionDepth(depth int) *Parser {
	p.maxRecursionDepth = depth
	return p
}

// WithFilename provides the filename for Location details
// on parsed statements.
func (p *Parser) WithFilename(filename string) *Parser {
	p.s.loc.File = filename
	return p
}

// WithReader provides the io.Reader that the parser will
// use as its source.
func (p *Parser) WithReader(r io.Reader) *Parser {
	p.r = r
	return p
}

// WithProcessAnnotation enables or disables the processing of
// annotations by the Parser
func (p *Parser) WithProcessAnnotation(processAnnotation bool) *Parser {
	p.po.ProcessAnnotation = processAnnotation
	return p
}

// WithFutureKeywords enables "future" keywords, i.e., keywords that can
// be imported via
//
//	import future.keywords.kw
//	import future.keywords.other
//
// but in a more direct way. The equivalent of this import would be
//
//	WithFutureKeywords("kw", "other")
func (p *Parser) WithFutureKeywords(kws ...string) *Parser {
	p.po.FutureKeywords = kws
	return p
}

// WithAllFutureKeywords enables all "future" keywords, i.e., the
// ParserOption equivalent of
//
//	import future.keywords
func (p *Parser) WithAllFutureKeywords(yes bool) *Parser {
	p.po.AllFutureKeywords = yes
	return p
}

// withUnreleasedKeywords allows using keywords that haven't surfaced
// as future keywords (see above) yet, but have tests that require
// them to be parsed
func (p *Parser) withUnreleasedKeywords(yes bool) *Parser {
	p.po.unreleasedKeywords = yes
	return p
}

// WithCapabilities sets the capabilities structure on the parser.
func (p *Parser) WithCapabilities(c *Capabilities) *Parser {
	p.po.Capabilities = c
	return p
}

// WithSkipRules instructs the parser not to attempt to parse Rule statements.
func (p *Parser) WithSkipRules(skip bool) *Parser {
	p.po.SkipRules = skip
	return p
}

// WithJSONOptions sets the JSON options on the parser (now a no-op).
//
// Deprecated: Use SetOptions in the json package instead, where a longer description
// of why this is deprecated also can be found.
func (p *Parser) WithJSONOptions(_ *astJSON.Options) *Parser {
	return p
}

func (p *Parser) WithRegoVersion(version RegoVersion) *Parser {
	p.po.RegoVersion = version
	return p
}

func (p *Parser) parsedTermCacheLookup() (*Term, *state) {
	l := p.s.loc.Offset
	// stop comparing once the cached offsets are lower than l
	for h := p.cache.m; h != nil && h.offset >= l; h = h.next {
		if h.offset == l {
			return h.t, h.post
		}
	}
	return nil, nil
}

func (p *Parser) parsedTermCachePush(t *Term, s0 *state) {
	s1 := p.save()
	o0 := s0.loc.Offset
	entry := parsedTermCacheItem{t: t, post: s1, offset: o0}

	// find the first one whose offset is smaller than ours
	var e *parsedTermCacheItem
	for e = p.cache.m; e != nil; e = e.next {
		if e.offset < o0 {
			break
		}
	}
	entry.next = e
	p.cache.m = &entry
}

// futureParser returns a shallow copy of `p` with an empty
// cache, and a scanner that knows all future keywords.
// It's used to present hints in errors, when statements would
// only parse successfully if some future keyword is enabled.
func (p *Parser) futureParser() *Parser {
	q := *p
	q.s = p.save()
	q.s.s = p.s.s.WithKeywords(allFutureKeywords)
	q.cache = parsedTermCache{}
	return &q
}

// presentParser returns a shallow copy of `p` with an empty
// cache, and a scanner that knows none of the future keywords.
// It is used to successfully parse keyword imports, like
//
//	import future.keywords.in
//
// even when the parser has already been informed about the
// future keyword "in". This parser won't error out because
// "in" is an identifier.
func (p *Parser) presentParser() (*Parser, map[string]tokens.Token) {
	var cpy map[string]tokens.Token
	q := *p
	q.s = p.save()
	q.s.s, cpy = p.s.s.WithoutKeywords(allFutureKeywords)
	q.cache = parsedTermCache{}
	return &q, cpy
}

// Parse will read the Rego source and parse statements and
// comments as they are found. Any errors encountered while
// parsing will be accumulated and returned as a list of Errors.
func (p *Parser) Parse() ([]Statement, []*Comment, Errors) {

	if p.po.Capabilities == nil {
		p.po.Capabilities = CapabilitiesForThisVersion(CapabilitiesRegoVersion(p.po.RegoVersion))
	}

	allowedFutureKeywords := map[string]tokens.Token{}

	if p.po.EffectiveRegoVersion() == RegoV1 {
		if !p.po.Capabilities.ContainsFeature(FeatureRegoV1) {
			return nil, nil, Errors{
				&Error{
					Code:     ParseErr,
					Message:  "illegal capabilities: rego_v1 feature required for parsing v1 Rego",
					Location: nil,
				},
			}
		}

		// rego-v1 includes all v0 future keywords in the default language definition
		maps.Copy(allowedFutureKeywords, futureKeywordsV0)

		for _, kw := range p.po.Capabilities.FutureKeywords {
			if tok, ok := futureKeywords[kw]; ok {
				allowedFutureKeywords[kw] = tok
			} else {
				// For sake of error reporting, we still need to check that keywords in capabilities are known in v0
				if _, ok := futureKeywordsV0[kw]; !ok {
					return nil, nil, Errors{
						&Error{
							Code:     ParseErr,
							Message:  fmt.Sprintf("illegal capabilities: unknown keyword: %v", kw),
							Location: nil,
						},
					}
				}
			}
		}

		// Check that explicitly requested future keywords are known.
		for _, kw := range p.po.FutureKeywords {
			if _, ok := allowedFutureKeywords[kw]; !ok {
				return nil, nil, Errors{
					&Error{
						Code:     ParseErr,
						Message:  fmt.Sprintf("unknown future keyword: %v", kw),
						Location: nil,
					},
				}
			}
		}
	} else {
		for _, kw := range p.po.Capabilities.FutureKeywords {
			var ok bool
			allowedFutureKeywords[kw], ok = allFutureKeywords[kw]
			if !ok {
				return nil, nil, Errors{
					&Error{
						Code:     ParseErr,
						Message:  fmt.Sprintf("illegal capabilities: unknown keyword: %v", kw),
						Location: nil,
					},
				}
			}
		}

		if p.po.Capabilities.ContainsFeature(FeatureRegoV1) {
			// rego-v1 includes all v0 future keywords in the default language definition
			maps.Copy(allowedFutureKeywords, futureKeywordsV0)
		}
	}

	var err error
	p.s.s, err = scanner.New(p.r)
	if err != nil {
		return nil, nil, Errors{
			&Error{
				Code:     ParseErr,
				Message:  err.Error(),
				Location: nil,
			},
		}
	}

	selected := map[string]tokens.Token{}
	if p.po.AllFutureKeywords || p.po.EffectiveRegoVersion() == RegoV1 {
		maps.Copy(selected, allowedFutureKeywords)
	} else {
		for _, kw := range p.po.FutureKeywords {
			tok, ok := allowedFutureKeywords[kw]
			if !ok {
				return nil, nil, Errors{
					&Error{
						Code:     ParseErr,
						Message:  fmt.Sprintf("unknown future keyword: %v", kw),
						Location: nil,
					},
				}
			}
			selected[kw] = tok
		}
	}
	p.s.s = p.s.s.WithKeywords(selected)

	if p.po.EffectiveRegoVersion() == RegoV1 {
		for kw, tok := range allowedFutureKeywords {
			p.s.s.AddKeyword(kw, tok)
		}
	}

	// read the first token to initialize the parser
	p.scan()

	var stmts []Statement

	// Read from the scanner until the last token is reached or no statements
	// can be parsed. Attempt to parse package statements, import statements,
	// rule statements, and then body/query statements (in that order). If a
	// statement cannot be parsed, restore the parser state before trying the
	// next type of statement. If a statement can be parsed, continue from that
	// point trying to parse packages, imports, etc. in the same order.
	for p.s.tok != tokens.EOF {

		s := p.save()

		if pkg := p.parsePackage(); pkg != nil {
			stmts = append(stmts, pkg)
			continue
		} else if len(p.s.errors) > 0 {
			break
		}

		p.restore(s)
		s = p.save()

		if imp := p.parseImport(); imp != nil {
			if RegoRootDocument.Equal(imp.Path.Value.(Ref)[0]) {
				p.regoV1Import(imp)
			}

			if FutureRootDocument.Equal(imp.Path.Value.(Ref)[0]) {
				p.futureImport(imp, allowedFutureKeywords)
			}

			stmts = append(stmts, imp)
			continue
		} else if len(p.s.errors) > 0 {
			break
		}

		p.restore(s)

		if !p.po.SkipRules {
			s = p.save()

			if rules := p.parseRules(); rules != nil {
				for i := range rules {
					stmts = append(stmts, rules[i])
				}
				continue
			} else if len(p.s.errors) > 0 {
				break
			}

			p.restore(s)
		}

		if body := p.parseQuery(true, tokens.EOF); body != nil {
			stmts = append(stmts, body)
			continue
		}

		break
	}

	if p.po.ProcessAnnotation {
		stmts = p.parseAnnotations(stmts)
	}

	return stmts, p.s.comments, p.s.errors
}

func (p *Parser) parseAnnotations(stmts []Statement) []Statement {

	annotStmts, errs := parseAnnotations(p.s.comments)
	for _, err := range errs {
		p.error(err.Location, err.Message)
	}

	for _, annotStmt := range annotStmts {
		stmts = append(stmts, annotStmt)
	}

	return stmts
}

func parseAnnotations(comments []*Comment) ([]*Annotations, Errors) {

	var hint = []byte("METADATA")
	var curr *metadataParser
	var blocks []*metadataParser

	for i := range comments {
		if curr != nil {
			if comments[i].Location.Row == comments[i-1].Location.Row+1 && comments[i].Location.Col == 1 {
				curr.Append(comments[i])
				continue
			}
			curr = nil
		}
		if bytes.HasPrefix(bytes.TrimSpace(comments[i].Text), hint) {
			curr = newMetadataParser(comments[i].Location)
			blocks = append(blocks, curr)
		}
	}

	var stmts []*Annotations
	var errs Errors
	for _, b := range blocks {
		a, err := b.Parse()
		if err != nil {
			errs = append(errs, &Error{
				Code:     ParseErr,
				Message:  err.Error(),
				Location: b.loc,
			})
		} else {
			stmts = append(stmts, a)
		}
	}

	return stmts, errs
}

func (p *Parser) parsePackage() *Package {

	var pkg Package
	pkg.SetLoc(p.s.Loc())

	if p.s.tok != tokens.Package {
		return nil
	}

	p.scanWS()

	// Make sure we allow the first term of refs to be the 'package' keyword.
	if p.s.tok == tokens.Dot || p.s.tok == tokens.LBrack {
		// This is a ref, not a package declaration.
		return nil
	}

	if p.s.tok == tokens.Whitespace {
		p.scan()
	}

	if !isIdentOrAllowedRefKeyword(p) {
		p.illegalToken()
		return nil
	}

	term := p.parseTerm()

	if term != nil {
		switch v := term.Value.(type) {
		case Var:
			pkg.Path = Ref{
				DefaultRootDocument.Copy().SetLocation(term.Location),
				StringTerm(string(v)).SetLocation(term.Location),
			}
		case Ref:
			pkg.Path = make(Ref, len(v)+1)
			pkg.Path[0] = DefaultRootDocument.Copy().SetLocation(v[0].Location)
			first, ok := v[0].Value.(Var)
			if !ok {
				p.errorf(v[0].Location, "unexpected %v token: expecting var", ValueName(v[0].Value))
				return nil
			}
			pkg.Path[1] = StringTerm(string(first)).SetLocation(v[0].Location)
			for i := 2; i < len(pkg.Path); i++ {
				switch v[i-1].Value.(type) {
				case String:
					pkg.Path[i] = v[i-1]
				default:
					p.errorf(v[i-1].Location, "unexpected %v token: expecting string", ValueName(v[i-1].Value))
					return nil
				}
			}
		default:
			p.illegalToken()
			return nil
		}
	}

	if pkg.Path == nil {
		if len(p.s.errors) == 0 {
			p.error(p.s.Loc(), "expected path")
		}
		return nil
	}

	return &pkg
}

func (p *Parser) parseImport() *Import {

	var imp Import
	imp.SetLoc(p.s.Loc())

	if p.s.tok != tokens.Import {
		return nil
	}

	p.scanWS()

	// Make sure we allow the first term of refs to be the 'import' keyword.
	if p.s.tok == tokens.Dot || p.s.tok == tokens.LBrack {
		// This is a ref, not an import declaration.
		return nil
	}

	if p.s.tok == tokens.Whitespace {
		p.scan()
	}

	if !isIdentOrAllowedRefKeyword(p) {
		p.illegalToken()
		return nil
	}

	q, prev := p.presentParser()
	term := q.parseTerm()
	if term != nil {
		switch v := term.Value.(type) {
		case Var:
			imp.Path = RefTerm(term).SetLocation(term.Location)
		case Ref:
			for i := 1; i < len(v); i++ {
				if _, ok := v[i].Value.(String); !ok {
					p.errorf(v[i].Location, "unexpected %v token: expecting string", ValueName(v[i].Value))
					return nil
				}
			}
			imp.Path = term
		}
	}
	// keep advanced parser state, reset known keywords
	p.s = q.s
	p.s.s = q.s.s.WithKeywords(prev)

	if imp.Path == nil {
		p.error(p.s.Loc(), "expected path")
		return nil
	}

	path := imp.Path.Value.(Ref)

	switch {
	case RootDocumentNames.Contains(path[0]):
	case FutureRootDocument.Equal(path[0]):
	case RegoRootDocument.Equal(path[0]):
	default:
		p.hint("if this is unexpected, try updating OPA")
		p.errorf(imp.Path.Location, "unexpected import path, must begin with one of: %v, got: %v",
			RootDocumentNames.Union(NewSet(FutureRootDocument, RegoRootDocument)),
			path[0])
		return nil
	}

	if p.s.tok == tokens.As {
		p.scan()

		if p.s.tok != tokens.Ident {
			p.illegal("expected var")
			return nil
		}

		if alias := p.parseTerm(); alias != nil {
			v, ok := alias.Value.(Var)
			if ok {
				imp.Alias = v
				return &imp
			}
		}
		p.illegal("expected var")
		return nil
	}

	if imp.Alias != "" {
		// Unreachable: parsing the alias var should already have generated an error.
		name := imp.Alias.String()
		if IsKeywordInRegoVersion(name, p.po.EffectiveRegoVersion()) {
			p.errorf(imp.Location, "unexpected import alias, must not be a keyword, got: %s", name)
		}
		return &imp
	}

	r := imp.Path.Value.(Ref)

	// Don't allow keywords in the tail path term unless it's a future import
	if len(r) == 1 {
		t := r[0]
		name := string(t.Value.(Var))
		if IsKeywordInRegoVersion(name, p.po.EffectiveRegoVersion()) {
			p.errorf(t.Location, "unexpected import path, must not end with a keyword, got: %s", name)
			p.hint("import a different path or use an alias")
		}
	} else if !FutureRootDocument.Equal(r[0]) {
		t := r[len(r)-1]
		name := string(t.Value.(String))
		if IsKeywordInRegoVersion(name, p.po.EffectiveRegoVersion()) {
			p.errorf(t.Location, "unexpected import path, must not end with a keyword, got: %s", name)
			p.hint("import a different path or use an alias")
		}
	}

	return &imp
}

// isIdentOrAllowedRefKeyword checks if the current token is an Ident or a keyword in the active rego-version.
// If a keyword, sets p.s.token to token.Ident
func isIdentOrAllowedRefKeyword(p *Parser) bool {
	if p.s.tok == tokens.Ident {
		return true
	}

	if p.isAllowedRefKeyword(p.s.tok) {
		p.s.tok = tokens.Ident
		return true
	}

	return false
}

func scanAheadRef(p *Parser) bool {
	if p.isAllowedRefKeyword(p.s.tok) {
		// scan ahead to check if we're parsing a ref
		s := p.save()
		p.scanWS()
		tok := p.s.tok
		p.restore(s)

		if tok == tokens.Dot || tok == tokens.LBrack {
			p.s.tok = tokens.Ident
			return true
		}
	}

	return false
}

func (p *Parser) parseRules() []*Rule {

	var rule Rule
	rule.SetLoc(p.s.Loc())

	// This allows keywords in the first var term of the ref
	_ = scanAheadRef(p)

	if p.s.tok == tokens.Default {
		p.scan()
		rule.Default = true
		_ = scanAheadRef(p)
	}

	if p.s.tok != tokens.Ident {
		return nil
	}

	usesContains := false
	if rule.Head, usesContains = p.parseHead(rule.Default); rule.Head == nil {
		return nil
	}

	if usesContains {
		rule.Head.keywords = append(rule.Head.keywords, tokens.Contains)
	}

	if rule.Default {
		if !p.validateDefaultRuleValue(&rule) {
			return nil
		}

		if len(rule.Head.Args) > 0 {
			if !p.validateDefaultRuleArgs(&rule) {
				return nil
			}
		}

		rule.Body = NewBody(NewExpr(BooleanTerm(true).SetLocation(rule.Location)).SetLocation(rule.Location))
		return []*Rule{&rule}
	}

	// back-compat with `p[x] { ... }``
	hasIf := p.s.tok == tokens.If

	// p[x] if ...  becomes a single-value rule p[x]
	if hasIf && !usesContains && len(rule.Head.Ref()) == 2 {
		v := rule.Head.Ref()[1]
		_, isRef := v.Value.(Ref)
		if (!v.IsGround() || isRef) && len(rule.Head.Args) == 0 {
			rule.Head.Key = rule.Head.Ref()[1]
		}

		if rule.Head.Value == nil {
			rule.Head.generatedValue = true
			rule.Head.Value = BooleanTerm(true).SetLocation(rule.Head.Location)
		} else {
			// p[x] = y if  becomes a single-value rule p[x] with value y, but needs name for compat
			v, ok := rule.Head.Ref()[0].Value.(Var)
			if !ok {
				return nil
			}
			rule.Head.Name = v
		}
	}

	// p[x]         becomes a multi-value rule p
	if !hasIf && !usesContains &&
		len(rule.Head.Args) == 0 && // not a function
		len(rule.Head.Ref()) == 2 { // ref like 'p[x]'
		v, ok := rule.Head.Ref()[0].Value.(Var)
		if !ok {
			return nil
		}
		rule.Head.Name = v
		rule.Head.Key = rule.Head.Ref()[1]
		if rule.Head.Value == nil {
			rule.Head.SetRef(rule.Head.Ref()[:len(rule.Head.Ref())-1])
		}
	}

	switch {
	case hasIf:
		rule.Head.keywords = append(rule.Head.keywords, tokens.If)
		p.scan()
		s := p.save()
		if expr := p.parseLiteral(); expr != nil {
			// NOTE(sr): set literals are never false or undefined, so parsing this as
			//  p if { true }
			//       ^^^^^^^^ set of one element, `true`
			// isn't valid.
			isSetLiteral := false
			if t, ok := expr.Terms.(*Term); ok {
				_, isSetLiteral = t.Value.(Set)
			}
			// expr.Term is []*Term or Every
			if !isSetLiteral {
				rule.Body.Append(expr)
				break
			}
		}

		// parsing as literal didn't work out, expect '{ BODY }'
		p.restore(s)
		fallthrough

	case p.s.tok == tokens.LBrace:
		p.scan()
		if rule.Body = p.parseBody(tokens.RBrace); rule.Body == nil {
			return nil
		}
		p.scan()

	case usesContains:
		rule.Body = NewBody(NewExpr(BooleanTerm(true).SetLocation(rule.Location)).SetLocation(rule.Location))
		rule.generatedBody = true
		rule.Location = rule.Head.Location

		return []*Rule{&rule}

	default:
		return nil
	}

	if p.s.tok == tokens.Else {
		// This might just be a refhead rule with a leading 'else' term.
		if !scanAheadRef(p) {
			if r := rule.Head.Ref(); len(r) > 1 && !r.IsGround() {
				p.error(p.s.Loc(), "else keyword cannot be used on rules with variables in head")
				return nil
			}
			if rule.Head.Key != nil {
				p.error(p.s.Loc(), "else keyword cannot be used on multi-value rules")
				return nil
			}

			if rule.Else = p.parseElse(rule.Head); rule.Else == nil {
				return nil
			}
		}
	}

	rule.Location.Text = p.s.Text(rule.Location.Offset, p.s.lastEnd)

	rules := []*Rule{&rule}

	for p.s.tok == tokens.LBrace {

		if rule.Else != nil {
			p.error(p.s.Loc(), "expected else keyword")
			return nil
		}

		loc := p.s.Loc()

		p.scan()
		var next Rule

		if next.Body = p.parseBody(tokens.RBrace); next.Body == nil {
			return nil
		}
		p.scan()

		loc.Text = p.s.Text(loc.Offset, p.s.lastEnd)
		next.SetLoc(loc)

		// Chained rule head's keep the original
		// rule's head AST but have their location
		// set to the rule body.
		next.Head = rule.Head.Copy()
		next.Head.keywords = rule.Head.keywords
		for i := range next.Head.Args {
			if v, ok := next.Head.Args[i].Value.(Var); ok && v.IsWildcard() {
				next.Head.Args[i].Value = Var(p.genwildcard())
			}
		}
		setLocRecursive(next.Head, loc)

		rules = append(rules, &next)
	}

	return rules
}

func (p *Parser) parseElse(head *Head) *Rule {

	var rule Rule
	rule.SetLoc(p.s.Loc())

	rule.Head = head.Copy()
	rule.Head.generatedValue = false
	for i := range rule.Head.Args {
		if v, ok := rule.Head.Args[i].Value.(Var); ok && v.IsWildcard() {
			rule.Head.Args[i].Value = Var(p.genwildcard())
		}
	}
	rule.Head.SetLoc(p.s.Loc())

	defer func() {
		rule.Location.Text = p.s.Text(rule.Location.Offset, p.s.lastEnd)
	}()

	p.scan()

	switch p.s.tok {
	case tokens.LBrace, tokens.If: // no value, but a body follows directly
		rule.Head.generatedValue = true
		rule.Head.Value = BooleanTerm(true)
	case tokens.Assign, tokens.Unify:
		rule.Head.Assign = tokens.Assign == p.s.tok
		p.scan()
		rule.Head.Value = p.parseTermInfixCall()
		if rule.Head.Value == nil {
			return nil
		}
		rule.Head.Location.Text = p.s.Text(rule.Head.Location.Offset, p.s.lastEnd)
	default:
		p.illegal("expected else value term or rule body")
		return nil
	}

	hasIf := p.s.tok == tokens.If
	hasLBrace := p.s.tok == tokens.LBrace

	if !hasIf && !hasLBrace {
		rule.Body = NewBody(NewExpr(BooleanTerm(true)))
		rule.generatedBody = true
		setLocRecursive(rule.Body, rule.Location)
		return &rule
	}

	if hasIf {
		rule.Head.keywords = append(rule.Head.keywords, tokens.If)
		p.scan()
	}

	if p.s.tok == tokens.LBrace {
		p.scan()
		if rule.Body = p.parseBody(tokens.RBrace); rule.Body == nil {
			return nil
		}
		p.scan()
	} else if p.s.tok != tokens.EOF {
		expr := p.parseLiteral()
		if expr == nil {
			return nil
		}
		rule.Body.Append(expr)
		setLocRecursive(rule.Body, rule.Location)
	} else {
		p.illegal("rule body expected")
		return nil
	}

	if p.s.tok == tokens.Else {
		if rule.Else = p.parseElse(head); rule.Else == nil {
			return nil
		}
	}
	return &rule
}

func (p *Parser) parseHead(defaultRule bool) (*Head, bool) {
	head := &Head{}
	loc := p.s.Loc()
	defer func() {
		if head != nil {
			head.SetLoc(loc)
			head.Location.Text = p.s.Text(head.Location.Offset, p.s.lastEnd)
		}
	}()

	term := p.parseVar()
	if term == nil {
		return nil, false
	}

	ref := p.parseTermFinish(term, true)
	if ref == nil {
		p.illegal("expected rule head name")
		return nil, false
	}

	switch x := ref.Value.(type) {
	case Var:
		// TODO
		head = VarHead(x, ref.Location, nil)
	case Ref:
		head = RefHead(x)
	case Call:
		op, args := x[0], x[1:]
		var ref Ref
		switch y := op.Value.(type) {
		case Var:
			ref = Ref{op}
		case Ref:
			if _, ok := y[0].Value.(Var); !ok {
				p.illegal("rule head ref %v invalid", y)
				return nil, false
			}
			ref = y
		}
		head = RefHead(ref)
		head.Args = slices.Clone[[]*Term](args)

	default:
		return nil, false
	}

	name := head.Ref().String()

	switch p.s.tok {
	case tokens.Contains: // NOTE: no Value for `contains` heads, we return here
		// Catch error case of using 'contains' with a function definition rule head.
		if head.Args != nil {
			p.illegal("the contains keyword can only be used with multi-value rule definitions (e.g., %s contains <VALUE> { ... })", name)
		}
		p.scan()
		head.Key = p.parseTermInfixCall()
		if head.Key == nil {
			p.illegal("expected rule key term (e.g., %s contains <VALUE> { ... })", name)
		}
		return head, true

	case tokens.Unify:
		p.scan()
		head.Value = p.parseTermInfixCall()
		if head.Value == nil {
			// FIX HEAD.String()
			p.illegal("expected rule value term (e.g., %s[%s] = <VALUE> { ... })", name, head.Key)
		}
	case tokens.Assign:
		p.scan()
		head.Assign = true
		head.Value = p.parseTermInfixCall()
		if head.Value == nil {
			switch {
			case len(head.Args) > 0:
				p.illegal("expected function value term (e.g., %s(...) := <VALUE> { ... })", name)
			case head.Key != nil:
				p.illegal("expected partial rule value term (e.g., %s[...] := <VALUE> { ... })", name)
			case defaultRule:
				p.illegal("expected default rule value term (e.g., default %s := <VALUE>)", name)
			default:
				p.illegal("expected rule value term (e.g., %s := <VALUE> { ... })", name)
			}
		}
	}

	if head.Value == nil && head.Key == nil {
		if len(head.Ref()) != 2 || len(head.Args) > 0 {
			head.generatedValue = true
			head.Value = BooleanTerm(true).SetLocation(head.Location)
		}
	}
	return head, false
}

func (p *Parser) parseBody(end tokens.Token) Body {
	if !p.enter() {
		return nil
	}
	defer p.leave()
	return p.parseQuery(false, end)
}

func (p *Parser) parseQuery(requireSemi bool, end tokens.Token) Body {
	body := Body{}

	if p.s.tok == end {
		p.error(p.s.Loc(), "found empty body")
		return nil
	}

	for {
		expr := p.parseLiteral()
		if expr == nil {
			return nil
		}

		body.Append(expr)

		if p.s.tok == tokens.Semicolon {
			p.scan()
			continue
		}

		if p.s.tok == end || requireSemi {
			return body
		}

		if !p.s.skippedNL {
			// If there was already an error then don't pile this one on
			if len(p.s.errors) == 0 {
				p.illegal(`expected \n or %s or %s`, tokens.Semicolon, end)
			}
			return nil
		}
	}
}

func (p *Parser) parseLiteral() (expr *Expr) {

	offset := p.s.loc.Offset
	loc := p.s.Loc()

	defer func() {
		if expr != nil {
			loc.Text = p.s.Text(offset, p.s.lastEnd)
			expr.SetLoc(loc)
		}
	}()

	// Check that we're not parsing a ref
	if p.isAllowedRefKeyword(p.s.tok) {
		// Scan ahead
		s := p.save()
		p.scanWS()
		tok := p.s.tok
		p.restore(s)

		if tok == tokens.Dot || tok == tokens.LBrack {
			p.s.tok = tokens.Ident
			return p.parseLiteralExpr(false)
		}
	}

	var negated bool
	if p.s.tok == tokens.Not {
		s := p.save()
		p.scanWS()
		tok := p.s.tok
		p.restore(s)

		if tok != tokens.Dot && tok != tokens.LBrack {
			p.scan()
			negated = true
		}
	}

	switch p.s.tok {
	case tokens.Some:
		if negated {
			p.illegal("illegal negation of 'some'")
			return nil
		}
		return p.parseSome()
	case tokens.Every:
		if negated {
			p.illegal("illegal negation of 'every'")
			return nil
		}
		return p.parseEvery()
	default:
		return p.parseLiteralExpr(negated)
	}
}

func (p *Parser) isAllowedRefKeyword(t tokens.Token) bool {
	return p.isAllowedRefKeywordStr(t.String())
}

func (p *Parser) isAllowedRefKeywordStr(s string) bool {
	if p.po.Capabilities.ContainsFeature(FeatureKeywordsInRefs) {
		return IsKeywordInRegoVersion(s, p.po.EffectiveRegoVersion()) || p.s.s.IsKeyword(s)
	}

	return false
}

func (p *Parser) parseLiteralExpr(negated bool) *Expr {
	s := p.save()
	expr := p.parseExpr()
	if expr != nil {
		expr.Negated = negated
		if p.s.tok == tokens.With {
			if expr.With = p.parseWith(); expr.With == nil {
				return nil
			}
		}
		// If we find a plain `every` identifier, attempt to parse an every expression,
		// add hint if it succeeds.
		if term, ok := expr.Terms.(*Term); ok && Var("every").Equal(term.Value) {
			var hint bool
			t := p.save()
			p.restore(s)
			if expr := p.futureParser().parseEvery(); expr != nil {
				_, hint = expr.Terms.(*Every)
			}
			p.restore(t)
			if hint {
				p.hint("`import future.keywords.every` for `every x in xs { ... }` expressions")
			}
		}
		return expr
	}
	return nil
}

func (p *Parser) parseWith() []*With {

	withs := []*With{}

	for {

		with := With{
			Location: p.s.Loc(),
		}
		p.scan()

		if p.s.tok != tokens.Ident {
			p.illegal("expected ident")
			return nil
		}

		with.Target = p.parseTerm()
		if with.Target == nil {
			return nil
		}

		switch with.Target.Value.(type) {
		case Ref, Var:
			break
		default:
			p.illegal("expected with target path")
		}

		if p.s.tok != tokens.As {
			p.illegal("expected as keyword")
			return nil
		}

		p.scan()

		if with.Value = p.parseTermInfixCall(); with.Value == nil {
			return nil
		}

		with.Location.Text = p.s.Text(with.Location.Offset, p.s.lastEnd)

		withs = append(withs, &with)

		if p.s.tok != tokens.With {
			break
		}
	}

	return withs
}

func (p *Parser) parseSome() *Expr {

	decl := &SomeDecl{}
	decl.SetLoc(p.s.Loc())

	// Attempt to parse "some x in xs", which will end up in
	//   SomeDecl{Symbols: ["member(x, xs)"]}
	s := p.save()
	p.scan()
	if term := p.parseTermInfixCall(); term != nil {
		if call, ok := term.Value.(Call); ok {
			switch call[0].String() {
			case Member.Name:
				if len(call) != 3 {
					p.illegal("illegal domain")
					return nil
				}
			case MemberWithKey.Name:
				if len(call) != 4 {
					p.illegal("illegal domain")
					return nil
				}
			default:
				p.illegal("expected `x in xs` or `x, y in xs` expression")
				return nil
			}

			decl.Symbols = []*Term{term}
			expr := NewExpr(decl).SetLocation(decl.Location)
			if p.s.tok == tokens.With {
				if expr.With = p.parseWith(); expr.With == nil {
					return nil
				}
			}
			return expr
		}
	}

	p.restore(s)
	s = p.save() // new copy for later
	var hint bool
	p.scan()
	if term := p.futureParser().parseTermInfixCall(); term != nil {
		if call, ok := term.Value.(Call); ok {
			switch call[0].String() {
			case Member.Name, MemberWithKey.Name:
				hint = true
			}
		}
	}

	// go on as before, it's `some x[...]` or illegal
	p.restore(s)
	if hint {
		p.hint("`import future.keywords.in` for `some x in xs` expressions")
	}

	for { // collecting var args

		p.scan()

		if p.s.tok != tokens.Ident {
			p.illegal("expected var")
			return nil
		}

		decl.Symbols = append(decl.Symbols, p.parseVar())

		p.scan()

		if p.s.tok != tokens.Comma {
			break
		}
	}

	return NewExpr(decl).SetLocation(decl.Location)
}

func (p *Parser) parseEvery() *Expr {
	qb := &Every{}
	qb.SetLoc(p.s.Loc())

	// TODO(sr): We'd get more accurate error messages if we didn't rely on
	// parseTermInfixCall here, but parsed "var [, var] in term" manually.
	p.scan()
	term := p.parseTermInfixCall()
	if term == nil {
		return nil
	}
	call, ok := term.Value.(Call)
	if !ok {
		p.illegal("expected `x[, y] in xs { ... }` expression")
		return nil
	}
	switch call[0].String() {
	case Member.Name: // x in xs
		if len(call) != 3 {
			p.illegal("illegal domain")
			return nil
		}
		qb.Value = call[1]
		qb.Domain = call[2]
	case MemberWithKey.Name: // k, v in xs
		if len(call) != 4 {
			p.illegal("illegal domain")
			return nil
		}
		qb.Key = call[1]
		qb.Value = call[2]
		qb.Domain = call[3]
		if _, ok := qb.Key.Value.(Var); !ok {
			p.illegal("expected key to be a variable")
			return nil
		}
	default:
		p.illegal("expected `x[, y] in xs { ... }` expression")
		return nil
	}
	if _, ok := qb.Value.Value.(Var); !ok {
		p.illegal("expected value to be a variable")
		return nil
	}
	if p.s.tok == tokens.LBrace { // every x in xs { ... }
		p.scan()
		body := p.parseBody(tokens.RBrace)
		if body == nil {
			return nil
		}
		p.scan()
		qb.Body = body
		expr := NewExpr(qb).SetLocation(qb.Location)

		if p.s.tok == tokens.With {
			if expr.With = p.parseWith(); expr.With == nil {
				return nil
			}
		}
		return expr
	}

	p.illegal("missing body")
	return nil
}

func (p *Parser) parseExpr() *Expr {

	lhs := p.parseTermInfixCall()
	if lhs == nil {
		return nil
	}

	if op := p.parseTermOp(tokens.Assign, tokens.Unify); op != nil {
		if rhs := p.parseTermInfixCall(); rhs != nil {
			return NewExpr([]*Term{op, lhs, rhs})
		}
		return nil
	}

	// NOTE(tsandall): the top-level call term is converted to an expr because
	// the evaluator does not support the call term type (nested calls are
	// rewritten by the compiler.)
	if call, ok := lhs.Value.(Call); ok {
		return NewExpr([]*Term(call))
	}

	return NewExpr(lhs)
}

// parseTermInfixCall consumes the next term from the input and returns it. If a
// term cannot be parsed the return value is nil and error will be recorded. The
// scanner will be advanced to the next token before returning.
// By starting out with infix relations (==, !=, <, etc) and further calling the
// other binary operators (|, &, arithmetics), it constitutes the binding
// precedence.
func (p *Parser) parseTermInfixCall() *Term {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	return p.parseTermIn(nil, true, p.s.loc.Offset)
}

func (p *Parser) parseTermInfixCallInList() *Term {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	return p.parseTermIn(nil, false, p.s.loc.Offset)
}

// use static references to avoid allocations, and
// copy them to  the call term only when needed
var memberWithKeyRef = MemberWithKey.Ref()
var memberRef = Member.Ref()

func (p *Parser) parseTermIn(lhs *Term, keyVal bool, offset int) *Term {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	// NOTE(sr): `in` is a bit special: besides `lhs in rhs`, it also
	// supports `key, val in rhs`, so it can have an optional second lhs.
	// `keyVal` triggers if we attempt to parse a second lhs argument (`mhs`).
	if lhs == nil {
		lhs = p.parseTermRelation(nil, offset)
	}
	if lhs != nil {
		if keyVal && p.s.tok == tokens.Comma { // second "lhs", or "middle hand side"
			s := p.save()
			p.scan()
			if mhs := p.parseTermRelation(nil, offset); mhs != nil {

				if op := p.parseTermOpName(memberWithKeyRef, tokens.In); op != nil {
					if rhs := p.parseTermRelation(nil, p.s.loc.Offset); rhs != nil {
						call := p.setLoc(CallTerm(op, lhs, mhs, rhs), lhs.Location, offset, p.s.lastEnd)
						switch p.s.tok {
						case tokens.In:
							return p.parseTermIn(call, keyVal, offset)
						default:
							return call
						}
					}
				}
			}
			p.restore(s)
		}

		_ = scanAheadRef(p)

		if op := p.parseTermOpName(memberRef, tokens.In); op != nil {
			if rhs := p.parseTermRelation(nil, p.s.loc.Offset); rhs != nil {
				call := p.setLoc(CallTerm(op, lhs, rhs), lhs.Location, offset, p.s.lastEnd)
				switch p.s.tok {
				case tokens.In:
					return p.parseTermIn(call, keyVal, offset)
				default:
					return call
				}
			}
		}
	}
	return lhs
}

func (p *Parser) parseTermRelation(lhs *Term, offset int) *Term {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	if lhs == nil {
		lhs = p.parseTermOr(nil, offset)
	}
	if lhs != nil {
		if op := p.parseTermOp(tokens.Equal, tokens.Neq, tokens.Lt, tokens.Gt, tokens.Lte, tokens.Gte); op != nil {
			if rhs := p.parseTermOr(nil, p.s.loc.Offset); rhs != nil {
				call := p.setLoc(CallTerm(op, lhs, rhs), lhs.Location, offset, p.s.lastEnd)
				switch p.s.tok {
				case tokens.Equal, tokens.Neq, tokens.Lt, tokens.Gt, tokens.Lte, tokens.Gte:
					return p.parseTermRelation(call, offset)
				default:
					return call
				}
			}
		}
	}
	return lhs
}

func (p *Parser) parseTermOr(lhs *Term, offset int) *Term {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	if lhs == nil {
		lhs = p.parseTermAnd(nil, offset)
	}
	if lhs != nil {
		if op := p.parseTermOp(tokens.Or); op != nil {
			if rhs := p.parseTermAnd(nil, p.s.loc.Offset); rhs != nil {
				call := p.setLoc(CallTerm(op, lhs, rhs), lhs.Location, offset, p.s.lastEnd)
				switch p.s.tok {
				case tokens.Or:
					return p.parseTermOr(call, offset)
				default:
					return call
				}
			}
		}
		return lhs
	}
	return nil
}

func (p *Parser) parseTermAnd(lhs *Term, offset int) *Term {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	if lhs == nil {
		lhs = p.parseTermArith(nil, offset)
	}
	if lhs != nil {
		if op := p.parseTermOp(tokens.And); op != nil {
			if rhs := p.parseTermArith(nil, p.s.loc.Offset); rhs != nil {
				call := p.setLoc(CallTerm(op, lhs, rhs), lhs.Location, offset, p.s.lastEnd)
				switch p.s.tok {
				case tokens.And:
					return p.parseTermAnd(call, offset)
				default:
					return call
				}
			}
		}
		return lhs
	}
	return nil
}

func (p *Parser) parseTermArith(lhs *Term, offset int) *Term {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	if lhs == nil {
		lhs = p.parseTermFactor(nil, offset)
	}
	if lhs != nil {
		if op := p.parseTermOp(tokens.Add, tokens.Sub); op != nil {
			if rhs := p.parseTermFactor(nil, p.s.loc.Offset); rhs != nil {
				call := p.setLoc(CallTerm(op, lhs, rhs), lhs.Location, offset, p.s.lastEnd)
				switch p.s.tok {
				case tokens.Add, tokens.Sub:
					return p.parseTermArith(call, offset)
				default:
					return call
				}
			}
		}
	}
	return lhs
}

func (p *Parser) parseTermFactor(lhs *Term, offset int) *Term {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	if lhs == nil {
		lhs = p.parseTerm()
	}
	if lhs != nil {
		if op := p.parseTermOp(tokens.Mul, tokens.Quo, tokens.Rem); op != nil {
			if rhs := p.parseTerm(); rhs != nil {
				call := p.setLoc(CallTerm(op, lhs, rhs), lhs.Location, offset, p.s.lastEnd)
				switch p.s.tok {
				case tokens.Mul, tokens.Quo, tokens.Rem:
					return p.parseTermFactor(call, offset)
				default:
					return call
				}
			}
		}
	}
	return lhs
}

func (p *Parser) parseTerm() *Term {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	if term, s := p.parsedTermCacheLookup(); s != nil {
		p.restore(s)
		return term
	}
	s0 := p.save()

	var term *Term
	switch p.s.tok {
	case tokens.Null:
		term = NullTerm().SetLocation(p.s.Loc())
	case tokens.True:
		term = BooleanTerm(true).SetLocation(p.s.Loc())
	case tokens.False:
		term = BooleanTerm(false).SetLocation(p.s.Loc())
	case tokens.Sub, tokens.Dot, tokens.Number:
		term = p.parseNumber()
	case tokens.String:
		term = p.parseString()
	case tokens.Ident, tokens.Contains: // NOTE(sr): contains anywhere BUT in rule heads gets no special treatment
		term = p.parseVar()
	case tokens.LBrack:
		term = p.parseArray()
	case tokens.LBrace:
		term = p.parseSetOrObject()
	case tokens.LParen:
		offset := p.s.loc.Offset
		p.scan()
		if r := p.parseTermInfixCall(); r != nil {
			if p.s.tok == tokens.RParen {
				r.Location.Text = p.s.Text(offset, p.s.tokEnd)
				term = r
			} else {
				p.error(p.s.Loc(), "non-terminated expression")
			}
		}
	default:
		p.illegalToken()
	}

	term = p.parseTermFinish(term, false)
	p.parsedTermCachePush(term, s0)
	return term
}

func (p *Parser) parseTermFinish(head *Term, skipws bool) *Term {
	if head == nil {
		return nil
	}
	offset := p.s.loc.Offset
	p.doScan(skipws)

	switch p.s.tok {
	case tokens.LParen, tokens.Dot, tokens.LBrack:
		return p.parseRef(head, offset)
	case tokens.Whitespace:
		p.scan()
		fallthrough
	default:
		if _, ok := head.Value.(Var); ok && RootDocumentNames.Contains(head) {
			return RefTerm(head).SetLocation(head.Location)
		}
		return head
	}
}

func (p *Parser) parseNumber() *Term {
	var prefix string
	loc := p.s.Loc()

	// Handle negative sign
	if p.s.tok == tokens.Sub {
		prefix = "-"
		p.scan()
		switch p.s.tok {
		case tokens.Number, tokens.Dot:
			break
		default:
			p.illegal("expected number")
			return nil
		}
	}

	// Handle decimal point
	if p.s.tok == tokens.Dot {
		prefix += "."
		p.scan()
		if p.s.tok != tokens.Number {
			p.illegal("expected number")
			return nil
		}
	}

	// Validate leading zeros: reject numbers like "01", "007", etc.
	// Skip validation if prefix ends with '.' (like ".123")
	hasDecimalPrefix := len(prefix) > 0 && prefix[len(prefix)-1] == '.'

	if !hasDecimalPrefix && len(p.s.lit) > 1 && p.s.lit[0] == '0' {
		// These are the only valid cases starting with '0':
		isDecimal := p.s.lit[1] == '.'                                               // "0.123"
		isScientific := len(p.s.lit) > 2 && (p.s.lit[1] == 'e' || p.s.lit[1] == 'E') // "0e5", "0E-3"

		if !isDecimal && !isScientific {
			p.illegal("expected number without leading zero")
			return nil
		}
	}

	// Ensure that the number is valid
	s := prefix + p.s.lit
	f, ok := new(big.Float).SetString(s)
	if !ok {
		p.illegal("invalid float")
		return nil
	}

	// Put limit on size of exponent to prevent non-linear cost of String()
	// function on big.Float from causing denial of service: https://github.com/golang/go/issues/11068
	//
	// n == sign * mantissa * 2^exp
	// 0.5 <= mantissa < 1.0
	//
	// The limit is arbitrary.
	exp := f.MantExp(nil)
	if exp > 1e5 || exp < -1e5 || f.IsInf() { // +/- inf, exp is 0
		p.error(p.s.Loc(), "number too big")
		return nil
	}

	// Note: Use the original string, do *not* round trip from
	// the big.Float as it can cause precision loss.
	return NumberTerm(json.Number(s)).SetLocation(loc)
}

func (p *Parser) parseString() *Term {
	if p.s.lit[0] == '"' {
		if p.s.lit == "\"\"" {
			return NewTerm(InternedEmptyString.Value).SetLocation(p.s.Loc())
		}

		var s string
		err := json.Unmarshal([]byte(p.s.lit), &s)
		if err != nil {
			p.errorf(p.s.Loc(), "illegal string literal: %s", p.s.lit)
			return nil
		}
		term := StringTerm(s).SetLocation(p.s.Loc())
		return term
	}
	return p.parseRawString()
}

func (p *Parser) parseRawString() *Term {
	if len(p.s.lit) < 2 {
		return nil
	}
	term := StringTerm(p.s.lit[1 : len(p.s.lit)-1]).SetLocation(p.s.Loc())
	return term
}

// this is the name to use for instantiating an empty set, e.g., `set()`.
var setConstructor = RefTerm(VarTerm("set"))

func (p *Parser) parseCall(operator *Term, offset int) (term *Term) {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	loc := operator.Location
	var end int

	defer func() {
		p.setLoc(term, loc, offset, end)
	}()

	p.scan() // steps over '('

	if p.s.tok == tokens.RParen { // no args, i.e. set() or any.func()
		end = p.s.tokEnd
		p.scanWS()
		if operator.Equal(setConstructor) {
			return SetTerm()
		}
		return CallTerm(operator)
	}

	if r := p.parseTermList(tokens.RParen, []*Term{operator}); r != nil {
		end = p.s.tokEnd
		p.scanWS()
		return CallTerm(r...)
	}

	return nil
}

func (p *Parser) parseRef(head *Term, offset int) (term *Term) {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	loc := head.Location
	var end int

	defer func() {
		p.setLoc(term, loc, offset, end)
	}()

	switch h := head.Value.(type) {
	case Var, *Array, Object, Set, *ArrayComprehension, *ObjectComprehension, *SetComprehension, Call:
		// ok
	default:
		p.errorf(loc, "illegal ref (head cannot be %v)", ValueName(h))
	}

	ref := []*Term{head}

	for {
		switch p.s.tok {
		case tokens.Dot:
			p.scanWS()
			if p.s.tok != tokens.Ident && !p.isAllowedRefKeyword(p.s.tok) {
				p.illegal("expected %v", tokens.Ident)
				return nil
			}
			ref = append(ref, StringTerm(p.s.lit).SetLocation(p.s.Loc()))
			p.scanWS()
		case tokens.LParen:
			term = p.parseCall(p.setLoc(RefTerm(ref...), loc, offset, p.s.loc.Offset), offset)
			if term != nil {
				switch p.s.tok {
				case tokens.Whitespace:
					p.scan()
					end = p.s.lastEnd
					return term
				case tokens.Dot, tokens.LBrack:
					term = p.parseRef(term, offset)
				}
			}
			end = p.s.tokEnd
			return term
		case tokens.LBrack:
			p.scan()
			if term := p.parseTermInfixCall(); term != nil {
				if p.s.tok != tokens.RBrack {
					p.illegal("expected %v", tokens.LBrack)
					return nil
				}
				ref = append(ref, term)
				p.scanWS()
			} else {
				return nil
			}
		case tokens.Whitespace:
			end = p.s.lastEnd
			p.scan()
			return RefTerm(ref...)
		default:
			end = p.s.lastEnd
			return RefTerm(ref...)
		}
	}
}

func (p *Parser) parseArray() (term *Term) {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	loc := p.s.Loc()
	offset := p.s.loc.Offset

	defer func() {
		p.setLoc(term, loc, offset, p.s.tokEnd)
	}()

	p.scan()

	if p.s.tok == tokens.RBrack {
		return ArrayTerm()
	}

	potentialComprehension := true

	// Skip leading commas, eg [, x, y]
	// Supported for backwards compatibility. In the future
	// we should make this a parse error.
	if p.s.tok == tokens.Comma {
		potentialComprehension = false
		p.scan()
	}

	s := p.save()

	// NOTE(tsandall): The parser cannot attempt a relational term here because
	// of ambiguity around comprehensions. For example, given:
	//
	//  {1 | 1}
	//
	// Does this represent a set comprehension or a set containing binary OR
	// call? We resolve the ambiguity by prioritizing comprehensions.
	head := p.parseTerm()

	if head == nil {
		return nil
	}

	switch p.s.tok {
	case tokens.RBrack:
		return ArrayTerm(head)
	case tokens.Comma:
		p.scan()
		if terms := p.parseTermList(tokens.RBrack, []*Term{head}); terms != nil {
			return ArrayTerm(terms...)
		}
		return nil
	case tokens.Or:
		if potentialComprehension {
			// Try to parse as if it is an array comprehension
			p.scan()
			if body := p.parseBody(tokens.RBrack); body != nil {
				return ArrayComprehensionTerm(head, body)
			}
			if p.s.tok != tokens.Comma {
				return nil
			}
		}
		// fall back to parsing as a normal array definition
	}

	p.restore(s)

	if terms := p.parseTermList(tokens.RBrack, nil); terms != nil {
		return ArrayTerm(terms...)
	}
	return nil
}

func (p *Parser) parseSetOrObject() (term *Term) {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	loc := p.s.Loc()
	offset := p.s.loc.Offset

	defer func() {
		p.setLoc(term, loc, offset, p.s.tokEnd)
	}()

	p.scan()

	if p.s.tok == tokens.RBrace {
		return ObjectTerm()
	}

	potentialComprehension := true

	// Skip leading commas, eg {, x, y}
	// Supported for backwards compatibility. In the future
	// we should make this a parse error.
	if p.s.tok == tokens.Comma {
		potentialComprehension = false
		p.scan()
	}

	s := p.save()

	// Try parsing just a single term first to give comprehensions higher
	// priority to "or" calls in ambiguous situations. Eg: { a | b }
	// will be a set comprehension.
	//
	// Note: We don't know yet if it is a set or object being defined.
	head := p.parseTerm()
	if head == nil {
		return nil
	}

	switch p.s.tok {
	case tokens.Or:
		if potentialComprehension {
			return p.parseSet(s, head, potentialComprehension)
		}
	case tokens.RBrace, tokens.Comma:
		return p.parseSet(s, head, potentialComprehension)
	case tokens.Colon:
		return p.parseObject(head, potentialComprehension)
	}

	p.restore(s)

	head = p.parseTermInfixCallInList()
	if head == nil {
		return nil
	}

	switch p.s.tok {
	case tokens.RBrace, tokens.Comma:
		return p.parseSet(s, head, false)
	case tokens.Colon:
		// It still might be an object comprehension, eg { a+1: b | ... }
		return p.parseObject(head, potentialComprehension)
	}

	p.illegal("non-terminated set")
	return nil
}

func (p *Parser) parseSet(s *state, head *Term, potentialComprehension bool) *Term {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	switch p.s.tok {
	case tokens.RBrace:
		return SetTerm(head)
	case tokens.Comma:
		p.scan()
		if terms := p.parseTermList(tokens.RBrace, []*Term{head}); terms != nil {
			return SetTerm(terms...)
		}
	case tokens.Or:
		if potentialComprehension {
			// Try to parse as if it is a set comprehension
			p.scan()
			if body := p.parseBody(tokens.RBrace); body != nil {
				return SetComprehensionTerm(head, body)
			}
			if p.s.tok != tokens.Comma {
				return nil
			}
		}
		// Fall back to parsing as normal set definition
		p.restore(s)
		if terms := p.parseTermList(tokens.RBrace, nil); terms != nil {
			return SetTerm(terms...)
		}
	}
	return nil
}

func (p *Parser) parseObject(k *Term, potentialComprehension bool) *Term {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	// NOTE(tsandall): Assumption: this function is called after parsing the key
	// of the head element and then receiving a colon token from the scanner.
	// Advance beyond the colon and attempt to parse an object.
	if p.s.tok != tokens.Colon {
		panic("expected colon")
	}
	p.scan()

	s := p.save()

	// NOTE(sr): We first try to parse the value as a term (`v`), and see
	// if we can parse `{ x: v | ...}` as a comprehension.
	// However, if we encounter either a Comma or an RBace, it cannot be
	// parsed as a comprehension -- so we save double work further down
	// where `parseObjectFinish(k, v, false)` would only exercise the
	// same code paths once more.
	v := p.parseTerm()
	if v == nil {
		return nil
	}

	potentialRelation := true
	if potentialComprehension {
		switch p.s.tok {
		case tokens.RBrace, tokens.Comma:
			potentialRelation = false
			fallthrough
		case tokens.Or:
			if term := p.parseObjectFinish(k, v, true); term != nil {
				return term
			}
		}
	}

	p.restore(s)

	if potentialRelation {
		v := p.parseTermInfixCallInList()
		if v == nil {
			return nil
		}

		switch p.s.tok {
		case tokens.RBrace, tokens.Comma:
			return p.parseObjectFinish(k, v, false)
		}
	}

	p.illegal("non-terminated object")
	return nil
}

func (p *Parser) parseObjectFinish(key, val *Term, potentialComprehension bool) *Term {
	if !p.enter() {
		return nil
	}
	defer p.leave()

	switch p.s.tok {
	case tokens.RBrace:
		return ObjectTerm([2]*Term{key, val})
	case tokens.Or:
		if potentialComprehension {
			p.scan()
			if body := p.parseBody(tokens.RBrace); body != nil {
				return ObjectComprehensionTerm(key, val, body)
			}
		} else {
			p.illegal("non-terminated object")
		}
	case tokens.Comma:
		p.scan()
		if r := p.parseTermPairList(tokens.RBrace, [][2]*Term{{key, val}}); r != nil {
			return ObjectTerm(r...)
		}
	}
	return nil
}

func (p *Parser) parseTermList(end tokens.Token, r []*Term) []*Term {
	if p.s.tok == end {
		return r
	}
	for {
		term := p.parseTermInfixCallInList()
		if term != nil {
			r = append(r, term)
			switch p.s.tok {
			case end:
				return r
			case tokens.Comma:
				p.scan()
				if p.s.tok == end {
					return r
				}
				continue
			default:
				p.illegal(fmt.Sprintf("expected %q or %q", tokens.Comma, end))
				return nil
			}
		}
		return nil
	}
}

func (p *Parser) parseTermPairList(end tokens.Token, r [][2]*Term) [][2]*Term {
	if p.s.tok == end {
		return r
	}
	for {
		key := p.parseTermInfixCallInList()
		if key != nil {
			switch p.s.tok {
			case tokens.Colon:
				p.scan()
				if val := p.parseTermInfixCallInList(); val != nil {
					r = append(r, [2]*Term{key, val})
					switch p.s.tok {
					case end:
						return r
					case tokens.Comma:
						p.scan()
						if p.s.tok == end {
							return r
						}
						continue
					default:
						p.illegal(fmt.Sprintf("expected %q or %q", tokens.Comma, end))
						return nil
					}
				}
			default:
				p.illegal(fmt.Sprintf("expected %q", tokens.Colon))
				return nil
			}
		}
		return nil
	}
}

func (p *Parser) parseTermOp(values ...tokens.Token) *Term {
	if slices.Contains(values, p.s.tok) {
		r := RefTerm(VarTerm(p.s.tok.String()).SetLocation(p.s.Loc())).SetLocation(p.s.Loc())
		p.scan()
		return r
	}
	return nil
}

func (p *Parser) parseTermOpName(ref Ref, values ...tokens.Token) *Term {
	if slices.Contains(values, p.s.tok) {
		cp := ref.Copy()
		for _, r := range cp {
			r.SetLocation(p.s.Loc())
		}
		t := RefTerm(cp...)
		t.SetLocation(p.s.Loc())
		p.scan()
		return t
	}
	return nil
}

func (p *Parser) parseVar() *Term {

	s := p.s.lit

	term := VarTerm(s).SetLocation(p.s.Loc())

	// Update wildcard values with unique identifiers
	if term.Equal(Wildcard) {
		term.Value = Var(p.genwildcard())
	}

	return term
}

func (p *Parser) genwildcard() string {
	c := p.s.wildcard
	p.s.wildcard++
	return fmt.Sprintf("%v%d", WildcardPrefix, c)
}

func (p *Parser) error(loc *location.Location, reason string) {
	p.errorf(loc, reason) //nolint:govet
}

func (p *Parser) errorf(loc *location.Location, f string, a ...any) {
	msg := strings.Builder{}
	msg.WriteString(fmt.Sprintf(f, a...))

	switch len(p.s.hints) {
	case 0: // nothing to do
	case 1:
		msg.WriteString(" (hint: ")
		msg.WriteString(p.s.hints[0])
		msg.WriteRune(')')
	default:
		msg.WriteString(" (hints: ")
		for i, h := range p.s.hints {
			if i > 0 {
				msg.WriteString(", ")
			}
			msg.WriteString(h)
		}
		msg.WriteRune(')')
	}

	p.s.errors = append(p.s.errors, &Error{
		Code:     ParseErr,
		Message:  msg.String(),
		Location: loc,
		Details:  newParserErrorDetail(p.s.s.Bytes(), loc.Offset),
	})
	p.s.hints = nil
}

func (p *Parser) hint(f string, a ...any) {
	p.s.hints = append(p.s.hints, fmt.Sprintf(f, a...))
}

func (p *Parser) illegal(note string, a ...any) {
	tok := p.s.tok.String()

	if p.s.tok == tokens.Illegal {
		p.errorf(p.s.Loc(), "illegal token")
		return
	}

	tokType := "token"
	if tokens.IsKeyword(p.s.tok) {
		tokType = "keyword"
	} else if _, ok := allFutureKeywords[p.s.tok.String()]; ok {
		tokType = "keyword"
	}

	note = fmt.Sprintf(note, a...)
	if len(note) > 0 {
		p.errorf(p.s.Loc(), "unexpected %s %s: %s", tok, tokType, note)
	} else {
		p.errorf(p.s.Loc(), "unexpected %s %s", tok, tokType)
	}
}

func (p *Parser) illegalToken() {
	p.illegal("")
}

func (p *Parser) scan() {
	p.doScan(true)
}

func (p *Parser) scanWS() {
	p.doScan(false)
}

func (p *Parser) doScan(skipws bool) {

	// NOTE(tsandall): the last position is used to compute the "text" field for
	// complex AST nodes. Whitespace never affects the last position of an AST
	// node so do not update it when scanning.
	if p.s.tok != tokens.Whitespace {
		p.s.lastEnd = p.s.tokEnd
		p.s.skippedNL = false
	}

	var errs []scanner.Error
	for {
		var pos scanner.Position
		p.s.tok, pos, p.s.lit, errs = p.s.s.Scan()

		p.s.tokEnd = pos.End
		p.s.loc.Row = pos.Row
		p.s.loc.Col = pos.Col
		p.s.loc.Offset = pos.Offset
		p.s.loc.Text = p.s.Text(pos.Offset, pos.End)
		p.s.loc.Tabs = pos.Tabs

		for _, err := range errs {
			p.error(p.s.Loc(), err.Message)
		}

		if len(errs) > 0 {
			p.s.tok = tokens.Illegal
		}

		if p.s.tok == tokens.Whitespace {
			if p.s.lit == "\n" {
				p.s.skippedNL = true
			}
			if skipws {
				continue
			}
		}

		if p.s.tok != tokens.Comment {
			break
		}

		// For backwards compatibility leave a nil
		// Text value if there is no text rather than
		// an empty string.
		var commentText []byte
		if len(p.s.lit) > 1 {
			commentText = []byte(p.s.lit[1:])
		}
		comment := NewComment(commentText)
		comment.SetLoc(p.s.Loc())
		p.s.comments = append(p.s.comments, comment)
	}
}

func (p *Parser) save() *state {
	cpy := *p.s
	s := *cpy.s
	cpy.s = &s
	return &cpy
}

func (p *Parser) restore(s *state) {
	p.s = s
}

func setLocRecursive(x any, loc *location.Location) {
	NewGenericVisitor(func(x any) bool {
		if node, ok := x.(Node); ok {
			node.SetLoc(loc)
		}
		return false
	}).Walk(x)
}

func (p *Parser) setLoc(term *Term, loc *location.Location, offset, end int) *Term {
	if term != nil {
		cpy := *loc
		term.Location = &cpy
		term.Location.Text = p.s.Text(offset, end)
	}
	return term
}

func (p *Parser) validateDefaultRuleValue(rule *Rule) bool {
	if rule.Head.Value == nil {
		p.error(rule.Loc(), "illegal default rule (must have a value)")
		return false
	}

	valid := true
	vis := NewGenericVisitor(func(x any) bool {
		switch x.(type) {
		case *ArrayComprehension, *ObjectComprehension, *SetComprehension: // skip closures
			return true
		case Ref, Var, Call:
			p.error(rule.Loc(), fmt.Sprintf("illegal default rule (value cannot contain %v)", TypeName(x)))
			valid = false
			return true
		}
		return false
	})

	vis.Walk(rule.Head.Value.Value)
	return valid
}

func (p *Parser) validateDefaultRuleArgs(rule *Rule) bool {

	valid := true
	vars := NewVarSet()

	vis := NewGenericVisitor(func(x any) bool {
		switch x := x.(type) {
		case Var:
			if vars.Contains(x) {
				p.error(rule.Loc(), fmt.Sprintf("illegal default rule (arguments cannot be repeated %v)", x))
				valid = false
				return true
			}
			vars.Add(x)

		case *Term:
			switch v := x.Value.(type) {
			case Var: // do nothing
			default:
				p.error(rule.Loc(), fmt.Sprintf("illegal default rule (arguments cannot contain %v)", ValueName(v)))
				valid = false
				return true
			}
		}

		return false
	})

	vis.Walk(rule.Head.Args)
	return valid
}

// We explicitly use yaml unmarshalling, to accommodate for the '_' in 'related_resources',
// which isn't handled properly by json for some reason.
type rawAnnotation struct {
	Scope            string           `yaml:"scope"`
	Title            string           `yaml:"title"`
	Entrypoint       bool             `yaml:"entrypoint"`
	Description      string           `yaml:"description"`
	Organizations    []string         `yaml:"organizations"`
	RelatedResources []any            `yaml:"related_resources"`
	Authors          []any            `yaml:"authors"`
	Schemas          []map[string]any `yaml:"schemas"`
	Custom           map[string]any   `yaml:"custom"`
}

type metadataParser struct {
	buf      *bytes.Buffer
	comments []*Comment
	loc      *location.Location
}

func newMetadataParser(loc *Location) *metadataParser {
	return &metadataParser{loc: loc, buf: bytes.NewBuffer(nil)}
}

func (b *metadataParser) Append(c *Comment) {
	b.buf.Write(bytes.TrimPrefix(c.Text, []byte(" ")))
	b.buf.WriteByte('\n')
	b.comments = append(b.comments, c)
}

var yamlLineErrRegex = regexp.MustCompile(`^yaml:(?: unmarshal errors:[\n\s]*)? line ([[:digit:]]+):`)

func (b *metadataParser) Parse() (*Annotations, error) {

	var raw rawAnnotation

	if len(bytes.TrimSpace(b.buf.Bytes())) == 0 {
		return nil, errors.New("expected METADATA block, found whitespace")
	}

	if err := yaml.Unmarshal(b.buf.Bytes(), &raw); err != nil {
		var comment *Comment
		match := yamlLineErrRegex.FindStringSubmatch(err.Error())
		if len(match) == 2 {
			index, err2 := strconv.Atoi(match[1])
			if err2 == nil {
				if index >= len(b.comments) {
					comment = b.comments[len(b.comments)-1]
				} else {
					comment = b.comments[index]
				}
				b.loc = comment.Location
			}
		}

		if match == nil && len(b.comments) > 0 {
			b.loc = b.comments[0].Location
		}

		return nil, augmentYamlError(err, b.comments)
	}

	var result Annotations
	result.comments = b.comments
	result.Scope = raw.Scope
	result.Entrypoint = raw.Entrypoint
	result.Title = raw.Title
	result.Description = raw.Description
	result.Organizations = raw.Organizations

	for _, v := range raw.RelatedResources {
		rr, err := parseRelatedResource(v)
		if err != nil {
			return nil, fmt.Errorf("invalid related-resource definition %s: %w", v, err)
		}
		result.RelatedResources = append(result.RelatedResources, rr)
	}

	for _, pair := range raw.Schemas {
		k, v := unwrapPair(pair)

		var a SchemaAnnotation
		var err error

		a.Path, err = ParseRef(k)
		if err != nil {
			return nil, errors.New("invalid document reference")
		}

		switch v := v.(type) {
		case string:
			a.Schema, err = parseSchemaRef(v)
			if err != nil {
				return nil, err
			}
		case map[string]any:
			w, err := convertYAMLMapKeyTypes(v, nil)
			if err != nil {
				return nil, fmt.Errorf("invalid schema definition: %w", err)
			}
			a.Definition = &w
		default:
			return nil, fmt.Errorf("invalid schema declaration for path %q", k)
		}

		result.Schemas = append(result.Schemas, &a)
	}

	for _, v := range raw.Authors {
		author, err := parseAuthor(v)
		if err != nil {
			return nil, fmt.Errorf("invalid author definition %s: %w", v, err)
		}
		result.Authors = append(result.Authors, author)
	}

	result.Custom = make(map[string]any)
	for k, v := range raw.Custom {
		val, err := convertYAMLMapKeyTypes(v, nil)
		if err != nil {
			return nil, err
		}
		result.Custom[k] = val
	}

	result.Location = b.loc

	// recreate original text of entire metadata block for location text attribute
	sb := strings.Builder{}
	sb.WriteString("# METADATA\n")

	lines := bytes.Split(b.buf.Bytes(), []byte{'\n'})

	for _, line := range lines[:len(lines)-1] {
		sb.WriteString("# ")
		sb.Write(line)
		sb.WriteByte('\n')
	}

	result.Location.Text = []byte(strings.TrimSuffix(sb.String(), "\n"))

	return &result, nil
}

// augmentYamlError augments a YAML error with hints intended to help the user figure out the cause of an otherwise
// cryptic error. These are hints, instead of proper errors, because they are educated guesses, and aren't guaranteed
// to be correct.
func augmentYamlError(err error, comments []*Comment) error {
	// Adding hints for when key/value ':' separator isn't suffixed with a legal YAML space symbol
	for _, comment := range comments {
		txt := string(comment.Text)
		parts := strings.Split(txt, ":")
		if len(parts) > 1 {
			parts = parts[1:]
			var invalidSpaces []string
			for partIndex, part := range parts {
				if len(part) == 0 && partIndex == len(parts)-1 {
					invalidSpaces = []string{}
					break
				}

				r, _ := utf8.DecodeRuneInString(part)
				if r == ' ' || r == '\t' {
					invalidSpaces = []string{}
					break
				}

				invalidSpaces = append(invalidSpaces, fmt.Sprintf("%+q", r))
			}
			if len(invalidSpaces) > 0 {
				err = fmt.Errorf(
					"%s\n  Hint: on line %d, symbol(s) %v immediately following a key/value separator ':' is not a legal yaml space character",
					err.Error(), comment.Location.Row, invalidSpaces)
			}
		}
	}
	return err
}

func unwrapPair(pair map[string]any) (string, any) {
	for k, v := range pair {
		return k, v
	}
	return "", nil
}

var errInvalidSchemaRef = errors.New("invalid schema reference")

// NOTE(tsandall): 'schema' is not registered as a root because it's not
// supported by the compiler or evaluator today. Once we fix that, we can remove
// this function.
func parseSchemaRef(s string) (Ref, error) {

	term, err := ParseTerm(s)
	if err == nil {
		switch v := term.Value.(type) {
		case Var:
			if term.Equal(SchemaRootDocument) {
				return SchemaRootRef.Copy(), nil
			}
		case Ref:
			if v.HasPrefix(SchemaRootRef) {
				return v, nil
			}
		}
	}

	return nil, errInvalidSchemaRef
}

func parseRelatedResource(rr any) (*RelatedResourceAnnotation, error) {
	rr, err := convertYAMLMapKeyTypes(rr, nil)
	if err != nil {
		return nil, err
	}

	switch rr := rr.(type) {
	case string:
		if len(rr) > 0 {
			u, err := url.Parse(rr)
			if err != nil {
				return nil, err
			}
			return &RelatedResourceAnnotation{Ref: *u}, nil
		}
		return nil, errors.New("ref URL may not be empty string")
	case map[string]any:
		description := strings.TrimSpace(getSafeString(rr, "description"))
		ref := strings.TrimSpace(getSafeString(rr, "ref"))
		if len(ref) > 0 {
			u, err := url.Parse(ref)
			if err != nil {
				return nil, err
			}
			return &RelatedResourceAnnotation{Description: description, Ref: *u}, nil
		}
		return nil, errors.New("'ref' value required in object")
	}

	return nil, errors.New("invalid value type, must be string or map")
}

func parseAuthor(a any) (*AuthorAnnotation, error) {
	a, err := convertYAMLMapKeyTypes(a, nil)
	if err != nil {
		return nil, err
	}

	switch a := a.(type) {
	case string:
		return parseAuthorString(a)
	case map[string]any:
		name := strings.TrimSpace(getSafeString(a, "name"))
		email := strings.TrimSpace(getSafeString(a, "email"))
		if len(name) > 0 || len(email) > 0 {
			return &AuthorAnnotation{name, email}, nil
		}
		return nil, errors.New("'name' and/or 'email' values required in object")
	}

	return nil, errors.New("invalid value type, must be string or map")
}

func getSafeString(m map[string]any, k string) string {
	if v, found := m[k]; found {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

const emailPrefix = "<"
const emailSuffix = ">"

// parseAuthor parses a string into an AuthorAnnotation. If the last word of the input string is enclosed within <>,
// it is extracted as the author's email. The email may not contain whitelines, as it then will be interpreted as
// multiple words.
func parseAuthorString(s string) (*AuthorAnnotation, error) {
	parts := strings.Fields(s)

	if len(parts) == 0 {
		return nil, errors.New("author is an empty string")
	}

	namePartCount := len(parts)
	trailing := parts[namePartCount-1]
	var email string
	if len(trailing) >= len(emailPrefix)+len(emailSuffix) && strings.HasPrefix(trailing, emailPrefix) &&
		strings.HasSuffix(trailing, emailSuffix) {
		email = trailing[len(emailPrefix):]
		email = email[0 : len(email)-len(emailSuffix)]
		namePartCount -= 1
	}

	name := strings.Join(parts[0:namePartCount], " ")

	return &AuthorAnnotation{Name: name, Email: email}, nil
}

func convertYAMLMapKeyTypes(x any, path []string) (any, error) {
	var err error
	switch x := x.(type) {
	case map[any]any:
		result := make(map[string]any, len(x))
		for k, v := range x {
			str, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("invalid map key type(s): %v", strings.Join(path, "/"))
			}
			result[str], err = convertYAMLMapKeyTypes(v, append(path, str))
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	case []any:
		for i := range x {
			x[i], err = convertYAMLMapKeyTypes(x[i], append(path, strconv.Itoa(i)))
			if err != nil {
				return nil, err
			}
		}
		return x, nil
	default:
		return x, nil
	}
}

// futureKeywords is the source of truth for future keywords that will
// eventually become standard keywords inside of Rego.
var futureKeywords = map[string]tokens.Token{}

// futureKeywordsV0 is the source of truth for future keywords that were
// not yet a standard part of Rego in v0, and required importing.
var futureKeywordsV0 = map[string]tokens.Token{
	"in":       tokens.In,
	"every":    tokens.Every,
	"contains": tokens.Contains,
	"if":       tokens.If,
}

var allFutureKeywords map[string]tokens.Token

func IsFutureKeyword(s string) bool {
	return IsFutureKeywordForRegoVersion(s, RegoV1)
}

func IsFutureKeywordForRegoVersion(s string, v RegoVersion) bool {
	var yes bool

	switch v {
	case RegoV0, RegoV0CompatV1:
		_, yes = futureKeywordsV0[s]
	case RegoV1:
		_, yes = futureKeywords[s]
	}

	return yes
}

func (p *Parser) futureImport(imp *Import, allowedFutureKeywords map[string]tokens.Token) {
	path := imp.Path.Value.(Ref)

	if len(path) == 1 || !path[1].Equal(InternedTerm("keywords")) {
		p.errorf(imp.Path.Location, "invalid import, must be `future.keywords`")
		return
	}

	if imp.Alias != "" {
		p.errorf(imp.Path.Location, "`future` imports cannot be aliased")
		return
	}

	kwds := make([]string, 0, len(allowedFutureKeywords))
	for k := range allowedFutureKeywords {
		kwds = append(kwds, k)
	}

	switch len(path) {
	case 2: // all keywords imported, nothing to do
	case 3: // one keyword imported
		kw, ok := path[2].Value.(String)
		if !ok {
			p.errorf(imp.Path.Location, "invalid import, must be `future.keywords.x`, e.g. `import future.keywords.in`")
			return
		}
		keyword := string(kw)
		_, ok = allowedFutureKeywords[keyword]
		if !ok {
			sort.Strings(kwds) // so the error message is stable
			p.errorf(imp.Path.Location, "unexpected keyword, must be one of %v", kwds)
			return
		}

		kwds = []string{keyword} // overwrite
	}
	for _, kw := range kwds {
		p.s.s.AddKeyword(kw, allowedFutureKeywords[kw])
	}
}

func (p *Parser) regoV1Import(imp *Import) {
	if !p.po.Capabilities.ContainsFeature(FeatureRegoV1Import) && !p.po.Capabilities.ContainsFeature(FeatureRegoV1) {
		p.errorf(imp.Path.Location, "invalid import, `%s` is not supported by current capabilities", RegoV1CompatibleRef)
		return
	}

	path := imp.Path.Value.(Ref)

	// v1 is only valid option
	if len(path) == 1 || !path[1].Equal(RegoV1CompatibleRef[1]) || len(path) > 2 {
		p.errorf(imp.Path.Location, "invalid import `%s`, must be `%s`", path, RegoV1CompatibleRef)
		return
	}

	if p.po.EffectiveRegoVersion() == RegoV1 {
		// We're parsing for Rego v1, where the 'rego.v1' import is a no-op.
		return
	}

	if imp.Alias != "" {
		p.errorf(imp.Path.Location, "`rego` imports cannot be aliased")
		return
	}

	// import all future keywords with the rego.v1 import
	kwds := make([]string, 0, len(futureKeywordsV0))
	for k := range futureKeywordsV0 {
		kwds = append(kwds, k)
	}

	p.s.s.SetRegoV1Compatible()
	for _, kw := range kwds {
		p.s.s.AddKeyword(kw, futureKeywordsV0[kw])
	}
}

func init() {
	allFutureKeywords = map[string]tokens.Token{}
	maps.Copy(allFutureKeywords, futureKeywords)
	maps.Copy(allFutureKeywords, futureKeywordsV0)
}

// enter increments the recursion depth counter and checks if it exceeds the maximum.
// Returns false if the maximum is exceeded, true otherwise.
// If p.maxRecursionDepth is 0 or negative, the check is effectively disabled.
func (p *Parser) enter() bool {
	p.recursionDepth++
	if p.maxRecursionDepth > 0 && p.recursionDepth > p.maxRecursionDepth {
		p.error(p.s.Loc(), ErrMaxParsingRecursionDepthExceeded.Error())
		p.recursionDepth--
		return false
	}
	return true
}

// leave decrements the recursion depth counter.
func (p *Parser) leave() {
	p.recursionDepth--
}
