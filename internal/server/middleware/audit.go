package middleware

import (
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

const adminAuthenticatedKey = "octopus.admin_authenticated"
const auditFieldsKey = "octopus.audit_fields"
const auditResultKey = "octopus.audit_result"
const authEventKey = "octopus.auth_event"

type authEvent struct {
	Action string
	Reason string
	KeyID  int
}

// RecordAuthEvent leaves emission to the request logger, where repeated
// rejections can be sampled and carry the final status and trusted client IP.
func RecordAuthEvent(c *gin.Context, action, reason string, keyID int) {
	c.Set(authEventKey, authEvent{Action: action, Reason: reason, KeyID: keyID})
}

// AuditFields accepts explicit operation metadata, never a request object or
// credentials. The logger emits it once when the operation finishes.
func AuditFields(c *gin.Context, fields ...any) {
	values := map[string]any{}
	if previous, exists := c.Get(auditFieldsKey); exists {
		values, _ = previous.(map[string]any)
	}
	if values == nil {
		values = map[string]any{}
	}
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			continue
		}
		value := fields[i+1]
		if text, ok := value.(string); ok {
			value = log.SafeText(text)
		}
		values[key] = value
	}
	c.Set(auditFieldsKey, values)
}

// AuditChanges records schema field names only. Pointer update fields preserve
// explicit false/zero changes without serializing their potentially secret values.
func AuditChanges(c *gin.Context, resourceID int, request any) {
	if resourceID > 0 {
		AuditFields(c, "resource_id", resourceID)
	}
	value := reflect.ValueOf(request)
	if value.Kind() == reflect.Pointer && !value.IsNil() {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return
	}
	var fields []string
	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if field.PkgPath != "" || name == "" || name == "-" || name == "id" || value.Field(i).IsZero() {
			continue
		}
		fields = append(fields, name)
	}
	sort.Strings(fields)
	AuditFields(c, "changed_fields", fields)
}

func AuditResult(c *gin.Context, success bool, fields ...any) {
	c.Set(auditResultKey, success)
	AuditFields(c, fields...)
}

func isAdminOperation(c *gin.Context) bool {
	if !c.GetBool(adminAuthenticatedKey) || !strings.HasPrefix(c.FullPath(), "/api/v1/") {
		return false
	}
	switch c.FullPath() {
	case "/api/v1/group/preview", "/api/v1/site/detect", "/api/v1/site/account/manual-sync/preview/:id":
		return false
	}
	if c.Request.Method == http.MethodGet {
		return c.FullPath() == "/api/v1/setting/export"
	}
	return c.Request.Method != http.MethodHead && c.Request.Method != http.MethodOptions
}

type authLogWindow struct {
	until      time.Time
	suppressed int
}

type authLogLimiter struct {
	mu       sync.Mutex
	windows  map[string]authLogWindow
	overflow authLogWindow
}

var authLogs = authLogLimiter{windows: make(map[string]authLogWindow)}

// Bound both log volume and memory when many different source IPs are seen.
func (l *authLogLimiter) allow(key string, now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	window, exists := l.windows[key]
	if !exists && len(l.windows) >= 1024 {
		for existing, entry := range l.windows {
			if !now.Before(entry.until) {
				delete(l.windows, existing)
			}
		}
	}
	overflow := !exists && len(l.windows) >= 1024
	if overflow {
		window = l.overflow
	}
	allowed := !now.Before(window.until)
	suppressed := window.suppressed
	if allowed {
		window = authLogWindow{until: now.Add(time.Minute)}
	} else {
		window.suppressed++
	}
	if overflow {
		l.overflow = window
	} else {
		l.windows[key] = window
	}
	return allowed, suppressed
}
