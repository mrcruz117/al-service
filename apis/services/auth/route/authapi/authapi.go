// Package authapi maintains the web based api for auth access.
package authapi

import (
	"context"
	"net/http"

	"github.com/mrcruz117/al-service/app/api/errs"
	"github.com/mrcruz117/al-service/app/api/mid"
	"github.com/mrcruz117/al-service/business/api/auth"
	"github.com/mrcruz117/al-service/foundation/web"
)

type api struct {
	auth *auth.Auth
}

func newAPI(auth *auth.Auth) *api {
	return &api{
		auth: auth,
	}
}

func (api *api) token(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	kid := web.Param(r, "kid")
	if kid == "" {
		return errs.Newf(errs.FailedPrecondition, "missing kid")
	}

	claims := mid.GetClaims(ctx)

	tkn, err := api.auth.GenerateToken(kid, claims)

	if err != nil {
		return errs.New(errs.Internal, err)
	}

	token := struct {
		Token string `json:"token"`
	}{
		Token: tkn,
	}

	return web.Respond(ctx, w, token, http.StatusOK)
}
