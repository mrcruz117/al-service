package mid

import (
	"context"
	"net/http"

	"github.com/mrcruz117/al-service/app/api/mid"
	"github.com/mrcruz117/al-service/foundation/logger"
	"github.com/mrcruz117/al-service/foundation/web"
)

// Logger writes information about the request to the logs.
func Logger(log *logger.Logger) web.MidHandler {
	m := func(handler web.Handler) web.Handler {
		h := func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
			hdl := func(ctx context.Context) error {
				return handler(ctx, w, r)
			}

			return mid.Logger(ctx, log, r.URL.Path, r.URL.RawQuery, r.Method, r.RemoteAddr, hdl)
		}

		return h
	}

	return m
}
