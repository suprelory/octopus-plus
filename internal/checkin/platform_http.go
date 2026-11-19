package checkin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin/router"
	"github.com/suprelory/octopus-plus/internal/checkin/rules"
	"github.com/suprelory/octopus-plus/internal/helper"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

var errVerificationRequired = errors.New("complete verification at the check-in site")

type platformHTTP struct {
	proxies  ProxyResolver
	base     string
	network  model.CheckinNetwork
	headers  []model.CustomHeader
	original model.CheckinCredential
	session  op.CheckinLoginSession
	cookies  *router.Cookies
}

func (r *checkinRun) platformClient(login bool) *platformHTTP {
	e := r.execution
	c := e.Credential
	base, network, headers := e.Site.BaseURL, e.Account.CheckinNetwork, e.Site.CustomHeader
	if network.ProxyMode == "" || network.ProxyMode == model.ProxyUsageModeInherit {
		network = e.Site.CheckinNetwork
	}
	if login {
		base, network, headers = c.AuthBaseURL, c.CheckinNetwork, c.AuthHeaders
	}
	seed := c.Cookie
	// Some existing platform credentials explicitly contain a Cookie header.
	if seed == "" && router.HasSession(c.AccessToken) {
		seed = c.AccessToken
	}
	return &platformHTTP{proxies: r.engine.proxies, base: base, network: network, headers: headers, original: c,
		session: op.CheckinLoginSession{AccessToken: c.AccessToken, PlatformUserID: c.PlatformUserID, TokenExpiresAt: c.TokenExpiresAt},
		cookies: router.NewCookies(base, seed, c.SessionCookies)}
}

func (p *platformHTTP) snapshot() op.CheckinLoginSession {
	s := p.session
	s.SessionCookies = p.cookies.Snapshot()
	if cookie := p.cookies.Header(p.original.AuthBaseURL + "/api/user/self"); router.HasSession(cookie) {
		s.Cookie = cookie
	}
	if p.original.Type == model.CheckinCredentialCookie {
		// The entered credential is editable. The scoped jar takes precedence
		// for platform requests, including when the server revoked that cookie.
		s.Cookie = p.original.Cookie
	}
	return s
}

var platformUserHeaders = []string{"New-API-User", "Veloera-User", "Voapi-User", "User-Id", "Rix-Api-User", "Neo-Api-User"}

func setPlatformUser(request *http.Request, id *int) {
	for _, key := range platformUserHeaders {
		if id == nil {
			request.Header.Del(key)
		} else {
			request.Header.Set(key, strconv.Itoa(*id))
		}
	}
}

func (p *platformHTTP) prepare(ctx context.Context, method, path string, body any, login bool) (*preparedCheckinRequest, error) {
	endpoint, err := checkinEndpoint(p.base, path)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil || len(encoded) > 64<<10 {
			return nil, ErrCheckinRequest
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, ErrCheckinRequest
	}
	request.GetBody = nil
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", authUserAgent)
	request.Header.Set("Origin", request.URL.Scheme+"://"+request.URL.Host)
	request.Header.Set("Referer", p.base+"/console")
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	if login {
		request.Header.Set("Referer", p.base+"/login")
	}
	for _, header := range p.headers {
		request.Header.Set(header.HeaderKey, header.HeaderValue)
	}
	request.Header.Del("Authorization")
	setPlatformUser(request, nil)
	if !login {
		request.Header.Del("Cookie")
		cookie := p.cookies.Header(endpoint)
		if p.original.Type == model.CheckinCredentialAPIKey {
			cookie = ""
		}
		if cookie != "" {
			request.Header.Set("Cookie", cookie)
		}
		if !router.HasSession(cookie) {
			token := model.NormalizeSiteAccessToken(p.session.AccessToken)
			if p.original.Type == model.CheckinCredentialAPIKey {
				token = model.NormalizeSiteAccessToken(p.original.APIKey)
			}
			if token == "" || router.HasSession(token) {
				return nil, ErrSessionUnavailable
			}
			request.Header.Set("Authorization", "Bearer "+token)
		}
		setPlatformUser(request, p.session.PlatformUserID)
	}
	client, err := NewHTTPAuthenticator(p.proxies).httpClient(ctx, p.network)
	if err != nil {
		return nil, ErrCheckinRequest
	}
	client.Jar = nil
	return prepareCheckinTransport(client, request, method != http.MethodGet)
}

// Decode objects once and use exact numeric types for status codes and log timestamps.
func platformObject(data []byte) map[string]any {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil
	}
	return payload
}

func platformMessage(payload map[string]any) string {
	for _, key := range []string{"message", "msg"} {
		if msg := stringValue(payload[key]); msg != "" {
			return msg
		}
	}
	object, _ := payload["error"].(map[string]any)
	return stringValue(object["message"])
}

func requiresVerification(header http.Header, body []byte, payload map[string]any) bool {
	if strings.EqualFold(header.Get("Cf-Mitigated"), "challenge") {
		return true
	}
	if payload != nil {
		msg := strings.ToLower(platformMessage(payload))
		return payload["success"] != true && (strings.Contains(msg, "turnstile") || strings.Contains(msg, "captcha") || strings.Contains(msg, "人机验证") || helper.IsCloudflareProtectionMessage(msg))
	}
	page := string(body)
	return helper.IsHTMLResponse(header.Get("Content-Type"), page) && (helper.IsCloudflareProtectionMessage(page) ||
		strings.Contains(page, "acw_sc__v2") || strings.Contains(page, "var arg1="))
}

func (p *platformHTTP) send(prepared *preparedCheckinRequest) (*rules.Response, map[string]any, error) {
	response, err := prepared.send()
	if err != nil {
		return nil, nil, err
	}
	payload := platformObject(response.Body)
	if requiresVerification(response.Header, response.Body, payload) {
		return response, nil, errVerificationRequired
	}
	cookies := (&http.Response{Header: response.Header}).Cookies()
	if response.StatusCode >= 200 && response.StatusCode < 300 && payload != nil && payload["success"] != false {
		p.cookies.SetCookies(prepared.request.URL, cookies)
	} else {
		// A rejected request can explicitly revoke a cookie. Keep the revocation
		// even when there is no successful response to establish a new session.
		for _, cookie := range cookies {
			if cookie.MaxAge < 0 || (!cookie.Expires.IsZero() && !cookie.Expires.After(time.Now())) {
				p.cookies.SetCookies(prepared.request.URL, []*http.Cookie{cookie})
			}
		}
	}
	return response, payload, nil
}

func platformUserID(payload map[string]any) *int {
	data, _ := payload["data"].(map[string]any)
	id, ok := integer(data["id"])
	if !ok || id <= 0 || id > math.MaxInt {
		return nil
	}
	value := int(id)
	return &value
}

func platformLoginToken(payload map[string]any) string {
	if token := stringValue(payload["data"]); token != "" {
		return model.NormalizeSiteAccessToken(token)
	}
	data, _ := payload["data"].(map[string]any)
	for _, object := range []map[string]any{data, payload} {
		for _, key := range []string{"token", "access_token", "accessToken"} {
			if token := model.NormalizeSiteAccessToken(stringValue(object[key])); token != "" {
				return token
			}
		}
	}
	return ""
}
