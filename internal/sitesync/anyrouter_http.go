package sitesync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/suprelory/octopus-plus/internal/helper"
	"github.com/suprelory/octopus-plus/internal/model"
)

const anyRouterUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36"

var (
	anyRouterHTMLCodePattern  = regexp.MustCompile(`(?is)<span[^>]*>\s*Error\s*</span>\s*<span[^>]*>\s*(\d{3,4})\s*</span>`)
	anyRouterLooseCodePattern = regexp.MustCompile(`\bError\s*(\d{3,4})\b`)
)

func anyRouterRequestJSONWithCookies(ctx context.Context, siteRecord *model.Site, method string, requestURL string, body any, headers map[string]string, accounts ...*model.SiteAccount) (map[string]any, string, error) {
	httpClient, err := siteHTTPClient(ctx, siteRecord, accounts...)
	if err != nil {
		return nil, "", err
	}

	var payloadBytes []byte
	if body != nil {
		payloadBytes, err = json.Marshal(body)
		if err != nil {
			return nil, "", err
		}
	}

	mergedHeaders := map[string]string{
		"User-Agent": anyRouterUserAgent,
	}
	if len(payloadBytes) > 0 {
		mergedHeaders["Content-Type"] = "application/json"
	}
	for _, item := range siteRecord.CustomHeader {
		if strings.TrimSpace(item.HeaderKey) != "" {
			mergedHeaders[strings.TrimSpace(item.HeaderKey)] = item.HeaderValue
		}
	}
	for key, value := range headers {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			mergedHeaders[key] = value
		}
	}

	cookieHeader := firstNonEmptyString(mergedHeaders["Cookie"], mergedHeaders["cookie"])
	delete(mergedHeaders, "cookie")
	if cookieHeader != "" {
		mergedHeaders["Cookie"] = cookieHeader
	}

	var lastResponseErr error
	for attempt := 0; attempt < 3; attempt++ {
		var bodyReader io.Reader
		if len(payloadBytes) > 0 {
			bodyReader = bytes.NewReader(payloadBytes)
		}

		// Bound each shield-challenge attempt separately, so three unresponsive
		// tries cannot together outlast the batch deadline.
		bodyBytes, respHeader, statusCode, err := func() ([]byte, http.Header, int, error) {
			attemptCtx, cancel := context.WithTimeout(ctx, siteRequestTimeout)
			defer cancel()

			req, err := http.NewRequestWithContext(attemptCtx, method, requestURL, bodyReader)
			if err != nil {
				return nil, nil, 0, err
			}
			for key, value := range mergedHeaders {
				req.Header.Set(key, value)
			}

			resp, err := httpClient.Do(req)
			if err != nil {
				return nil, nil, 0, err
			}
			defer resp.Body.Close()

			bodyBytes, readErr := readSiteResponseBody(resp.Body)
			if readErr != nil {
				return nil, nil, 0, readErr
			}
			return bodyBytes, resp.Header, resp.StatusCode, nil
		}()
		if err != nil {
			return nil, cookieHeader, err
		}

		cookieHeader = anyRouterMergeSetCookiePairs(cookieHeader, respHeader.Values("Set-Cookie"))
		if cookieHeader != "" {
			mergedHeaders["Cookie"] = cookieHeader
		}

		payload, responseErr := decodeSiteResponse(statusCode, respHeader, bodyBytes)
		if responseErr == nil {
			return payload, cookieHeader, nil
		}
		lastResponseErr = responseErr

		text := strings.TrimSpace(string(bodyBytes))
		if statusCode >= 200 && statusCode < 300 {
			if anyRouterIsShieldChallenge(respHeader.Get("Content-Type"), text) && cookieHeader != "" {
				acwScV2 := anyRouterSolveAcwScV2(text)
				if acwScV2 != "" {
					cookieHeader = anyRouterUpsertCookie(cookieHeader, "acw_sc__v2", acwScV2)
					mergedHeaders["Cookie"] = cookieHeader
					continue
				}
			}
		}

		return nil, cookieHeader, responseErr
	}

	return nil, cookieHeader, lastResponseErr
}

func anyRouterFormatHTTPError(statusCode int, header http.Header, body string) error {
	return formatSiteHTTPError(statusCode, header, []byte(body))
}

