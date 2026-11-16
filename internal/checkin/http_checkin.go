package checkin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/suprelory/octopus-plus/internal/checkin/rules"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"golang.org/x/net/http/httpguts"
)

var ErrCheckinRequest = errors.New("checkin request could not be completed")

type preparedCheckinRequest struct {
	client  *http.Client
	request *http.Request
	cleanup func()
}

func (p *preparedCheckinRequest) send() (*rules.Response, error) {
	defer p.cleanup()
	response, err := p.client.Do(p.request)
	if err != nil {
		return nil, ErrCheckinRequest
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, rules.ResponseLimit+1))
	if err != nil || len(body) > rules.ResponseLimit {
		return nil, ErrCheckinRequest
	}
	return &rules.Response{StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: body}, nil
}

func prepareCheckinRequest(ctx context.Context, proxies ProxyResolver, execution *op.CheckinExecution, query bool) (*preparedCheckinRequest, error) {
	site, account, credential := execution.Site, execution.Account, execution.Credential
	if !site.CheckinHTTPEnabled {
		return nil, op.ErrCheckinAdapter
	}
	if err := model.ValidateCheckinIdentity(site, account, credential); err != nil {
		return nil, err
	}
	method, path, body, headers := site.CheckinHTTPMethod, site.CheckinHTTPPath, site.CheckinHTTPBody, site.CheckinHTTPHeaders
	if query {
		if site.CheckinStatus == nil {
			return nil, op.ErrCheckinAdapter
		}
		config := site.CheckinStatus
		method, path, body, headers = config.Method, config.Path, config.Body, config.Headers
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodGet && method != http.MethodPost {
		return nil, ErrCheckinRequest
	}
	path = strings.ReplaceAll(path, "{{checkin_date}}", execution.Workflow().Date)
	endpoint, err := checkinEndpoint(site.BaseURL, path)
	if err != nil {
		return nil, err
	}
	body = expandCheckinTemplate(body, credential, execution.Workflow().Date, true)
	if len(body) > 64<<10 || (body != "" && (method == http.MethodGet || !json.Valid([]byte(body)))) {
		return nil, ErrCheckinRequest
	}
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, ErrCheckinRequest
	}
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("User-Agent", authUserAgent)
	origin := request.URL.Scheme + "://" + request.URL.Host
	request.Header.Set("Origin", origin)
	request.Header.Set("Referer", origin+"/")
	for _, configured := range [][]model.CustomHeader{site.CustomHeader, headers} {
		for _, header := range configured {
			value := expandCheckinTemplate(header.HeaderValue, credential, execution.Workflow().Date, false)
			if len(value) > 8192 || !httpguts.ValidHeaderFieldName(header.HeaderKey) || !httpguts.ValidHeaderFieldValue(value) || strings.ContainsRune(value, 0) {
				return nil, ErrCheckinRequest
			}
			request.Header.Set(header.HeaderKey, value)
		}
	}
	// The selected independent credential is authoritative; dormant headers must
	// not introduce a second identity alongside it.
	request.Header.Del("Authorization")
	request.Header.Del("Cookie")
	switch account.AuthMode {
	case model.CheckinAuthCookie:
		if model.ValidateSiteCheckinCookie(credential.Cookie) != nil {
			return nil, ErrCheckinRequest
		}
		request.Header.Set("Cookie", credential.Cookie)
	case model.CheckinAuthBearer:
		token := model.NormalizeSiteAccessToken(credential.AccessToken)
		if token == "" || len(token) > 8192 || !httpguts.ValidHeaderFieldValue(token) || strings.ContainsRune(token, 0) {
			return nil, ErrCheckinRequest
		}
		request.Header.Set("Authorization", "Bearer "+token)
	default:
		return nil, op.ErrCheckinAdapter
	}
	network := account.CheckinNetwork
	if network.ProxyMode == "" || network.ProxyMode == model.ProxyUsageModeInherit {
		network = site.CheckinNetwork
	}
	client, err := NewHTTPAuthenticator(proxies).httpClient(ctx, network)
	if err != nil {
		return nil, ErrCheckinRequest
	}
	client.Jar = nil // Custom HTTP credentials remain the explicit account cookie.
	cleanup := func() {}
	if !query {
		// GET can also submit a check-in. A fresh connection prevents net/http's
		// automatic retry of a GET after a reused connection breaks.
		transport, ok := client.Transport.(*http.Transport)
		if !ok {
			return nil, ErrCheckinRequest
		}
		isolated := transport.Clone()
		isolated.DisableKeepAlives = true
		client.Transport = isolated
		cleanup = isolated.CloseIdleConnections
	}
	return &preparedCheckinRequest{client: client, request: request, cleanup: cleanup}, nil
}

func checkinEndpoint(baseURL, path string) (string, error) {
	if model.ValidateSiteCheckinHTTPPath(path) != nil {
		return "", ErrCheckinRequest
	}
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil {
		return "", ErrCheckinRequest
	}
	suffix, err := url.ParseRequestURI(path)
	if err != nil {
		return "", ErrCheckinRequest
	}
	base.Path, base.RawPath = strings.TrimRight(base.Path, "/")+suffix.Path, ""
	base.RawQuery, base.Fragment = suffix.RawQuery, ""
	return base.String(), nil
}

func expandCheckinTemplate(value string, c model.CheckinCredential, date string, jsonBody bool) string {
	userID := ""
	if c.PlatformUserID != nil {
		userID = strconv.Itoa(*c.PlatformUserID)
	}
	cookie, token := "", ""
	if c.Type == model.CheckinCredentialCookie {
		cookie = c.Cookie
	}
	if c.Type == model.CheckinCredentialAccessToken {
		token = model.NormalizeSiteAccessToken(c.AccessToken)
	}
	pairs := []string{"{{checkin_date}}", date, "{{username}}", c.Username, "{{platform_user_id}}", userID,
		"{{cookie}}", cookie, "{{access_token}}", token, "{{password}}", "", "{{refresh_token}}", "", "{{api_key}}", ""}
	if jsonBody {
		for i := 1; i < len(pairs); i += 2 {
			encoded, _ := json.Marshal(pairs[i])
			pairs[i] = string(encoded[1 : len(encoded)-1])
		}
	}
	return strings.NewReplacer(pairs...).Replace(value)
}

func recoverableCheckinQuery(response *rules.Response, c model.CheckinCredential) bool {
	if response == nil || response.StatusCode != http.StatusUnauthorized || c.Platform != model.SitePlatformSub2API || c.Type != model.CheckinCredentialAccessToken {
		return false
	}
	var body map[string]any
	_ = json.Unmarshal(response.Body, &body)
	switch body["reason"] {
	case nil, "", "UNAUTHORIZED", "TOKEN_EXPIRED", "ACCESS_TOKEN_EXPIRED", "INVALID_TOKEN":
		return true
	}
	return false
}
