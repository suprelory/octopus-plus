package model

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func normalizeCheckinHeaderKeys(headers []CustomHeader) {
	for i := range headers {
		headers[i].HeaderKey = strings.TrimSpace(headers[i].HeaderKey)
	}
}

func validateSiteCheckinHTTPRequest(method, path, body string, headers []CustomHeader) error {
	if method != http.MethodGet && method != http.MethodPost {
		return fmt.Errorf("site checkin HTTP method must be GET or POST")
	}
	if err := ValidateSiteCheckinHTTPPath(path); err != nil {
		return err
	}
	if len(headers) > 32 {
		return fmt.Errorf("site checkin HTTP headers must not exceed 32")
	}
	seenHeaders := make(map[string]struct{}, len(headers))
	for _, header := range headers {
		key := header.HeaderKey
		value := header.HeaderValue
		canonical := http.CanonicalHeaderKey(key)
		if key == "" || canonical == "" || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("site checkin HTTP header is invalid")
		}
		if !siteCheckinHeaderAllowed(key) {
			return fmt.Errorf("site checkin HTTP header %q cannot be overridden", key)
		}
		lowerKey := strings.ToLower(key)
		if _, exists := seenHeaders[lowerKey]; exists {
			return fmt.Errorf("site checkin HTTP header %q is duplicated", key)
		}
		seenHeaders[lowerKey] = struct{}{}
		if len(key) > 256 || len(value) > 4096 {
			return fmt.Errorf("site checkin HTTP header exceeds its size limit")
		}
	}
	if len(body) > 64<<10 {
		return fmt.Errorf("site checkin HTTP body must not exceed 64 KiB")
	}
	if method == http.MethodGet && body != "" {
		return fmt.Errorf("site checkin HTTP body is only supported with POST")
	}
	if body != "" && !json.Valid([]byte(body)) {
		return fmt.Errorf("site checkin HTTP body must be valid JSON")
	}
	return nil
}

func ValidateSiteCheckinHTTPPath(value string) error {
	if value == "" || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n\x00#") {
		return fmt.Errorf("site checkin HTTP path must be a relative URL path")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || !strings.HasPrefix(value, "/") || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" {
		return fmt.Errorf("site checkin HTTP path must be a relative URL path")
	}
	if strings.Contains(parsed.Path, "\\") {
		return fmt.Errorf("site checkin HTTP path must not contain backslashes")
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("site checkin HTTP path must not contain dot segments")
		}
	}
	return nil
}

func siteCheckinHeaderAllowed(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "host", "content-length", "connection", "transfer-encoding", "upgrade", "proxy-connection", "te", "trailer", "expect":
		return false
	default:
		return true
	}
}
