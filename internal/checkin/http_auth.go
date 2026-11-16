package checkin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/client"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"golang.org/x/net/publicsuffix"
)

const authUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36"
const authResponseLimit = 1 << 20

type ProxyResolver interface {
	CheckinProxyURL(context.Context, int) (string, error)
}

type HTTPAuthenticator struct{ proxies ProxyResolver }

func NewHTTPAuthenticator(proxies ProxyResolver) *HTTPAuthenticator {
	return &HTTPAuthenticator{proxies: proxies}
}

func (a *HTTPAuthenticator) httpClient(ctx context.Context, network model.CheckinNetwork) (*http.Client, error) {
	if err := network.Validate(false); err != nil {
		return nil, err
	}
	var base *http.Client
	var err error
	switch network.ProxyMode {
	case model.ProxyUsageModeDirect:
		base, err = client.GetHTTPClientSystemProxy(false)
	case model.ProxyUsageModeSystem:
		base, err = client.GetHTTPClientSystemProxy(true)
	case model.ProxyUsageModePool:
		if a.proxies == nil {
			return nil, ErrSessionUnavailable
		}
		var proxyURL string
		proxyURL, err = a.proxies.CheckinProxyURL(ctx, *network.ProxyConfigID)
		if err == nil {
			base, err = client.GetHTTPClientCustomProxy(proxyURL)
		}
	}
	if err != nil {
		return nil, err
	}
	clone := *base
	clone.Timeout = 30 * time.Second
	clone.Jar, _ = cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	// Login and refresh are mutations. A redirect must not replay the body or
	// forward passwords, clearance cookies or refresh tokens to another endpoint.
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone, nil
}

func (a *HTTPAuthenticator) exchange(ctx context.Context, c model.CheckinCredential, path string, body any, login bool) (map[string]any, string, error) {
	if err := c.Validate(); err != nil {
		return nil, "", &ExchangeError{Failure: ExchangeRejected}
	}
	httpClient, err := a.httpClient(ctx, c.CheckinNetwork)
	if err != nil {
		return nil, "", &ExchangeError{Failure: ExchangeRetryable}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, "", &ExchangeError{Failure: ExchangeRejected}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.AuthBaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, "", &ExchangeError{Failure: ExchangeRejected}
	}
	// Even a user-supplied Idempotency-Key must not let net/http replay a refresh
	// body after a broken reused connection. Only the caller owns recovery.
	request.GetBody = nil
	request.Header.Set("User-Agent", authUserAgent)
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	for _, header := range c.AuthHeaders {
		if !strings.EqualFold(header.HeaderKey, "Authorization") {
			request.Header.Set(header.HeaderKey, header.HeaderValue)
		}
	}
	request.Header.Set("Content-Type", "application/json")
	if login && c.Platform == model.SitePlatformAnyRouter {
		request.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	if ctx.Err() != nil {
		return nil, "", &ExchangeError{Failure: ExchangeRetryable}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, "", &ExchangeError{Failure: ExchangeUncertain}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		return nil, "", &ExchangeError{Failure: ExchangeRetryable, AllowAccessFallback: true}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, authResponseLimit+1))
	if err != nil || len(data) > authResponseLimit {
		return nil, "", &ExchangeError{Failure: ExchangeUncertain}
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decodeErr := decoder.Decode(&payload)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := ExchangeRejected
		if response.StatusCode >= 500 {
			failure = ExchangeUncertain
		}
		fallback := false
		if decodeErr == nil {
			switch payload["reason"] {
			case "REFRESH_TOKEN_INVALID", "REFRESH_TOKEN_EXPIRED", "REFRESH_TOKEN_REUSED":
				fallback = true
			}
		}
		return nil, "", &ExchangeError{Failure: failure, AllowAccessFallback: fallback}
	}
	if decodeErr != nil || payload == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, "", &ExchangeError{Failure: ExchangeUncertain}
	}
	if strings.EqualFold(response.Header.Get("Cf-Mitigated"), "challenge") {
		return nil, "", &ExchangeError{Failure: ExchangeRejected}
	}
	// Export only session cookies valid at the service's authenticated endpoints.
	// Clearance cookies are not themselves proof of a successful login session.
	u, _ := url.Parse(c.AuthBaseURL + "/api/user/self")
	cookieRequest := &http.Request{Header: make(http.Header)}
	hasSession := false
	for _, cookie := range httpClient.Jar.Cookies(u) {
		cookieRequest.AddCookie(cookie)
		name := strings.ToLower(cookie.Name)
		if cookie.Value != "" && (strings.Contains(name, "session") || strings.Contains(name, "token") || strings.Contains(name, "auth")) {
			hasSession = true
		}
	}
	cookie := ""
	if hasSession {
		cookie = cookieRequest.Header.Get("Cookie")
	}
	return payload, cookie, nil
}

