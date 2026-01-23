// Package mux provides support to bind domain level routes
// to the application mux.
package mux

import (
	"os"

	"github.com/mrcruz117/al-service/apis/services/api/mid"
	"github.com/mrcruz117/al-service/apis/services/sales/route/checkapi"
	"github.com/mrcruz117/al-service/app/api/authclient"
	"github.com/mrcruz117/al-service/foundation/logger"
	"github.com/mrcruz117/al-service/foundation/web"
)

// WebAPI constructs an http.Handler with all the application routes bound
func WebAPI(log *logger.Logger, authClient *authclient.Client, shutdown chan os.Signal) *web.App {
	app := web.NewApp(shutdown, mid.Logger(log), mid.Errors(log), mid.Metrics(), mid.Panics())

	checkapi.Routes(app, log, authClient)

	return app
}
