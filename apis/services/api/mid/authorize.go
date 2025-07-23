package mid

import (
	"context"
	"net/http"

	"github.com/mrcruz117/al-service/app/api/mid"
	"github.com/mrcruz117/al-service/business/api/auth"
	"github.com/mrcruz117/al-service/foundation/web"
)

// Authorize executes the authorize middleware functionality.
func Authorize(auth *auth.Auth, rule string) web.MidHandler {
	m := func(handler web.Handler) web.Handler {
		h := func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
			hdl := func(ctx context.Context) error {
				return handler(ctx, w, r)
			}

			return mid.Authorize(ctx, auth, rule, hdl)
		}

		return h
	}

	return m
}
