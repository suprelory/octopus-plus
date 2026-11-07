package sitesync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/notify"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func captureSiteLogs(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	core, entries := observer.New(zap.InfoLevel)
	previous := log.Logger
	log.Logger = zap.New(core).Sugar()
	t.Cleanup(func() { log.Logger = previous })
	return entries
}

func TestManualDataSyncLogsCountsWithoutSnapshotCredentials(t *testing.T) {
	ctx := setupProjectTestDB(t)
	_, account := createProjectionFixture(t, ctx)
	entries := captureSiteLogs(t)
	enabled := true
	request := ManualSyncRequest{Mode: ManualSyncModeReplace, Format: ManualSyncFormatSnapshot,
		Snapshot: &ManualSyncSnapshotInput{
			Tokens: &[]ManualSyncTokenInput{{Name: "primary", Token: "private-snapshot-token", GroupKey: "default", Enabled: &enabled}},
			Models: &map[string][]ManualSyncModelInput{"default": {{ModelName: "gpt-5"}}},
		}}
	preview, err := PreviewManualSync(ctx, account.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if entries.Len() != 0 {
		t.Fatal("preview logged a sync completion")
	}
	request.PreviewFingerprint = preview.PreviewFingerprint
	result, err := ApplyManualSync(ctx, account.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	completed := entries.FilterMessage("sitesync.sync.complete").All()
	if len(completed) != 1 {
		t.Fatalf("completion logs=%d", len(completed))
	}
	fields := completed[0].ContextMap()
	if fields["trigger"] != "manual_data" || fields["account_id"] != int64(account.ID) || fields["models"] != int64(result.SyncResult.ModelCount) || strings.Contains(fmt.Sprint(fields), "private-snapshot-token") {
		t.Fatalf("unsafe or incomplete summary: %+v", fields)
	}
}

func TestGroupFallbackReportsOnlyFinalFailureAndRedactsAccountSecret(t *testing.T) {
	entries := captureSiteLogs(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"invalid opaque-account-secret"}`))
	}))
	defer server.Close()
	site := &model.Site{ID: 3, BaseURL: server.URL, Platform: model.SitePlatformOneAPI}
	account := &model.SiteAccount{ID: 5, AccessToken: "opaque-account-secret"}
	groups, err := fetchManagementGroups(context.Background(), site, account, account.AccessToken)
	if err != nil || len(groups) != 1 || groups[0].GroupKey != model.SiteDefaultGroupKey {
		t.Fatalf("fallback behavior changed: %+v %v", groups, err)
	}
	if entries.Len() != 1 {
		t.Fatalf("expected one final warning, got %d", entries.Len())
	}
	fields := entries.All()[0].ContextMap()
	if fields["reason"] != "default_group_fallback" || strings.Contains(fmt.Sprint(fields), account.AccessToken) {
		t.Fatalf("unsafe or missing fallback diagnostic: %+v", fields)
	}
}

func TestSyncFailureRedactsEchoedCredentialsInCompletionAndReturnedDiagnostic(t *testing.T) {
	ctx := setupProjectTestDB(t)
	const credential = "opaque-account-secret"
	server := siteErrorTestServer(t, http.StatusUnauthorized, "application/json", `{"message":"invalid `+credential+`"}`)
	site := &model.Site{Name: "Sync diagnostic", BaseURL: server.URL, Platform: model.SitePlatformOneAPI, Enabled: true}
	if err := op.SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "Primary", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: credential, Enabled: true}
	if err := op.SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	entries := captureSiteLogs(t)
	result, err := SyncAccountWithTrigger(ctx, account.ID, "account_create")
	if err == nil || result != nil {
		t.Fatalf("expected early sync failure, got %+v, %v", result, err)
	}
	completed := entries.FilterMessage("sitesync.sync.complete").All()
	if len(completed) != 1 || completed[0].Level != zap.WarnLevel {
		t.Fatalf("expected one warning, got %+v", completed)
	}
	fields := completed[0].ContextMap()
	if fields["site_id"] != int64(site.ID) || fields["trigger"] != "account_create" || fields["status"] != string(model.SiteExecutionStatusFailed) {
		t.Fatalf("incomplete sync diagnostic: %+v", fields)
	}
	for _, message := range []string{fmt.Sprint(fields), log.SafeError(err), log.SafeError(fmt.Errorf("background sync: %w", err))} {
		if strings.Contains(message, credential) || !strings.Contains(message, "401") {
			t.Fatalf("unsafe or incomplete sync diagnostic: %s", message)
		}
	}
}

func TestPartialSyncLogsWarningAndBatchContinuesAfterProgressPanic(t *testing.T) {
	ctx := setupProjectTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/token/":
			_, _ = w.Write([]byte(`{"data":[{"name":"default","key":"opaque-default-key","group":"default","status":1},{"name":"vip","key":"opaque-vip-key","group":"vip","status":1}]}`))
		case "/api/user/self/groups", "/api/user_group_map":
			_, _ = w.Write([]byte(`{"data":{"default":"Default","vip":"VIP"}}`))
		case "/api/user/self":
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":1,"quota":500000,"used_quota":0}}`))
		case "/models", "/v1/models":
			if r.Header.Get("Authorization") == "Bearer sk-opaque-default-key" {
				_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o-mini"}]}`))
			} else {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"invalid opaque-vip-key"}}`))
			}
		default:
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	}))
	defer server.Close()
	site := &model.Site{Name: "Partial sync", BaseURL: server.URL, Platform: model.SitePlatformOneAPI, Enabled: true}
	if err := op.SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	userID := 1
	account := &model.SiteAccount{SiteID: site.ID, Name: "Primary", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "opaque-account-key", PlatformUserID: &userID, Enabled: true}
	if err := op.SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	entries := captureSiteLogs(t)
	result, err := SyncAccount(ctx, account.ID)
	if err != nil || result == nil || result.Status != model.SiteExecutionStatusPartial {
		t.Fatalf("expected partial sync, got %+v, %v", result, err)
	}
	completed := entries.FilterMessage("sitesync.sync.complete").All()
	if len(completed) != 1 || completed[0].Level != zap.WarnLevel {
		t.Fatalf("partial sync was not logged as a warning: %+v", completed)
	}
	if strings.Contains(fmt.Sprint(completed[0].ContextMap()), "opaque-") {
		t.Fatal("sync summary exposed a snapshot credential")
	}
	entries.TakeAll()
	progressCalls := 0
	summary := SyncAccountsWithOptions(ctx, []int{account.ID}, SiteBatchOptions{
		Trigger: SiteBatchTriggerManual,
		OnProgress: func(SiteBatchProgress) {
			progressCalls++
			panic("callback failed password=private-callback-secret")
		},
	})
	if summary.Partial != 1 || summary.Failed != 0 || progressCalls != 1 {
		t.Fatalf("progress panic interrupted or repeatedly affected the batch: %+v, calls=%d", summary, progressCalls)
	}
	if entries.FilterMessage("sitesync.sync.complete").Len() != 0 || entries.FilterMessage("sitesync.sync.warning_summary").Len() != 1 {
		t.Fatalf("batch did not aggregate account outcomes: %+v", entries.All())
	}
	panics := entries.FilterMessage("sitesync.progress.panic").All()
	if len(panics) != 1 || panics[0].ContextMap()["stack"] == "" || strings.Contains(fmt.Sprint(panics[0].ContextMap()), "private-callback-secret") {
		t.Fatalf("unsafe or missing progress diagnostic: %+v", panics)
	}
}

func TestNotificationFailureLogsSafeCauseAndRemainsRetriable(t *testing.T) {
	entries := captureSiteLogs(t)
	n := newCheckinNotifier(nil)
	t.Cleanup(n.wg.Wait) // Join workers before captureSiteLogs restores Logger.
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("private-provider-response"))
	}))
	defer sink.Close()
	config := checkinNotificationConfig{enabled: true, channels: notify.Config{WebhookURL: sink.URL + "/private-webhook?token=private-query"}, cooldown: time.Hour}
	event := checkinNotification{AccountID: 7, Event: "site_checkin_failed"}
	for i := 0; i < 2; i++ {
		if !n.enqueue(config, event) {
			t.Fatal("failed delivery was suppressed by success cooldown")
		}
		n.wg.Wait()
	}
	failures := entries.FilterMessage("checkin.notification.failed").All()
	if len(failures) != 2 {
		t.Fatalf("failure logs=%d", len(failures))
	}
	text := fmt.Sprint(failures[0].ContextMap())
	if !strings.Contains(text, "429") || strings.Contains(text, "private-") {
		t.Fatalf("unsafe or incomplete delivery diagnostic: %s", text)
	}
}
