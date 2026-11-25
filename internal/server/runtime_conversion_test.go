package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin"
	"github.com/suprelory/octopus-plus/internal/conf"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/auth"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestRuntimeLegacyBackupRelayAndPendingConfirmation(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "converted.db")
	if err := db.InitDB("sqlite", path, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	user := model.User{ID: 1, Username: "conversion-admin", Password: "fixture-password-hash"}
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
	router, err := NewHandler(services)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	date := now.Format("2006-01-02")
	var submissions, confirmations, relays atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/submit":
			submissions.Add(1)
			t.Error("restored workflow submitted again")
			http.Error(w, "unexpected submission", http.StatusConflict)
		case "/status":
			confirmations.Add(1)
			if req.Method != http.MethodGet || req.Header.Get("Cookie") != "session=legacy-fixture" {
				t.Error("confirmation lost its method or independent cookie")
				http.Error(w, "invalid confirmation", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"checked_in": true, "date": date, "reward": 0.25},
			})
		case "/v1/chat/completions":
			relays.Add(1)
			if req.Header.Get("Authorization") != "Bearer legacy-channel-key" || req.Header.Get("X-Fixture") != "converted" {
				t.Error("relay lost the converted channel key or header")
				http.Error(w, "invalid relay credentials", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"id":"converted","object":"chat.completion","model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"converted-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))
		default:
			http.NotFound(w, req)
		}
	}))
	defer upstream.Close()

	site := model.LegacySite{
		ID: 20, Name: "legacy check-in", Kind: model.LegacySiteKindCheckin,
		Platform: model.SitePlatformAPI, BaseURL: upstream.URL, Enabled: true,
		ProxyMode: model.ProxyUsageModeDirect, CheckinMode: model.SiteCheckinModeEnabled,
		CheckinHTTPEnabled: true, CheckinHTTPMethod: "POST", CheckinHTTPPath: "/submit",
		CheckinTimezone: "UTC", CheckinWindowStart: "00:00", CheckinWindowEnd: "23:59",
		CheckinStatus: &model.SiteCheckinStatusConfig{
			Method: "GET", Path: "/status", CheckedPath: "data.checked_in", DatePath: "data.date",
			CheckedValue: json.RawMessage("true"), UncheckedValue: json.RawMessage("false"),
			PollIntervalMs: 1, MaxAttempts: 1, TimeoutSeconds: 5,
			ConfirmTimeoutMinutes: 60, RewardSource: "status",
		},
	}
	account := model.LegacySiteAccount{
		ID: 21, SiteID: site.ID, Name: "legacy cookie", Enabled: true,
		CredentialType: model.LegacySiteCredentialTypeCookie, Cookie: "session=legacy-fixture",
		ProxyMode: model.ProxyUsageModeInherit,
	}
	account.CheckinPending = &model.SiteCheckinPending{
		LogID: 9007199254740993, Date: date, Source: model.CheckinTriggerManual,
		StartedAt: now, Deadline: now.Add(time.Hour), Status: model.SiteExecutionStatusPending,
		BeforeUnchecked: true, Submitted: true, Fingerprint: site.CheckinWorkflowFingerprint(&account),
	}
	key := model.APIKey{ID: 50, Name: "converted key", APIKey: "sk-" + conf.APP_NAME + "-converted-fixture", Enabled: true}
	legacy := model.LegacyDBDump{
		Version: 1, ExportedAt: now, IncludeLogs: true, IncludeStats: true,
		Sites: []model.LegacySite{
			{ID: 10, Name: "retired subscription", Kind: model.LegacySiteKindRelay, Platform: model.SitePlatformNewAPI,
				BaseURL: upstream.URL, Enabled: true, ProxyMode: model.ProxyUsageModeDirect},
			site,
		},
		SiteAccounts: []model.LegacySiteAccount{
			{ID: 11, SiteID: 10, Name: "retired account", Enabled: true, CredentialType: model.LegacySiteCredentialTypeAccessToken,
				AccessToken: "legacy-subscription-token", ProxyMode: model.ProxyUsageModeInherit},
			account,
		},
		Channels: []model.Channel{
			{ID: 100, Name: "converted channel", Type: outbound.OutboundTypeOpenAIChat, Enabled: true,
				BaseUrls: []model.BaseUrl{{URL: upstream.URL + "/v1"}}, Model: "upstream-model",
				ProxyMode: model.ProxyUsageModeDirect, CustomHeader: []model.CustomHeader{{HeaderKey: "X-Fixture", HeaderValue: "converted"}}},
			{ID: 101, Name: "disabled channel", Type: outbound.OutboundTypeOpenAIChat, Enabled: false,
				BaseUrls: []model.BaseUrl{{URL: upstream.URL + "/v1"}}, Model: "upstream-model", ProxyMode: model.ProxyUsageModeDirect},
		},
		ChannelKeys: []model.ChannelKey{
			{ID: 200, ChannelID: 100, Enabled: true, ChannelKey: "legacy-channel-key"},
			{ID: 201, ChannelID: 101, Enabled: false, ChannelKey: "disabled-channel-key"},
		},
		SiteChannelBindings: []model.LegacySiteChannelBinding{{ID: 1, SiteID: 10, SiteAccountID: 11, ChannelID: 100, GroupKey: "default"}},
		Groups:              []model.Group{{ID: 300, Name: "converted-model", Mode: model.GroupModeFailover}},
		GroupItems:          []model.GroupItem{{ID: 400, GroupID: 300, ChannelID: 100, ModelName: "upstream-model", Priority: 1, Weight: 7}},
		APIKeys:             []model.APIKey{key},
		Settings:            []model.Setting{{Key: model.SettingKeyProjectedChannelAutoGroupEnabled, Value: "2"}},
		SiteCheckinLogs: []model.CheckinLog{{
			ID: account.CheckinPending.LogID, SiteID: site.ID, AccountID: account.ID,
			SiteName: site.Name, AccountName: account.Name, Platform: site.Platform,
			Source: "manual", Status: model.SiteExecutionStatusPending, StartedAt: now, FinishedAt: now,
		}},
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	imported := runtimeData[model.DBImportResult](t, runtimeRequest(t, router, token, "POST", "/api/v1/setting/import", payload, 200))
	if imported.RowsAffected["checkin_accounts"] != 1 || imported.RowsAffected["channels"] != 2 {
		t.Fatalf("legacy resources were not converted: %+v", imported)
	}
	repeated := runtimeData[model.DBImportResult](t, runtimeRequest(t, router, token, "POST", "/api/v1/setting/import", payload, 200))
	if !repeated.AlreadyImported {
		t.Fatal("repeated legacy import was not recognized")
	}
	// Restart before confirmation, so neither routing nor submission evidence can
	// depend on conversion-time caches or the removed subscription tables.
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
	if err := op.UserInit(); err != nil {
		t.Fatal(err)
	}
	services = checkin.NewServices(op.NewCheckinStore(db.GetDB()), checkin.LoadNotificationConfig)
	router, err = NewHandler(services)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sites", "site_accounts", "site_tokens", "site_user_groups", "site_models", "site_channel_bindings"} {
		if db.GetDB().Migrator().HasTable(name) {
			t.Fatalf("legacy import created subscription table %s", name)
		}
	}
	channels := runtimeData[[]model.Channel](t, runtimeRequest(t, router, token, "GET", "/api/v1/channel/list", nil, 200))
	if len(channels) != 2 {
		t.Fatalf("converted channel count = %d", len(channels))
	}
	var convertedChannelID int
	for _, channel := range channels {
		if channel.Managed || channel.ManagedSource != nil || channel.AutoSync || len(channel.Keys) != 1 {
			t.Fatal("converted channel retained projection state or changed sync/key configuration")
		}
		if channel.Keys[0].ID <= 0 || channel.Keys[0].ChannelID != channel.ID {
			t.Fatal("converted key refers to a different channel")
		}
		switch channel.Name {
		case "converted channel":
			convertedChannelID = channel.ID
			if !channel.Enabled || channel.AutoGroup != model.AutoGroupTypeExact || !channel.Keys[0].Enabled {
				t.Fatal("projected channel grouping or enablement changed")
			}
		case "disabled channel":
			if channel.Enabled || channel.Keys[0].Enabled || channel.Keys[0].ChannelKey != "disabled-channel-key" {
				t.Fatal("disabled channel or key was enabled during conversion")
			}
		default:
			t.Fatal("channel name changed")
		}
	}
	groups := runtimeData[[]model.Group](t, runtimeRequest(t, router, token, "GET", "/api/v1/group/list", nil, 200))
	if len(groups) != 1 || groups[0].Name != "converted-model" || len(groups[0].Items) != 1 {
		t.Fatal("converted group identity or membership changed")
	}
	item := groups[0].Items[0]
	if item.GroupID != groups[0].ID || item.ChannelID != convertedChannelID || item.Priority != 1 || item.Weight != 7 {
		t.Fatal("converted group routing configuration changed")
	}
	response := runtimeRequest(t, router, key.APIKey, "POST", "/v1/chat/completions", []byte(`{"model":"converted-model","messages":[{"role":"user","content":"hello"}],"stream":false}`), 200)
	if !bytes.Contains(response, []byte("converted-ok")) || relays.Load() != 1 {
		t.Fatal("converted channel did not relay exactly once")
	}
	sites := runtimeData[[]model.CheckinSite](t, runtimeRequest(t, router, token, "GET", "/api/v1/checkin/sites", nil, 200))
	if len(sites) != 1 || sites[0].Name != site.Name {
		t.Fatal("legacy import did not isolate the check-in site")
	}
	accounts := runtimeData[[]model.CheckinAccount](t, runtimeRequest(t, router, token, "GET", "/api/v1/checkin/accounts", nil, 200))
	if len(accounts) != 1 || accounts[0].Name != account.Name || accounts[0].SiteID != sites[0].ID || accounts[0].CheckinPending == nil || !accounts[0].CheckinPending.Submitted {
		t.Fatal("converted account lost its identity or submission")
	}
	confirmPath := fmt.Sprintf("/api/v1/checkin/accounts/%d/confirm", accounts[0].ID)
	confirmed := runtimeData[model.CheckinResult](t, runtimeRequest(t, router, token, "POST", confirmPath, nil, 200))
	if confirmed.Status != model.SiteExecutionStatusSuccess || confirmed.LogID != account.CheckinPending.LogID || confirmed.Reward != "0.25" {
		t.Fatalf("converted confirmation failed: %+v", confirmed)
	}
	runtimeRequest(t, router, token, "POST", confirmPath, nil, 200)
	if submissions.Load() != 0 || confirmations.Load() != 1 {
		t.Fatalf("restored workflow repeated work: submissions=%d confirmations=%d", submissions.Load(), confirmations.Load())
	}
	stats := runtimeData[model.SiteCheckinStats](t, runtimeRequest(t, router, token, "GET", "/api/v1/checkin/stats?timezone=UTC", nil, 200))
	if stats.TotalCount != 1 || stats.TotalReward != 0.25 {
		t.Fatalf("converted confirmation counted more than once: %+v", stats)
	}
}
