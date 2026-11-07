package resp

import (
	"reflect"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

const errorDetailsKey = "octopus.response_error"
const errorLogOverrideKey = "octopus.response_error_log_override"
const resourceIDKey = "octopus.response_resource_id"

// ErrorDetails contains only sanitized diagnostics for the request logger.
// It is separate from the public response and never includes request data.
type ErrorDetails struct {
	Code    string
	Message string
	Cause   string
}

func RequestError(c *gin.Context) ErrorDetails {
	if value, exists := c.Get(errorDetailsKey); exists {
		if details, ok := value.(ErrorDetails); ok {
			return details
		}
	}
	return ErrorDetails{}
}

func recordError(c *gin.Context, code, message string, err error) {
	if value, exists := c.Get(errorLogOverrideKey); exists {
		c.Set(errorDetailsKey, value.(ErrorDetails))
		return
	}
	details := RequestError(c)
	details.Code = code
	details.Message = log.SafeText(message)
	if err != nil {
		details.Cause = log.SafeError(err)
	}
	c.Set(errorDetailsKey, details)
}

// UseSafeErrorLogDetails separates diagnostics from untrusted public response
// messages (for example provider errors which echo prompts or credentials).
// Apply only trusted constant/classification values here. The override takes
// effect when an error is recorded; successful requests get no error fields.
func UseSafeErrorLogDetails(c *gin.Context, code, message string) {
	c.Set(errorLogOverrideKey, ErrorDetails{Code: code, Message: message})
}

// RecordProtocolError annotates non-envelope responses (such as provider-native
// relay errors) without changing their wire format.
func RecordProtocolError(c *gin.Context, code, message string) {
	recordError(c, code, message, nil)
}

func recordResourceID(c *gin.Context, data any) {
	value := reflect.ValueOf(data)
	if value.Kind() == reflect.Pointer && !value.IsNil() {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return
	}
	id := value.FieldByName("ID")
	if id.IsValid() && id.CanInt() && id.Int() > 0 {
		c.Set(resourceIDKey, id.Int())
	}
}

func ResponseResourceID(c *gin.Context) int64 {
	return c.GetInt64(resourceIDKey)
}
