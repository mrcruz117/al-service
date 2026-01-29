package checkapi

import (
	"github.com/jmoiron/sqlx"
	"github.com/mrcruz117/al-service/api/http/api/mid"
	"github.com/mrcruz117/al-service/app/api/auth"
	"github.com/mrcruz117/al-service/app/api/authclient"
	"github.com/mrcruz117/al-service/foundation/logger"
	"github.com/mrcruz117/al-service/foundation/web"
)

// Routes adds specific routes for this group.
func Routes(build string, app *web.App, log *logger.Logger, db *sqlx.DB, authClient *authclient.Client) {

	authen := mid.Authenticate(log, authClient)
	authAdminOnly := mid.Authorize(log, authClient, auth.RuleAdminOnly)

	api := newAPI(build, log, db)

	app.HandleFuncNoMiddleware("GET /liveness", api.liveness)
	app.HandleFuncNoMiddleware("GET /readiness", api.readiness)
	app.HandleFunc("GET /testerror", api.testError)
	app.HandleFunc("GET /testpanic", api.testPanic)
	app.HandleFunc("GET /testauth", api.liveness, authen, authAdminOnly)
}
