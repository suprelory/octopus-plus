package checkin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestHTTPAuthRefreshUsesCredentialNetworkAndHeaders(t *testing.T) {
	store, conn := sessionStore(t)
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Host != "auth.invalid" || r.URL.Path != "/service/api/v1/auth/refresh" || r.Method != http.MethodPost {
			t.Error("authentication did not use its own address through the selected proxy")
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "cf_clearance=synthetic" || r.Header.Get("User-Agent") != "session-agent" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("authentication sent a stale bearer or lost its configured network headers")
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["refresh_token"] != "refresh-old" {
			t.Error("refresh body changed or included unrelated credentials")
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"Bearer issued","refresh_token":"rotated","expires_in":3600}}`))
	}))
	defer proxy.Close()
	if err := conn.AutoMigrate(&model.ProxyConfiguration{}); err != nil {
		t.Fatal(err)
	}
	pool := model.ProxyConfiguration{Name: "auth proxy", URL: proxy.URL, Enabled: true}
	if err := conn.Create(&pool).Error; err != nil {
		t.Fatal(err)
	}
	c, err := store.CreateCredential(context.Background(), model.CheckinCredential{Name: "network", Platform: model.SitePlatformSub2API,
		Type: model.CheckinCredentialAccessToken, AuthBaseURL: "http://auth.invalid/service", AccessToken: "private-access", RefreshToken: "refresh-old",
		TokenExpiresAt: 1, CheckinNetwork: model.CheckinNetwork{ProxyMode: model.ProxyUsageModePool, ProxyConfigID: &pool.ID},
		AuthHeaders: []model.CustomHeader{{HeaderKey: "User-Agent", HeaderValue: "session-agent"}, {HeaderKey: "Cookie", HeaderValue: "cf_clearance=synthetic"}, {HeaderKey: "Authorization", HeaderValue: "Bearer private-stale"}}})
	if err != nil {
		t.Fatal(err)
	}
	sessions := testSessions(store, NewHTTPAuthenticator(store))
	session, err := sessions.Ensure(context.Background(), c.ID)
	if err != nil || session.Credential.AccessToken != "issued" || session.Credential.RefreshToken != "rotated" || calls.Load() != 1 {
		t.Fatalf("HTTP refresh failed: %v", err)
	}
	if err := conn.Model(&pool).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.RecoverRead(context.Background(), session); err == nil || calls.Load() != 1 {
		t.Fatalf("disabled proxy reused cached transport or fell back to direct: %v", err)
	}
}

func TestHTTPAuthLoginProtocolsAndCookieIsolation(t *testing.T) {
	for _, platform := range []model.SitePlatform{model.SitePlatformNewAPI, model.SitePlatformOneAPI, model.SitePlatformOneHub, model.SitePlatformDoneHub, model.SitePlatformAnyRouter} {
		t.Run(string(platform), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/user/login" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("login path/method or session isolation incorrect")
				}
				if platform == model.SitePlatformAnyRouter && r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
					t.Error("AnyRouter login lost its request header")
				}
				var body map[string]string
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["password"] != "synthetic-password" {
					t.Error("login credentials incorrect")
				}
				http.SetCookie(w, &http.Cookie{Name: "cf_clearance", Value: "clearance", Path: "/"})
				if platform == model.SitePlatformAnyRouter {
					http.SetCookie(w, &http.Cookie{Name: "auth_token", Value: body["username"], Path: "/", HttpOnly: true})
					_, _ = w.Write([]byte(`{"success":true,"data":{"id":42}}`))
				} else {
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"id": 42, "access_token": body["username"]}})
				}
			}))
			defer server.Close()
			auth := NewHTTPAuthenticator(nil)
			var wg sync.WaitGroup
			for i := range 8 {
				wg.Go(func() {
					username := fmt.Sprintf("user-%d", i)
					c := model.CheckinCredential{Name: "password", Type: model.CheckinCredentialPassword, Platform: platform,
						AuthBaseURL: server.URL, Username: username, Password: "synthetic-password"}
					result, err := auth.Login(context.Background(), c)
					if err != nil || result.PlatformUserID == nil || *result.PlatformUserID != 42 {
						t.Errorf("platform login failed: %v", err)
						return
					}
					if platform == model.SitePlatformAnyRouter {
						if !strings.Contains(result.Cookie, "auth_token="+username) || result.AccessToken != "" {
							t.Error("login did not retain its own cookie session")
						}
					} else if result.AccessToken != username || result.Cookie != "" {
						t.Error("login session was confused with a clearance cookie")
					}
				})
			}
			wg.Wait()
		})
	}
}

func TestHTTPAuthNeverReplaysRedirectOrAgentRouterLogin(t *testing.T) {
	var targetCalls, sourceCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls.Add(1)
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	auth := NewHTTPAuthenticator(nil)
	c := model.CheckinCredential{Name: "redirect", Platform: model.SitePlatformSub2API, Type: model.CheckinCredentialAccessToken,
		AuthBaseURL: source.URL, AccessToken: "private-access", RefreshToken: "private-refresh"}
	if _, err := auth.Refresh(context.Background(), c); err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatal("redirect was accepted or leaked a credential")
	}
	c.Type, c.Platform, c.Username, c.Password = model.CheckinCredentialPassword, model.SitePlatformAgentRouter, "user", "private-password"
	if _, err := auth.Login(context.Background(), c); !errors.Is(err, ErrLoginRequiresCheckin) {
		t.Fatalf("AgentRouter authentication performed checkin: %v", err)
	}
	if targetCalls.Load() != 0 || sourceCalls.Load() != 1 {
		t.Fatal("authentication replayed a mutation or crossed a redirect")
	}
}

func TestHTTPAuthRejectsMalformedAndAmbiguousRefreshResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		failure    ExchangeFailure
	}{
		{"malformed", `private-response`, 200, ExchangeUncertain},
		{"missing_pair", `{"code":0,"data":{"access_token":"private-access","expires_in":3600}}`, 200, ExchangeUncertain},
		{"negative_expiry", `{"code":0,"data":{"access_token":"a","refresh_token":"r","expires_in":-1}}`, 200, ExchangeUncertain},
		{"trailing_document", `{"code":0} {}`, 200, ExchangeUncertain},
		{"unavailable", `{"message":"private-secret"}`, 503, ExchangeUncertain},
		{"rejected", `{"reason":"SESSION_BINDING_MISMATCH","message":"private-secret"}`, 401, ExchangeRejected},
		{"rate_limited", `{"message":"private-secret"}`, 429, ExchangeRetryable},
		{"oversized", strings.Repeat("x", authResponseLimit+1), 200, ExchangeUncertain},
		{"oversized_token", `{"code":0,"data":{"access_token":"` + strings.Repeat("x", 8193) + `","refresh_token":"r","expires_in":3600}}`, 200, ExchangeUncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c := model.CheckinCredential{Name: "bad response", Platform: model.SitePlatformSub2API, Type: model.CheckinCredentialAccessToken,
				AuthBaseURL: server.URL, AccessToken: "a", RefreshToken: "r"}
			_, err := NewHTTPAuthenticator(nil).Refresh(context.Background(), c)
			var failure *ExchangeError
			if !errors.As(err, &failure) || failure.Failure != tc.failure || strings.Contains(err.Error(), "private-") {
				t.Fatalf("unsafe authentication failure classification: %v", err)
			}
		})
	}
}

func TestHTTPAuthCookiesRequireAUsableServiceSession(t *testing.T) {
	for _, cookie := range []http.Cookie{
		{Name: "cf_clearance", Value: "private-clearance", Path: "/"},
		{Name: "session", Value: "private-session", Path: "/elsewhere"},
		{Name: "session", Value: "private-session", Path: "/", Domain: "unrelated.invalid"},
		{Name: "session", Value: "private-session", Path: "/", MaxAge: -1},
	} {
		t.Run(cookie.String(), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.SetCookie(w, &cookie)
				_, _ = w.Write([]byte(`{"success":true,"data":{"id":42}}`))
			}))
			defer server.Close()
			_, err := NewHTTPAuthenticator(nil).Login(context.Background(), model.CheckinCredential{Name: "cookie", Platform: model.SitePlatformAnyRouter,
				Type: model.CheckinCredentialPassword, AuthBaseURL: server.URL, Username: "user", Password: "password"})
			if err == nil {
				t.Fatal("unusable or unrelated cookie accepted as a login session")
			}
		})
	}
}

func TestSessionJWTExpiryHintsAndJSONSecrecy(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	payload := fmt.Sprintf(`{"exp":%d,"iat":%d}`, now.Add(time.Minute).Unix(), now.Add(-time.Minute).Unix())
	c := model.CheckinCredential{AccessToken: "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature",
		TokenExpiresAt: now.Add(time.Hour).UnixMilli(), RefreshToken: "private-refresh", Password: "private-password", Cookie: "private-cookie"}
	expires, issued := tokenTimes(c)
	if expires != now.Add(time.Minute).UnixMilli() || issued != now.Add(-time.Minute).UnixMilli() {
		t.Fatal("JWT expiry did not bound the stored hint")
	}
	encoded, err := json.Marshal(Session{Credential: c})
	if err != nil || string(encoded) != "{}" {
		t.Fatal("internal session serialized credentials")
	}
}
