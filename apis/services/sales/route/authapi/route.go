package authapi

import (
	"github.com/mrcruz117/al-service/business/api/auth"
)

// Config contains all the mandatory systems required by handlers.
type Config struct {
	Auth *auth.Auth
}

// // Routes adds specific routes for this group.
// func Routes(app *web.App, cfg Config) {
// 	bearer := mid.Bearer(cfg.Auth)
// 	basic := mid.Basic(cfg.Auth)

// 	api := newAPI(cfg.Auth)

// 	app.HandleFunc("GET /auth/token/{kid}", api.token, basic)
// 	app.HandleFunc("GET /auth/authenticate", api.authenticate, bearer)
// 	app.HandleFunc("POST /auth/authorize", api.authorize)
// }
