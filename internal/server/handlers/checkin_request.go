package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/server/resp"
)

// Strict decoding is local to the new API; changing Gin's global decoder would
// silently change the legacy API during the migration period.
func bindCheckinJSON(c *gin.Context, target any, optional bool, limit int64) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	body, err := io.ReadAll(c.Request.Body)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		resp.ErrorWithCode(c, http.StatusRequestEntityTooLarge, "checkin.request_too_large", "check-in request exceeds its size limit")
		return false
	}
	if err != nil {
		resp.InvalidJSON(c)
		return false
	}
	body = bytes.TrimSpace(body)
	hasBody := len(body) != 0
	if len(body) == 0 && optional {
		body = []byte("{}")
	}
	if len(body) == 0 || body[0] != '{' {
		resp.InvalidJSON(c)
		return false
	}
	if hasBody && !isCheckinJSON(c.GetHeader("Content-Type")) {
		resp.ErrorWithCode(c, http.StatusUnsupportedMediaType, "checkin.content_type", "Content-Type must be application/json")
		return false
	}
	// Duplicate keys are ambiguous for mutations, including nested credentials.
	checker := json.NewDecoder(bytes.NewReader(body))
	checker.UseNumber()
	if checkCheckinJSONValue(checker, 0, true, false) != nil {
		resp.InvalidJSON(c)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		resp.InvalidJSON(c)
		return false
	}
	return true
}

func isCheckinJSON(value string) bool {
	// Parameter parsing is shared with the standard library's media-type rules.
	media, _, err := mime.ParseMediaType(value)
	return err == nil && media == "application/json"
}

func checkCheckinJSONValue(d *json.Decoder, depth int, foldKeys, allowNull bool) error {
	if depth > 64 {
		return errors.New("JSON nesting limit")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token == nil && foldKeys && !allowNull {
		return errors.New("scalar JSON field cannot be null")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if foldKeys {
				name = strings.ToLower(name)
			}
			if !ok || keys[name] {
				return errors.New("duplicate JSON field")
			}
			keys[name] = true
			// Raw rule samples and expected values use case-sensitive JSON keys;
			// Go request fields are case-insensitive and must reject alias duplicates.
			childFold := foldKeys && name != "response" && name != "status_response" && name != "checked_value" && name != "unchecked_value"
			if err := checkCheckinJSONValue(d, depth+1, childFold, checkinNullableField(name)); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := checkCheckinJSONValue(d, depth+1, foldKeys, false); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = d.Token()
	return err
}

// Only pointer/slice configuration fields accept null. Raw rule JSON bypasses
// this list so samples and comparison values retain their original semantics.
func checkinNullableField(name string) bool {
	switch name {
	case "proxy_config_id", "platform_user_id", "external_checkin_url", "credential", "credential_id", "balance_config", "checkin_status", "config", "custom_header", "checkin_http_headers", "auth_headers", "headers", "tags", "site_ids":
		return true
	default:
		return false
	}
}

func checkinQuery(c *gin.Context, allowed ...string) (url.Values, bool) {
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		resp.InvalidParam(c)
		return nil, false
	}
	keys := map[string]bool{}
	for _, key := range allowed {
		keys[key] = true
	}
	for key, value := range values {
		if !keys[key] || len(value) != 1 || value[0] == "" {
			resp.InvalidParam(c)
			return nil, false
		}
	}
	return values, true
}

func checkinPositiveID(raw string, bits int) (int64, bool) {
	if raw == "" {
		return 0, false
	}
	for _, value := range raw {
		if value < '0' || value > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(raw, 10, bits)
	return id, err == nil && id > 0
}

func checkinPathID(c *gin.Context) (int, bool) {
	id, ok := checkinPositiveID(c.Param("id"), strconv.IntSize)
	if !ok {
		resp.InvalidParam(c)
	}
	return int(id), ok
}

func checkinLongPathID(c *gin.Context) (int64, bool) {
	id, ok := checkinPositiveID(c.Param("id"), 64)
	if !ok {
		resp.InvalidParam(c)
	}
	return id, ok
}

func checkinPageParams(c *gin.Context, values url.Values) (int, int64, bool) {
	limit, before := 50, int64(0)
	if raw := values.Get("limit"); raw != "" {
		n, ok := checkinPositiveID(raw, 32)
		if !ok || n > 100 {
			resp.InvalidParam(c)
			return 0, 0, false
		}
		limit = int(n)
	}
	if raw := values.Get("before_id"); raw != "" {
		var ok bool
		before, ok = checkinPositiveID(raw, 64)
		if !ok {
			resp.InvalidParam(c)
			return 0, 0, false
		}
	}
	return limit, before, true
}

func checkinEmptyAction(c *gin.Context) bool {
	if _, ok := checkinQuery(c); !ok {
		return false
	}
	return bindCheckinJSON(c, &struct{}{}, true, 1<<20)
}
