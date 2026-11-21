package server

import (
	"bytes"
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

	"github.com/suprelory/octopus-plus/internal/backup"
	"github.com/suprelory/octopus-plus/internal/checkin"
	"github.com/suprelory/octopus-plus/internal/conf"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/auth"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func runtimeRequest(t *testing.T, router http.Handler, token, method, path string, body []byte, status int) []byte {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	if len(body) > 0 {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, w.Code, status, w.Body.String())
	}
	return w.Body.Bytes()
}

func runtimeData[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var response struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	return response.Data
}

func TestRuntimeIndependentHTTPRelayAndBackupRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime.db")
	if err := db.InitDB("sqlite", path, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	user := model.User{ID: 1, Username: "runtime-admin", Password: "fixture-password-hash"}
	if err := db.GetDB().Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := op.UserInit(); err != nil {
		t.Fatal(err)
	}
	token, _, err := auth.GenerateJWTTokenForUser(15, user)
	if err != nil {
		t.Fatal(err)
	}
	services := checkin.NewServices(op.NewCheckinStore(db.GetDB()), checkin.LoadNotificationConfig)
	defer func() { _ = services.Close(context.Background()) }()
	r, err := NewHandler(services)
	if err != nil {
		t.Fatal(err)
	}
	// The production registration is reusable and never mounts retired routes.
	if _, err := NewHandler(services); err != nil {
		t.Fatal(err)
	}
	for _, route := range r.Routes() {
		if strings.HasPrefix(route.Path, "/api/v1/site") || strings.Contains(route.Path, "site-action-targets") {
			t.Fatalf("retired route mounted: %s", route.Path)
		}
	}
	runtimeRequest(t, r, "", "GET", "/api/v1/checkin/sites", nil, 401)
	for _, endpoint := range []string{"/api/v1/checkin/sites", "/api/v1/checkin/accounts", "/api/v1/checkin/credentials", "/api/v1/checkin/batches", "/api/v1/checkin/notifications", "/api/v1/channel/list", "/api/v1/group/list", "/api/v1/group/auto-group/config", "/api/v1/proxy-pool/list", "/api/v1/model/list"} {
		runtimeRequest(t, r, token, "GET", endpoint, nil, 200)
	}
	var submitted atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/submit":
			submitted.Add(1)
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"runtime-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))
		default:
			http.NotFound(w, req)
		}
	}))
	defer upstream.Close()
	siteBody, _ := json.Marshal(map[string]any{"name": "runtime-site", "platform": "api", "base_url": upstream.URL, "checkin_http_enabled": true, "checkin_http_path": "/submit"})
	site := runtimeData[model.CheckinSite](t, runtimeRequest(t, r, token, "POST", "/api/v1/checkin/sites", siteBody, 200))
	accountBody, _ := json.Marshal(map[string]any{"site_id": site.ID, "name": "runtime-account", "auth_mode": "cookie", "credential": map[string]any{"name": "runtime-cookie", "type": "cookie", "platform": "api", "auth_base_url": upstream.URL, "cookie": "session=fixture"}})
	account := runtimeData[model.CheckinAccount](t, runtimeRequest(t, r, token, "POST", "/api/v1/checkin/accounts", accountBody, 200))
	result := runtimeData[model.CheckinResult](t, runtimeRequest(t, r, token, "POST", fmt.Sprintf("/api/v1/checkin/accounts/%d/execute", account.ID), nil, 200))
	if result.Status != model.SiteExecutionStatusSuccess || submitted.Load() != 1 {
		t.Fatalf("independent execution failed: %+v", result)
	}
	channel := model.Channel{Name: "runtime-channel", Type: outbound.OutboundTypeOpenAIChat, Enabled: true, BaseUrls: []model.BaseUrl{{URL: upstream.URL + "/v1"}}, Model: "upstream-model", Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "upstream-fixture"}}}
	if err := op.ChannelCreate(&channel, ctx); err != nil {
		t.Fatal(err)
	}
	group := model.Group{Name: "runtime-model", Mode: model.GroupModeRoundRobin}
	if err := op.GroupCreate(&group, ctx); err != nil {
		t.Fatal(err)
	}
	if err := op.GroupItemAdd(&model.GroupItem{GroupID: group.ID, ChannelID: channel.ID, ModelName: "upstream-model"}, ctx); err != nil {
		t.Fatal(err)
	}
	key := model.APIKey{Name: "runtime-key", APIKey: "sk-" + conf.APP_NAME + "-runtime-fixture", Enabled: true}
	if err := op.APIKeyCreate(&key, ctx); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/api/v1/channel/list", "/api/v1/group/list", "/api/v1/group/auto-group/config", "/api/v1/model/list"} {
		runtimeRequest(t, r, token, "GET", endpoint, nil, 200)
	}
	response := runtimeRequest(t, r, key.APIKey, "POST", "/v1/chat/completions", []byte(`{"model":"runtime-model","messages":[{"role":"user","content":"hello"}],"stream":false}`), 200)
	if !bytes.Contains(response, []byte("runtime-ok")) {
		t.Fatal("relay response lost")
	}
	if err := op.StatsSaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	proxy := model.ProxyConfiguration{Name: "runtime-proxy", URL: "socks5h://127.0.0.1:1080", Enabled: true}
	if err := op.ProxyConfigurationCreate(&proxy, ctx); err != nil {
		t.Fatal(err)
	}
	mode := model.ProxyUsageModePool
	proxyID := &proxy.ID
	if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, ProxyMode: &mode, ProxyConfigID: proxyID}, ctx); err != nil {
		t.Fatal(err)
	}
	refs, err := op.ProxyConfigurationReferences(proxy.ID, ctx)
	if err != nil || len(refs) != 1 || refs[0].Type != model.ProxyConfigurationReferenceTypeChannel {
		t.Fatalf("ordinary proxy reference failed: %v", err)
	}
	if err := op.ProxyConfigurationDelete(proxy.ID, ctx); err == nil {
		t.Fatal("deleted a referenced proxy")
	}
	if _, err := op.GroupAutoGroupConfigUpdate(&model.GroupAutoGroupConfigUpdateRequest{ProjectedGlobalAutoGroup: new(model.AutoGroupType)}, ctx); err == nil {
		t.Fatal("retired projection setting accepted")
	}
	runtimeRequest(t, r, token, "GET", "/api/v1/setting/export?version=1", nil, 400)
	archive := runtimeRequest(t, r, token, "GET", "/api/v1/setting/export?format=zip&include_logs=true&include_stats=true", nil, 200)
	// Restore through the default HTTP endpoint into another ordinary v2 database.
	if err := services.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "restored.db")
	if err := db.InitDB("sqlite", path, false); err != nil {
		t.Fatal(err)
	}
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := op.UserInit(); err != nil {
		t.Fatal(err)
	}
	services = checkin.NewServices(op.NewCheckinStore(db.GetDB()), checkin.LoadNotificationConfig)
	r, err = NewHandler(services)
	if err != nil {
		t.Fatal(err)
	}
	restored := runtimeData[model.DBImportResult](t, runtimeRequest(t, r, token, "POST", "/api/v1/setting/import", archive, 200))
	if restored.RowsAffected["checkin_accounts"] != 1 {
		t.Fatal("default restore did not restore independent accounts")
	}
	runtimeRequest(t, r, token, "POST", "/api/v1/setting/import?version=1", archive, 400)
	if err := services.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.InitDB("sqlite", path, false); err != nil {
		t.Fatal(err)
	}
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	services = checkin.NewServices(op.NewCheckinStore(db.GetDB()), checkin.LoadNotificationConfig)
	r, err = NewHandler(services)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := services.Store.ListAccounts(ctx, site.ID)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("restored account missing: %v", err)
	}
	runtimeRequest(t, r, token, "POST", fmt.Sprintf("/api/v1/checkin/accounts/%d/execute", accounts[0].ID), nil, 200)
	if submitted.Load() != 1 {
		t.Fatal("restore/restart repeated submission")
	}
	for _, name := range []string{"sites", "site_accounts", "site_models", "site_channel_bindings"} {
		if db.GetDB().Migrator().HasTable(name) {
			t.Fatalf("runtime created %s", name)
		}
	}
	// A v1 file uses the converter even without an explicit version selector.
	legacy, _ := json.Marshal(model.LegacyDBDump{Version: 1, ExportedAt: time.Now().UTC()})
	runtimeRequest(t, r, token, "POST", "/api/v1/setting/import", legacy, 200)
	if _, err := backup.NewService(db.GetDB()).Export(ctx, false, false); err != nil {
		t.Fatal(err)
	}
	if err := services.Close(ctx); err != nil {
		t.Fatal(err)
	}
	stopped := runtimeRequest(t, r, token, "GET", "/api/v1/checkin/sites", nil, http.StatusServiceUnavailable)
	if !bytes.Contains(stopped, []byte(`"error_code":"checkin.unavailable"`)) {
		t.Fatal("shutdown response lost the API error contract")
	}
}
