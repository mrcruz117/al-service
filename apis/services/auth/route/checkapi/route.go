package checkapi

import (
	"github.com/mrcruz117/al-service/business/api/auth"
	"github.com/mrcruz117/al-service/foundation/web"
)

// Routes adds specific routes for this group.
func Routes(app *web.App, a *auth.Auth) {

	app.HandleFuncNoMiddleware("GET /liveness", liveness)
	app.HandleFuncNoMiddleware("GET /readiness", readiness)

}
