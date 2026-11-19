package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/suprelory/octopus-plus/internal/checkin"
	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type checkinAPIFixture struct {
	router   *gin.Engine
	token    string
	services *checkin.Services
	conn     *gorm.DB
}

func newCheckinAPIFixture(t *testing.T) *checkinAPIFixture {
	t.Helper()
	return newCheckinAPIFixtureWithConfig(t, nil)
}

func newCheckinAPIFixtureWithConfig(t *testing.T, config func() checkin.NotificationConfig) *checkinAPIFixture {
	t.Helper()
	// The administrator session uses the application's auth database. All check-in
	// operations use a separate connection containing no subscription entities.
	token := setupLogStreamHandlerTest(t)
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "checkin-api.db")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	if err := dbpkg.PrepareCheckinSchema(conn); err != nil {
		t.Fatal(err)
	}
	services := checkin.NewServices(op.NewCheckinStore(conn), config)
	router := gin.New()
	if err := RegisterCheckinRoutes(router, services); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := services.Notifications.Wait(ctx); err != nil {
			t.Errorf("notification worker did not finish: %v", err)
		}
		for _, name := range []string{"sites", "site_accounts", "site_tokens", "site_models", "site_channel_bindings", "channels"} {
			if conn.Migrator().HasTable(name) {
				t.Errorf("independent API created legacy table %s", name)
			}
		}
		_ = pool.Close()
	})
	return &checkinAPIFixture{router, token, services, conn}
}

