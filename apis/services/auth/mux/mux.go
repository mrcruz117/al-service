// Package mux provides support to bind domain level routes
// to the application mux.
package mux

import (
	"os"

	"github.com/jmoiron/sqlx"
	"github.com/mrcruz117/al-service/apis/services/api/mid"
	"github.com/mrcruz117/al-service/apis/services/auth/route/authapi"
	"github.com/mrcruz117/al-service/apis/services/auth/route/checkapi"
	"github.com/mrcruz117/al-service/business/api/auth"
	"github.com/mrcruz117/al-service/foundation/logger"
	"github.com/mrcruz117/al-service/foundation/web"
)

// WebAPIAuth constructs a http.Handler with all application routes bound.
func WebAPI(log *logger.Logger, db *sqlx.DB, auth *auth.Auth, shutdown chan os.Signal) *web.App {
	app := web.NewApp(
		shutdown, mid.Logger(log), mid.Errors(log), mid.Metrics(), mid.Panics(),
	)
	checkapi.Routes(app, db)
	authapi.Routes(app, auth)

	return app
}
