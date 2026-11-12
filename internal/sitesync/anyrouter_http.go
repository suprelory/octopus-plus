package sitesync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

	requestURI, err := url.Parse(requestURL)
	if err != nil {
		return nil, "", err
	}
	var account *model.SiteAccount
	if len(accounts) > 0 {
		account = accounts[0]
	}
	session := anyRouterSessionFromContext(ctx, siteRecord, account)
	if session == nil {
		session = newAnyRouterSession(siteRecord, account)
	}
	requestClient := *httpClient
	transport := requestClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	requestClient.Transport = anyRouterSessionTransport{base: transport, session: session}
	// The original client is shared by unrelated accounts and relay requests.
	requestClient.Jar = nil
	mergedHeaders := make(http.Header)
	mergedHeaders.Set("User-Agent", anyRouterUserAgent)
	mergedHeaders.Set("Accept", "application/json, text/plain, */*")
	mergedHeaders.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	if usesRouterSession(siteRecord.Platform) {
		mergedHeaders.Set("Origin", session.origin)
		mergedHeaders.Set("Referer", strings.TrimRight(siteRecord.BaseURL, "/")+"/console")
		if siteRecord.Platform == model.SitePlatformAgentRouter {
			mergedHeaders.Set("Referer", strings.TrimRight(siteRecord.BaseURL, "/")+"/login")
		}
	}
	if len(payloadBytes) > 0 {
		mergedHeaders.Set("Content-Type", "application/json")
	}
	for _, item := range siteRecord.CustomHeader {
		if strings.TrimSpace(item.HeaderKey) != "" {
			mergedHeaders.Set(strings.TrimSpace(item.HeaderKey), item.HeaderValue)
		}
	}
	for key, value := range headers {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			mergedHeaders.Set(key, value)
		}
	}

	if cookie := session.authCookie(); cookie != "" && mergedHeaders.Get("Cookie") == "" {
		mergedHeaders.Set("Cookie", cookie)
		mergedHeaders.Del("Authorization")
	}
	baseCookie := mergedHeaders.Get("Cookie")
	cookieHeader := session.cookieHeader(requestURI, baseCookie)

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
			req.Header = mergedHeaders.Clone()

			resp, err := requestClient.Do(req)
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

		cookieHeader = session.cookieHeader(requestURI, baseCookie)

		payload, responseErr := decodeSiteResponse(statusCode, respHeader, bodyBytes)
		if responseErr == nil {
			return payload, cookieHeader, nil
		}
		responseErr = session.redactError(responseErr)
		lastResponseErr = responseErr

		text := strings.TrimSpace(string(bodyBytes))
		if statusCode >= 200 && statusCode < 300 {
			if anyRouterIsShieldChallenge(respHeader.Get("Content-Type"), text) && cookieHeader != "" {
				acwScV2 := anyRouterSolveAcwScV2(text)
				if acwScV2 != "" {
					session.receiveCookies(requestURI, []*http.Cookie{{Name: "acw_sc__v2", Value: acwScV2, Path: "/"}})
					cookieHeader = session.cookieHeader(requestURI, baseCookie)
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
