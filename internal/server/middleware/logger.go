package middleware

import (
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

type LoggerConfig struct {
	Enabled       bool
	SlowThreshold time.Duration
}

// RequestObservedKey is set when a specialized request boundary has already
// emitted its final diagnostic. Explicit access logging still records access.
const RequestObservedKey = "octopus.request_observed"

func Logger(cfg LoggerConfig) gin.HandlerFunc {
	if cfg.SlowThreshold <= 0 {
		cfg.SlowThreshold = 3 * time.Second
	}
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()
		audit := isAdminOperation(c)
		auth, hasAuth := c.Get(authEventKey)
		if c.GetBool(RequestObservedKey) && !cfg.Enabled && !audit && !hasAuth {
			return
		}
		shouldLog := audit || hasAuth || cfg.Enabled || status >= 500 || latency >= cfg.SlowThreshold || len(c.Errors) > 0
		if !shouldLog {
			return
		}
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}

		fields := []interface{}{
			"method", c.Request.Method,
			"path", log.SafeText(path),
			"status", status,
			"latency", latency.String(),
			"latency_ms", latency.Milliseconds(),
			"ip", ClientIP(c),
		}
		if keyID := c.GetInt("api_key_id"); keyID > 0 {
			fields = append(fields, "api_key_id", keyID)
		}
		if log.IsDebugEnabled() && c.Request.URL.RawQuery != "" {
			var keys []string
			for key := range c.Request.URL.Query() {
				keys = append(keys, log.SafeText(key))
			}
			sort.Strings(keys)
			fields = append(fields, "query_keys", keys)
		}
		details := resp.RequestError(c)
		if details.Code != "" {
			fields = append(fields, "error_code", details.Code)
		}
		if details.Cause != "" {
			fields = append(fields, "error", details.Cause)
		} else if details.Message != "" {
			fields = append(fields, "error", details.Message)
		}
		if len(c.Errors) > 0 {
			fields = append(fields, "context_errors", log.SafeText(c.Errors.String()))
		}
		if hasAuth {
			event := auth.(authEvent)
			fields = append(fields, "action", event.Action, "reason", event.Reason)
			if event.KeyID > 0 {
				fields = append(fields, "credential_id", event.KeyID)
			}
			if status >= 400 {
				allowed, suppressed := authLogs.allow(event.Action+":"+event.Reason+":"+ClientIP(c), time.Now())
				if !allowed {
					return
				}
				fields = append(fields, "suppressed", suppressed)
				log.Warnw("auth.event", fields...)
			} else {
				log.Infow("auth.event", fields...)
			}
			return
		}
		if audit {
			if id := resp.ResponseResourceID(c); id > 0 {
				fields = append(fields, "result_id", id)
			}
			for _, param := range c.Params {
				if id, err := strconv.ParseInt(param.Value, 10, 64); err == nil && id > 0 {
					fields = append(fields, param.Key, id)
				}
			}
			if metadata, exists := c.Get(auditFieldsKey); exists {
				values := metadata.(map[string]any)
				keys := make([]string, 0, len(values))
				for key := range values {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					fields = append(fields, key, values[key])
				}
			}
			success := status < 400
			if result, exists := c.Get(auditResultKey); exists {
				success = success && result.(bool)
			}
			fields = append(fields, "success", success)
			switch {
			case status >= 500 || len(c.Errors) > 0:
				log.Errorw("admin.operation", fields...)
			case !success:
				log.Warnw("admin.operation", fields...)
			default:
				log.Infow("admin.operation", fields...)
			}
			return
		}

		switch {
		case status >= 500 || len(c.Errors) > 0:
			log.Errorw("http.request", fields...)
		case latency >= cfg.SlowThreshold:
			log.Warnw("http.slow", fields...)
		default:
			if cfg.Enabled {
				log.Infow("http.request", fields...)
			} else {
				log.Debugw("http.request", fields...)
			}
		}
	}
}
