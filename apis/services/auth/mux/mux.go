// Package mux provides support to bind domain level routes
// to the application mux.
package mux

import (
	"os"

	"github.com/mrcruz117/al-service/apis/services/api/mid"
	"github.com/mrcruz117/al-service/apis/services/auth/route/checkapi"
	"github.com/mrcruz117/al-service/business/api/auth"
	"github.com/mrcruz117/al-service/foundation/logger"
	"github.com/mrcruz117/al-service/foundation/web"
)

// WebAPIAuth constructs a http.Handler with all application routes bound.
func WebAPI(log *logger.Logger, auth *auth.Auth, shutdown chan os.Signal) *web.App {
	mux := web.NewApp(
		shutdown, mid.Logger(log), mid.Errors(log), mid.Metrics(), mid.Panics(),
	)
	checkapi.Routes(mux, auth)

	return mux
}
