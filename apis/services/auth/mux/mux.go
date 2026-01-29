// Package mux provides support to bind domain level routes
// to the application mux.
package mux

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/mrcruz117/al-service/apis/services/api/mid"
	"github.com/mrcruz117/al-service/apis/services/auth/route/authapi"
	"github.com/mrcruz117/al-service/apis/services/auth/route/checkapi"
	"github.com/mrcruz117/al-service/business/api/auth"
	"github.com/mrcruz117/al-service/foundation/logger"
	"github.com/mrcruz117/al-service/foundation/web"
)

// WebAPIAuth constructs a http.Handler with all application routes bound.
func WebAPI(build string, log *logger.Logger, db *sqlx.DB, auth *auth.Auth) *web.App {

	logger := func(ctx context.Context, msg string, v ...any) {
		log.Info(ctx, msg, v...)
	}

	app := web.NewApp(logger, mid.Logger(log), mid.Errors(log), mid.Metrics(), mid.Panics())

	checkapi.Routes(build, app, log, db)
	authapi.Routes(app, auth)

	return app
}
