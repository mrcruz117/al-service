// Package checkapi maintains the web based api for system access
package checkapi

import (
	"context"
	"net/http"

	"github.com/jmoiron/sqlx"
	"github.com/mrcruz117/al-service/foundation/web"
)

type api struct {
	db *sqlx.DB
}

func newAPI(db *sqlx.DB) *api {
	return &api{
		db: db,
	}
}

func (api *api) liveness(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	status := struct {
		Status string
	}{
		Status: "OK",
	}

	return web.Respond(ctx, w, status, http.StatusOK)

}

func (api *api) readiness(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	status := struct {
		Status string
	}{
		Status: "OK",
	}

	return web.Respond(ctx, w, status, http.StatusOK)
}
