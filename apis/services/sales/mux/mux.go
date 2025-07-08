// Package mux provides support to bind domain level routes
// to the application mux.
package mux

import (
	"net/http"

	"github.com/mrcruz117/al-service/apis/services/sales/route/sys/checkapi"
)

// WebAPI constructs an http.Handler with all the application routes bound
func WebAPI() http.Handler {
	mux := http.NewServeMux()

	checkapi.Routes(mux)

	return mux
}
