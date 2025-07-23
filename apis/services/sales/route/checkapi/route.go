package checkapi

import (
	"github.com/mrcruz117/al-service/apis/services/api/mid"
	"github.com/mrcruz117/al-service/business/api/auth"
	"github.com/mrcruz117/al-service/foundation/web"
)

// Routes adds specific routes for this group.
func Routes(app *web.App, a *auth.Auth) {

	authen := mid.Authorization(a)
	authAdminOnly := mid.Authorize(a, auth.RuleAdminOnly)

	app.HandleFuncNoMiddleware("GET /liveness", liveness)
	app.HandleFuncNoMiddleware("GET /readiness", readiness)
	app.HandleFunc("GET /testerror", testError)
	app.HandleFunc("GET /testpanic", testPanic)
	app.HandleFunc("GET /testauth", liveness, authen, authAdminOnly)
}