func anyRouterExtractResponseMessage(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	return firstNonEmptyString(
		jsonString(payload["message"]),
		jsonString(nestedValue(payload, "error", "message")),
		jsonString(payload["msg"]),
	)
}

func anyRouterResolveGroupFetchErrorMessage(payload map[string]any) string {
	message := strings.TrimSpace(anyRouterExtractResponseMessage(payload))
	lowered := strings.ToLower(message)
	if strings.Contains(lowered, "expired") ||
		strings.Contains(lowered, "invalid token") ||
		strings.Contains(lowered, "access token") ||
		strings.Contains(lowered, "unauthorized") ||
		strings.Contains(lowered, "forbidden") ||
		strings.Contains(lowered, "未登录") ||
		strings.Contains(lowered, "登录") ||
		strings.Contains(lowered, "过期") {
		return "账号会话可能已过期，请重新登录后再拉取分组"
	}
	return message
}

func anyRouterHasUsableSessionCookie(cookieHeader string) bool {
	if cookieHeader == "" {
		return false
	}
	ignored := map[string]struct{}{
		"acw_tc":     {},
		"acw_sc__v2": {},
		"cdn_sec_tc": {},
	}
	for _, part := range strings.Split(cookieHeader, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		segments := strings.SplitN(part, "=", 2)
		if len(segments) != 2 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(segments[0]))
		if _, ok := ignored[name]; ok {
			continue
		}
		if name == "session" || name == "token" || name == "auth_token" || name == "access_token" || name == "jwt" || name == "jwt_token" || strings.Contains(name, "session") || strings.Contains(name, "token") || strings.Contains(name, "auth") {
			return true
		}
	}
	return false
}

func anyRouterMergeSetCookiePairs(cookieHeader string, setCookieHeaders []string) string {
	merged := strings.TrimSpace(cookieHeader)
	for _, raw := range setCookieHeaders {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		firstPair := strings.TrimSpace(strings.SplitN(raw, ";", 2)[0])
		parts := strings.SplitN(firstPair, "=", 2)
		if len(parts) != 2 {
			continue
		}
		merged = anyRouterUpsertCookie(merged, strings.TrimSpace(parts[0]), parts[1])
	}
	return merged
}

func anyRouterUpsertCookie(cookieHeader string, name string, value string) string {
	parts := strings.Split(cookieHeader, ";")
	next := make([]string, 0, len(parts)+1)
	replaced := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		segments := strings.SplitN(part, "=", 2)
		if len(segments) != 2 {
			next = append(next, part)
			continue
		}
		if strings.TrimSpace(segments[0]) == name {
			next = append(next, name+"="+value)
			replaced = true
			continue
		}
		next = append(next, part)
	}
	if !replaced {
		next = append(next, name+"="+value)
	}
	return strings.Join(next, "; ")
}

func anyRouterIsShieldChallenge(contentType string, text string) bool {
	lowered := strings.ToLower(strings.TrimSpace(contentType))
	if strings.Contains(lowered, "text/html") && (strings.Contains(text, "var arg1=") || strings.Contains(text, "acw_sc__v2") || strings.Contains(text, "cdn_sec_tc") || strings.Contains(strings.ToLower(text), "<script")) {
		return true
	}
	return strings.Contains(text, "var arg1=")
}

func anyRouterExtractHTMLErrorSummary(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	lowered := strings.ToLower(trimmed)
	if !helper.IsHTMLResponse("", trimmed) {
		return ""
	}

	title := helper.ExtractHTMLTitle(trimmed)
	if title == "" && strings.Contains(lowered, "cloudflare tunnel error") {
		title = "Cloudflare Tunnel error"
	}
	if title == "" {
		return ""
	}

	code := ""
	if match := anyRouterHTMLCodePattern.FindStringSubmatch(trimmed); len(match) >= 2 {
		code = strings.TrimSpace(match[1])
	} else if match := anyRouterLooseCodePattern.FindStringSubmatch(trimmed); len(match) >= 2 {
		code = strings.TrimSpace(match[1])
	}
	if code != "" {
		return fmt.Sprintf("%s (Error %s)", title, code)
	}
	return title
}
