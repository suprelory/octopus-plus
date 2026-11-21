package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/utils/safe"
)

type WorkScope interface {
	Begin(context.Context) (context.Context, func(), error)
}

const runtimeWorkKey = "octopus.runtime.work"

func RuntimeWork(work WorkScope) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, release, err := work.Begin(c.Request.Context())
		if err != nil {
			code := ""
			if strings.HasPrefix(c.Request.URL.Path, "/api/v1/checkin/") {
				code = "checkin.unavailable"
			}
			resp.ErrorWithCode(c, http.StatusServiceUnavailable, code, "service is stopping")
			return
		}
		defer release()
		c.Set(runtimeWorkKey, work)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// RunBackground keeps postprocessing independent of the HTTP request lifetime,
// but registered with the application's shutdown barrier before starting it.
func RunBackground(c *gin.Context, name string, run func(context.Context)) {
	ctx := context.WithoutCancel(c.Request.Context())
	release := func() {}
	if value, ok := c.Get(runtimeWorkKey); ok {
		var err error
		ctx, release, err = value.(WorkScope).Begin(ctx)
		if err != nil {
			return
		}
	}
	safe.Go(name, func() {
		defer release()
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		run(ctx)
	})
}
