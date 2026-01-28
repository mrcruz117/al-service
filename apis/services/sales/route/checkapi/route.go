package checkapi

import (
	"github.com/jmoiron/sqlx"
	"github.com/mrcruz117/al-service/apis/services/api/mid"
	"github.com/mrcruz117/al-service/app/api/authclient"
	"github.com/mrcruz117/al-service/business/api/auth"
	"github.com/mrcruz117/al-service/foundation/logger"
	"github.com/mrcruz117/al-service/foundation/web"
)

// Routes adds specific routes for this group.
func Routes(app *web.App, log *logger.Logger, db *sqlx.DB, authClient *authclient.Client) {

	authen := mid.Authenticate(log, authClient)
	authAdminOnly := mid.Authorize(log, authClient, auth.RuleAdminOnly)

	app.HandleFuncNoMiddleware("GET /liveness", liveness)
	app.HandleFuncNoMiddleware("GET /readiness", readiness)
	app.HandleFunc("GET /testerror", testError)
	app.HandleFunc("GET /testpanic", testPanic)
	app.HandleFunc("GET /testauth", liveness, authen, authAdminOnly)
}