func (a *HTTPAuthenticator) Refresh(ctx context.Context, c model.CheckinCredential) (op.CheckinTokenPair, error) {
	if c.Platform != model.SitePlatformSub2API || c.Type != model.CheckinCredentialAccessToken || strings.TrimSpace(c.RefreshToken) == "" {
		return op.CheckinTokenPair{}, ErrUnsupportedSession
	}
	payload, _, err := a.exchange(ctx, c, "/api/v1/auth/refresh", map[string]string{"refresh_token": strings.TrimSpace(c.RefreshToken)}, false)
	if err != nil {
		return op.CheckinTokenPair{}, err
	}
	if code, exists := payload["code"]; exists {
		if value, ok := integer(code); !ok || value != 0 {
			return op.CheckinTokenPair{}, &ExchangeError{Failure: ExchangeRejected}
		}
	}
	data, _ := payload["data"].(map[string]any)
	pair := op.CheckinTokenPair{AccessToken: model.NormalizeSiteAccessToken(stringValue(data["access_token"])), RefreshToken: strings.TrimSpace(stringValue(data["refresh_token"]))}
	seconds, ok := integer(data["expires_in"])
	if pair.AccessToken == "" || pair.RefreshToken == "" || !ok || seconds <= 0 || seconds > math.MaxInt64/int64(time.Second) {
		return op.CheckinTokenPair{}, &ExchangeError{Failure: ExchangeUncertain}
	}
	pair.TokenExpiresAt = time.Now().Add(time.Duration(seconds) * time.Second).UnixMilli()
	c.AccessToken, c.RefreshToken, c.TokenExpiresAt = pair.AccessToken, pair.RefreshToken, pair.TokenExpiresAt
	if c.Validate() != nil {
		return op.CheckinTokenPair{}, &ExchangeError{Failure: ExchangeUncertain}
	}
	return pair, nil
}

func (a *HTTPAuthenticator) Login(ctx context.Context, c model.CheckinCredential) (op.CheckinLoginSession, error) {
	if c.Platform == model.SitePlatformAgentRouter {
		return op.CheckinLoginSession{}, ErrLoginRequiresCheckin
	}
	if !c.SupportsSessionLogin() {
		return op.CheckinLoginSession{}, ErrUnsupportedSession
	}
	payload, cookie, err := a.exchange(ctx, c, "/api/user/login", map[string]string{"username": c.Username, "password": c.Password}, true)
	if err != nil {
		return op.CheckinLoginSession{}, err
	}
	if payload["success"] != true {
		return op.CheckinLoginSession{}, &ExchangeError{Failure: ExchangeRejected}
	}
	data, _ := payload["data"].(map[string]any)
	token := stringValue(payload["data"])
	for _, object := range []map[string]any{data, payload} {
		for _, key := range []string{"token", "access_token", "accessToken"} {
			if token == "" {
				token = strings.TrimSpace(stringValue(object[key]))
			}
		}
	}
	if token == "" && cookie == "" {
		return op.CheckinLoginSession{}, &ExchangeError{Failure: ExchangeUncertain}
	}
	session := op.CheckinLoginSession{AccessToken: model.NormalizeSiteAccessToken(token), Cookie: cookie}
	c.AccessToken = session.AccessToken
	if c.Validate() != nil || (cookie != "" && model.ValidateSiteCheckinCookie(cookie) != nil) {
		return op.CheckinLoginSession{}, &ExchangeError{Failure: ExchangeUncertain}
	}
	if id, ok := integer(data["id"]); ok && id > 0 && id <= int64(math.MaxInt) {
		value := int(id)
		session.PlatformUserID = &value
	}
	return session, nil
}

func stringValue(value any) string { text, _ := value.(string); return text }
func integer(value any) (int64, bool) {
	var text string
	switch value := value.(type) {
	case json.Number:
		text = string(value)
	case string:
		text = strings.TrimSpace(value)
	default:
		return 0, false
	}
	number, err := strconv.ParseInt(text, 10, 64)
	return number, err == nil
}

func exchangeFailure(err error) *ExchangeError {
	var typed *ExchangeError
	if errors.As(err, &typed) {
		return typed
	}
	return &ExchangeError{Failure: ExchangeUncertain}
}
