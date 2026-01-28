package checkapi

import (
	"github.com/jmoiron/sqlx"
	"github.com/mrcruz117/al-service/foundation/web"
)

// Routes adds specific routes for this group.
func Routes(app *web.App, db *sqlx.DB) {

	api := newAPI(db)

	app.HandleFuncNoMiddleware("GET /liveness", api.liveness)
	app.HandleFuncNoMiddleware("GET /readiness", api.readiness)
}