func (f *checkinAPIFixture) request(t *testing.T, method, path string, body any, status int, codes ...string) json.RawMessage {
	t.Helper()
	var raw string
	if value, ok := body.(string); ok {
		raw = value
	} else if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(encoded)
	}
	w := httptest.NewRecorder()
	request := httptest.NewRequest(method, "/api/v1/checkin"+path, strings.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+f.token)
	if raw != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	f.router.ServeHTTP(w, request)
	if w.Code != status {
		t.Fatalf("%s %s status=%d want=%d response=%s", method, path, w.Code, status, w.Body.String())
	}
	var envelope struct {
		Data      json.RawMessage `json:"data"`
		ErrorCode string          `json:"error_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if status >= 400 && envelope.ErrorCode == "" {
		t.Fatalf("missing stable error code: %s", w.Body.String())
	}
	if len(codes) > 0 && envelope.ErrorCode != codes[0] {
		t.Fatalf("%s %s error_code=%s want=%s", method, path, envelope.ErrorCode, codes[0])
	}
	return envelope.Data
}

func decodeCheckinAPI[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode API response: %v %s", err, raw)
	}
	return value
}

func (f *checkinAPIFixture) site(t *testing.T, base string, status bool) model.CheckinSite {
	t.Helper()
	body := map[string]any{"name": "HTTP site", "platform": "api", "base_url": base, "checkin_http_enabled": true, "checkin_http_path": "/submit"}
	if status {
		body["checkin_status"] = map[string]any{"path": "/status", "checked_path": "data.checked", "max_attempts": 1, "initial_delay_ms": 0, "poll_interval_ms": 1, "timeout_seconds": 2, "reward_source": "status"}
	}
	return decodeCheckinAPI[model.CheckinSite](t, f.request(t, "POST", "/sites", body, 200))
}

func (f *checkinAPIFixture) account(t *testing.T, site model.CheckinSite) model.CheckinAccount {
	t.Helper()
	return decodeCheckinAPI[model.CheckinAccount](t, f.request(t, "POST", "/accounts", map[string]any{
		"site_id": site.ID, "name": "HTTP account", "auth_mode": "cookie", "auto_checkin": true,
		"credential": map[string]any{"name": "cookie credential", "type": "cookie", "platform": "api", "auth_base_url": site.BaseURL, "cookie": "session=api-fixture-secret"},
	}, 200))
}

func TestCheckinAPIAuthenticationAndExplicitRegistration(t *testing.T) {
	f := newCheckinAPIFixture(t)
	if err := RegisterCheckinRoutes(gin.New(), nil); err == nil {
		t.Fatal("registered missing services")
	}
	for _, route := range f.router.Routes() {
		path := strings.ReplaceAll(route.Path, ":id", "1")
		for _, token := range []string{"", "Bearer invalid", "Bearer sk-relay-key"} {
			w := httptest.NewRecorder()
			request := httptest.NewRequest(route.Method, path, strings.NewReader("{}"))
			request.Header.Set("Authorization", token)
			request.Header.Set("Content-Type", "application/json")
			f.router.ServeHTTP(w, request)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("unprotected %s %s: %d", route.Method, path, w.Code)
			}
		}
	}
	f.request(t, "GET", "/capabilities", nil, 200)
	for _, path := range []string{"/sites", "/accounts", "/credentials"} {
		if raw := f.request(t, "GET", path, nil, 200); string(raw) != "[]" {
			t.Fatalf("empty list %s: %s", path, raw)
		}
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/site/list", nil))
	if w.Code != 404 {
		t.Fatal("new API registered legacy routes")
	}
}

func TestCheckinAPIManagementCredentialsAndPatchSemantics(t *testing.T) {
	f := newCheckinAPIFixture(t)
	site := f.site(t, "https://checkin.example", false)
	account := f.account(t, site)
	if !site.Enabled || !account.Enabled || account.SiteID != site.ID || account.CredentialID == 0 {
		t.Fatal("creation defaults or references lost")
	}
	for _, path := range []string{fmt.Sprintf("/credentials/%d", account.CredentialID), "/credentials"} {
		raw := f.request(t, "GET", path, nil, 200)
		if strings.Contains(string(raw), "api-fixture-secret") || strings.Contains(string(raw), `"cookie":`) || strings.Contains(string(raw), `"auth_headers":`) {
			t.Fatalf("credential leaked: %s", raw)
		}
	}
	summary := decodeCheckinAPI[model.CheckinCredentialSummary](t, f.request(t, "GET", fmt.Sprintf("/credentials/%d", account.CredentialID), nil, 200))
	if !summary.HasCookie || summary.ReferenceCount != 1 {
		t.Fatal("credential summary lost state")
	}
	f.request(t, "PATCH", fmt.Sprintf("/credentials/%d", account.CredentialID), `{"expected_version":0,"name":"renamed"}`, 200)
	saved, _ := f.services.Store.GetCredential(t.Context(), account.CredentialID)
	if saved.Cookie != "session=api-fixture-secret" || saved.CredentialVersion != 1 {
		t.Fatal("omitted secret changed")
	}
	f.request(t, "PATCH", fmt.Sprintf("/credentials/%d", account.CredentialID), `{"expected_version":0,"cookie":"session=stale"}`, 409)
	f.request(t, "PATCH", fmt.Sprintf("/credentials/%d", account.CredentialID), `{"name":"missing version"}`, 400)
	f.request(t, "PATCH", fmt.Sprintf("/credentials/%d", account.CredentialID), `{"expected_version":1,"cookie":null}`, 400)
	f.request(t, "DELETE", fmt.Sprintf("/credentials/%d", account.CredentialID), nil, 409)
	f.request(t, "DELETE", fmt.Sprintf("/sites/%d", site.ID), nil, 409)
	f.request(t, "PATCH", fmt.Sprintf("/accounts/%d", account.ID), `{"enabled":false,"auto_checkin":false,"checkin_random_window_minutes":0,"balance_config":null}`, 200)
	savedAccount, _ := f.services.Store.GetAccount(t.Context(), account.ID)
	if savedAccount.Enabled || savedAccount.AutoCheckin || savedAccount.BalanceConfig.Mode != model.CheckinBalanceDisabled {
		t.Fatal("false/null patch ignored")
	}
	f.request(t, "PATCH", fmt.Sprintf("/sites/%d", site.ID), `{"enabled":false,"tags":[],"custom_header":null,"checkin_status":null}`, 200)
	f.request(t, "POST", fmt.Sprintf("/sites/%d/archive", site.ID), nil, 200)
	f.request(t, "POST", "/accounts", map[string]any{"site_id": site.ID, "name": "other", "credential_id": account.CredentialID, "auth_mode": "cookie"}, 409)
	f.request(t, "POST", fmt.Sprintf("/sites/%d/restore", site.ID), nil, 200)
	storedSite, _ := f.services.Store.GetSite(t.Context(), site.ID)
	if storedSite.Enabled || storedSite.Archived {
		t.Fatal("restore changed enabled setting")
	}
	f.request(t, "DELETE", fmt.Sprintf("/accounts/%d", account.ID), nil, 200)
	f.request(t, "DELETE", fmt.Sprintf("/sites/%d", site.ID), nil, 200)
	f.request(t, "DELETE", fmt.Sprintf("/credentials/%d", account.CredentialID), nil, 200)
	f.request(t, "GET", fmt.Sprintf("/credentials/%d", account.CredentialID), nil, 404)
}

func TestCheckinAPIRejectsInvalidRequestsAndRollsBack(t *testing.T) {
	f := newCheckinAPIFixture(t)
	site := f.site(t, "https://checkin.example", false)
	account := f.account(t, site)
	for _, request := range []struct{ method, path, body string }{
		{"POST", "/sites", `{"name":"a","name":"b"}`},
		{"POST", "/sites", `{"name":"a","Name":"b"}`},
		{"POST", "/sites", `{"id":42}`},
		{"POST", "/sites", `{"enabled":null}`},
		{"POST", "/sites", `{"checkin_status":{"skip_precheck":null}}`},
		{"POST", "/sites", `{"tags":[null]}`},
		{"POST", "/sites", `{} {}`},
		{"POST", "/sites", `null`},
		{"POST", "/sites", `[]`},
		{"PATCH", fmt.Sprintf("/accounts/%d", account.ID), `{"checkin_pending":{"submitted":false}}`},
		{"PATCH", fmt.Sprintf("/accounts/%d", account.ID), `{"site_id":2}`},
		{"PATCH", fmt.Sprintf("/accounts/%d", account.ID), `{"balance_config":{"mode":null}}`},
		{"POST", "/credentials", `{"name":"c","type":"access_token","platform":"sub2api","auth_base_url":"https://example.com","access_token":null}`},
		{"POST", "/credentials", `{"credential_version":1}`},
		{"POST", fmt.Sprintf("/accounts/%d/execute", account.ID), `{"trigger":"scheduled"}`},
		{"POST", fmt.Sprintf("/accounts/%d/confirm", account.ID), `{"force":true}`},
		{"POST", "/batches", `{"scope":"selected","site_ids":[]}`},
		{"POST", "/batches", `{"scope":"all","site_ids":[1]}`},
		{"POST", "/batches", `{"scope":"selected","site_ids":[0]}`},
		{"POST", "/batches", `{"scope":"selected","site_ids":[999999]}`},
		{"POST", "/accounts", fmt.Sprintf(`{"site_id":%d,"credential_id":%d,"credential":{"name":"invalid"}}`, site.ID, account.CredentialID)},
		{"POST", "/accounts", fmt.Sprintf(`{"site_id":%d,"name":"missing credential","auth_mode":"cookie"}`, site.ID)},
	} {
		t.Run(request.method+request.path+request.body[:min(20, len(request.body))], func(t *testing.T) { f.request(t, request.method, request.path, request.body, 400) })
	}
	for _, path := range []string{"/sites?include_archived=1", "/accounts?site_id=0", "/sites?unknown=1", "/logs?before_id=9223372036854775808", "/logs?limit=101", "/logs?limit=1&limit=2", "/logs?limit=", "/logs?source=typo", "/logs?status=typo", "/stats?timezone=Invalid/Zone", "/stats?from=2026-11-20T00:00:00Z&until=2026-11-19T00:00:00Z", "/sites/-1", "/sites/%2B1"} {
		f.request(t, "GET", path, nil, 400)
	}
	f.request(t, "POST", "/sites", map[string]any{"name": site.Name, "platform": "api", "base_url": site.BaseURL, "checkin_http_enabled": true, "checkin_http_path": "/submit"}, 409)
	var before, after int64
	if err := f.conn.Model(&model.CheckinCredential{}).Count(&before).Error; err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "/accounts", map[string]any{"site_id": site.ID, "name": account.Name, "auth_mode": "cookie", "credential": map[string]any{"name": "rolled back", "type": "cookie", "platform": "api", "auth_base_url": site.BaseURL, "cookie": "session=rollback-secret"}}, 409)
	if err := f.conn.Model(&model.CheckinCredential{}).Count(&after).Error; err != nil || after != before {
		t.Fatal("inline credential escaped rollback", err)
	}
	f.request(t, "POST", "/sites", `{"name":"`+strings.Repeat("x", 1<<20)+`"}`, 413)
	w := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v1/checkin/sites", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer "+f.token)
	request.Header.Set("Content-Type", "text/plain")
	f.router.ServeHTTP(w, request)
	if w.Code != 415 {
		t.Fatal("accepted non-JSON body")
	}
}

func TestCheckinAPIExecutionBalanceAndStringIDs(t *testing.T) {
	var submits, reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/submit":
			submits.Add(1)
			fmt.Fprint(w, `{"success":true,"data":{"reward":0}}`)
		case "/api/v1/auth/me":
			reads.Add(1)
			if r.Header.Get("Authorization") != "Bearer balance-secret" {
				t.Error("wrong balance credential")
			}
			fmt.Fprint(w, `{"code":0,"data":{"balance":0}}`)
		default:
			t.Errorf("unexpected upstream %s", r.URL.Path)
		}
	}))
	defer server.Close()
	f := newCheckinAPIFixture(t)
	site := f.site(t, server.URL, false)
	account := f.account(t, site)
	f.request(t, "POST", fmt.Sprintf("/accounts/%d/confirm", account.ID), nil, 409)
	summary := decodeCheckinAPI[model.CheckinCredentialSummary](t, f.request(t, "POST", "/credentials", map[string]any{"name": "balance", "type": "access_token", "platform": "sub2api", "auth_base_url": server.URL, "access_token": "balance-secret"}, 200))
	f.request(t, "PATCH", fmt.Sprintf("/accounts/%d", account.ID), map[string]any{"balance_config": map[string]any{"mode": "custom", "platform": "sub2api", "base_url": server.URL, "credential_id": summary.ID}}, 200)
	first := decodeCheckinAPI[model.CheckinResult](t, f.request(t, "POST", fmt.Sprintf("/accounts/%d/execute", account.ID), nil, 200))
	if first.Status != model.SiteExecutionStatusSuccess || first.Reward != "0" || first.Replayed || !first.Submitted {
		t.Fatalf("invalid checkin result %+v", first)
	}
	second := decodeCheckinAPI[model.CheckinResult](t, f.request(t, "POST", fmt.Sprintf("/accounts/%d/execute", account.ID), "{}", 200))
	if !second.Replayed || second.LogID != first.LogID || submits.Load() != 1 || reads.Load() != 2 {
		t.Fatal("repeated API execution re-submitted or re-read balances")
	}
	accountJSON := f.request(t, "GET", fmt.Sprintf("/accounts/%d", account.ID), nil, 200)
	if strings.Contains(string(accountJSON), "fingerprint") || strings.Contains(string(accountJSON), "before_unchecked") || strings.Contains(string(accountJSON), "api-fixture-secret") {
		t.Fatalf("private workflow fields leaked: %s", accountJSON)
	}
	balance := decodeCheckinAPI[map[string]any](t, f.request(t, "POST", fmt.Sprintf("/accounts/%d/balance", account.ID), nil, 200))
	if balance["balance"] != float64(0) || balance["today_income"] != nil {
		t.Fatal("unknown/zero balance distinction lost")
	}
	logs := decodeCheckinAPI[struct {
		Items []struct {
			ID     string `json:"id"`
			Reward string `json:"reward"`
		}
	}](t, f.request(t, "GET", "/logs?source=manual", nil, 200))
	if len(logs.Items) != 1 || logs.Items[0].ID != fmt.Sprint(first.LogID) || logs.Items[0].Reward != "0" {
		t.Fatal("history ID or reward changed")
	}
	stats := decodeCheckinAPI[model.SiteCheckinStats](t, f.request(t, "GET", "/stats?source=manual&timezone=Asia%2FShanghai", nil, 200))
	if stats.TotalCount != 1 || stats.UnknownRewardCount != 0 {
		t.Fatal("statistics lost zero reward")
	}
}

func TestCheckinAPIConfirmationAndDisabledOperations(t *testing.T) {
	var submits atomic.Int32
	var confirmed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submits.Add(1)
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		fmt.Fprintf(w, `{"success":true,"data":{"checked":%t,"reward":0.5}}`, confirmed.Load())
	}))
	defer server.Close()
	f := newCheckinAPIFixture(t)
	site := f.site(t, server.URL, true)
	account := f.account(t, site)
	first := decodeCheckinAPI[model.CheckinResult](t, f.request(t, "POST", fmt.Sprintf("/accounts/%d/execute", account.ID), nil, 200))
	if first.Status != model.SiteExecutionStatusPending || !first.Submitted {
		t.Fatal("pending submission not persisted")
	}
	f.request(t, "DELETE", fmt.Sprintf("/accounts/%d", account.ID), nil, 409)
	confirmed.Store(true)
	result := decodeCheckinAPI[model.CheckinResult](t, f.request(t, "POST", fmt.Sprintf("/accounts/%d/confirm", account.ID), nil, 200))
	if result.LogID != first.LogID || result.Reward != "0.5" || result.Status != model.SiteExecutionStatusSuccess || submits.Load() != 1 {
		t.Fatal("confirmation resubmitted or lost reward")
	}
	f.request(t, "POST", fmt.Sprintf("/accounts/%d/balance", account.ID), nil, 409)
	f.request(t, "PATCH", fmt.Sprintf("/accounts/%d", account.ID), `{"enabled":false}`, 200)
	f.request(t, "POST", fmt.Sprintf("/accounts/%d/execute", account.ID), nil, 409)
}

func TestCheckinAPIReferenceErrors(t *testing.T) {
	f := newCheckinAPIFixture(t)
	site := f.site(t, "https://checkin.example", false)
	account := f.account(t, site)
	path := fmt.Sprintf("/accounts/%d", account.ID)
	f.request(t, "PATCH", path, `{"credential_id":999999}`, 404, "checkin.not_found")
	f.request(t, "PATCH", path, `{"auth_mode":"bearer"}`, 400, "checkin.invalid_configuration")
	f.request(t, "PATCH", path, `{"balance_config":{"mode":"primary"}}`, 400, "checkin.invalid_configuration")
	// Proxy configurations are shared infrastructure, independent of subscriptions.
	if err := f.conn.AutoMigrate(&model.ProxyConfiguration{}); err != nil {
		t.Fatal(err)
	}
	proxy := model.ProxyConfiguration{Name: "disabled", URL: "socks5h://127.0.0.1:1080"}
	if err := f.conn.Create(&proxy).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.conn.Model(&proxy).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	f.request(t, "PATCH", path, map[string]any{"proxy_mode": "pool", "proxy_config_id": proxy.ID}, 409, "checkin.proxy_disabled")
	f.request(t, "PATCH", path, `{"proxy_mode":"pool","proxy_config_id":999999}`, 404, "checkin.not_found")
	saved, err := f.services.Store.GetAccount(t.Context(), account.ID)
	if err != nil || saved.CredentialID != account.CredentialID || saved.AuthMode != account.AuthMode || saved.ProxyConfigID != nil {
		t.Fatal("rejected reference patch mutated the account", err)
	}
}

func TestCheckinAPISanitizesStorageErrorsAndLocalRuleTests(t *testing.T) {
	f := newCheckinAPIFixture(t)
	for _, test := range []struct {
		path, body string
		status     int
	}{
		{"/reward/test", `{"code":"return response.reward;","response":{"reward":0}}`, 200},
		{"/reward/test", `{"code":"return response.Reward + response.reward;","response":{"Reward":1,"reward":2}}`, 200},
		{"/reward/test", `{"code":"return 1;","response":{"reward":1,"reward":2}}`, 400},
		{"/reward/test", `{"code":"return responses.status.reward;","response":{},"status_response":{"reward":0.25}}`, 200},
		{"/reward/test", `{"code":"throw new Error('script-secret');","response":{}}`, 400},
		{"/reward/test", `{"code":"return 1;","response":{},"url":"https://must-not-fetch.example"}`, 400},
		{"/status/test", `{"config":{"path":"/status","checked_path":"checked"},"http_status":200,"date":"2026-11-19","response":{"checked":true}}`, 200},
		{"/status/test", `{"config":null,"http_status":200,"date":"2026-11-19","response":{}}`, 400},
	} {
		f.request(t, "POST", test.path, test.body, test.status)
	}
	if err := f.conn.Callback().Create().Before("gorm:create").Register("fixture_failure", func(tx *gorm.DB) { tx.AddError(fmt.Errorf("SQL secret=fixture-db-secret URL=https://secret.example")) }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.conn.Callback().Create().Remove("fixture_failure") })
	var details resp.ErrorDetails
	f.router = gin.New()
	f.router.Use(func(c *gin.Context) { c.Next(); details = resp.RequestError(c) })
	if err := RegisterCheckinRoutes(f.router, f.services); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v1/checkin/credentials", strings.NewReader(`{"name":"c","type":"cookie","platform":"api","auth_base_url":"https://example.com","cookie":"session=secret"}`))
	request.Header.Set("Authorization", "Bearer "+f.token)
	request.Header.Set("Content-Type", "application/json")
	f.router.ServeHTTP(w, request)
	if w.Code != 500 || details.Code != "checkin.internal_error" || details.Cause != "" || strings.Contains(w.Body.String(), "fixture-db-secret") || strings.Contains(fmt.Sprint(details), "fixture-db-secret") {
		t.Fatalf("storage diagnostic leaked: %s %+v", w.Body.String(), details)
	}
}
