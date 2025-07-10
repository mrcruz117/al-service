// Package mux provides support to bind domain level routes
// to the application mux.
package mux

import (
	"os"

	"github.com/mrcruz117/al-service/apis/services/sales/route/sys/checkapi"
	"github.com/mrcruz117/al-service/foundation/web"
)

// WebAPI constructs an http.Handler with all the application routes bound
func WebAPI(shutdown chan os.Signal) *web.App {
	mux := web.NewApp(shutdown)

	checkapi.Routes(mux)

	return mux
}
