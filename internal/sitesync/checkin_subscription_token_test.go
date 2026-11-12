package sitesync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func subscriptionTokenCheckinFixture(t *testing.T, ctx context.Context, subscriptionURL, checkinURL string) (*model.Site, *model.SiteAccount, *model.SiteAccount) {
	t.Helper()
	sourceSite := &model.Site{Name: "Sub2API subscription", Platform: model.SitePlatformSub2API, BaseURL: subscriptionURL}
	if err := op.SiteCreate(sourceSite, ctx); err != nil {
		t.Fatal(err)
	}
	source := &model.SiteAccount{SiteID: sourceSite.ID, Name: "Subscription token", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "subscription-token-one"}
	if err := op.SiteAccountCreate(source, ctx); err != nil {
		t.Fatal(err)
	}
	site := &model.Site{Name: "Sub2API checkin", Kind: model.SiteKindCheckin, Platform: model.SitePlatformSub2API, BaseURL: checkinURL,
		LinkedSiteID: &sourceSite.ID, CheckinHTTPEnabled: true, CheckinUseSubscriptionToken: true, CheckinHTTPPath: "/daily"}
	if err := op.SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "Linked HTTP checkin", CredentialType: model.SiteCredentialTypeLinkedAccount, LinkedAccountID: &source.ID}
	if err := op.SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	return site, account, source
}

func TestCustomCheckinUsesLatestSubscriptionTokenWithoutCopyingIt(t *testing.T) {
	ctx := setupProjectTestDB(t)
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"balance":10}}`))
	}))
	defer subscription.Close()
	var wantToken atomic.Value
	wantToken.Store("subscription-token-one")
	var calls atomic.Int32
	var denied atomic.Bool
	checkin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		token := wantToken.Load().(string)
		if r.Method != http.MethodPost || r.URL.Path != "/daily" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Cookie") != "" {
			t.Errorf("incorrect linked token request: %s %s %v", r.Method, r.URL.Path, r.Header)
		}
		if denied.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(w, `{"message":"rejected %s"}`, token)
			return
		}
		_, _ = fmt.Fprintf(w, `{"success":true,"message":"accepted %s","data":{"reward":2}}`, token)
	}))
	defer checkin.Close()
	site, account, source := subscriptionTokenCheckinFixture(t, ctx, subscription.URL, checkin.URL)
	headers := []model.CustomHeader{{HeaderKey: "Authorization", HeaderValue: "Bearer stale-endpoint-token"}}
	common := []model.CustomHeader{{HeaderKey: "Cookie", HeaderValue: "session=stale-shared-cookie"}}
	if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinHTTPHeaders: &headers, CustomHeader: &common}, ctx); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"subscription-token-one", "subscription-token-two"} {
		wantToken.Store(token)
		storedToken := "Bearer " + token
		if _, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: source.ID, AccessToken: &storedToken}, ctx); err != nil {
			t.Fatal(err)
		}
		result, err := CheckinAccount(ctx, account.ID)
		if err != nil || result == nil || result.Status != model.SiteExecutionStatusSuccess || strings.Contains(result.Message, token) {
			t.Fatalf("linked token checkin: %+v %v", result, err)
		}
	}
	saved, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || saved.AccessToken != "" || saved.Cookie != "" || saved.LastCheckinSuccessAt == nil {
		t.Fatalf("credentials copied or result lost: %+v %v", saved, err)
	}
	denied.Store(true)
	result, err := CheckinAccount(ctx, account.ID)
	if err == nil || result == nil || result.Status != model.SiteExecutionStatusFailed || strings.Contains(result.Message+err.Error(), wantToken.Load().(string)) {
		t.Fatalf("unsafe authentication failure: %+v %v", result, err)
	}
	logs, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
	if err != nil || len(logs.Items) != 3 {
		t.Fatalf("checkin history: %+v %v", logs, err)
	}
	for _, entry := range logs.Items {
		if strings.Contains(entry.Message, "subscription-token-") {
			t.Fatalf("subscription token leaked into history: %s", entry.Message)
		}
	}
	if err := op.SiteAccountDel(source.ID, ctx); err != nil {
		t.Fatal(err)
	}
	result, err = CheckinAccount(ctx, account.ID)
	if err == nil || result == nil || result.Status != model.SiteExecutionStatusFailed || calls.Load() != 3 {
		t.Fatalf("deleted subscription account still used: %+v %v, calls=%d", result, err, calls.Load())
	}
}

func TestCustomCheckinRefreshesSubscriptionSessionOnItsOwnSite(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var refreshes atomic.Int32
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			refreshes.Add(1)
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["refresh_token"] != "subscription-refresh-one" || r.Header.Get("Authorization") != "Bearer subscription-token-one" {
				t.Errorf("invalid session refresh: %v %v", body, err)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"subscription-token-refreshed","refresh_token":"subscription-refresh-two","expires_in":3600}}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer subscription-token-refreshed" {
			t.Errorf("balance query used an expired token: %s %v", r.URL.Path, r.Header)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"balance":10}}`))
	}))
	defer subscription.Close()
	var submits atomic.Int32
	checkin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submits.Add(1)
		if r.URL.Path != "/daily" || r.Header.Get("Authorization") != "Bearer subscription-token-refreshed" || r.Header.Get("Cookie") != "" {
			t.Errorf("wrong refreshed checkin request: %s %v", r.URL.Path, r.Header)
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer checkin.Close()
	_, account, source := subscriptionTokenCheckinFixture(t, ctx, subscription.URL, checkin.URL)
	refresh, expiresAt := "subscription-refresh-one", time.Now().Add(time.Minute).UnixMilli()
	if _, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: source.ID, RefreshToken: &refresh, TokenExpiresAt: &expiresAt}, ctx); err != nil {
		t.Fatal(err)
	}
	result, err := CheckinAccount(ctx, account.ID)
	if err != nil || result == nil || result.Status != model.SiteExecutionStatusSuccess || refreshes.Load() != 1 || submits.Load() != 1 {
		t.Fatalf("refresh/checkin: %+v %v, refreshes=%d submits=%d", result, err, refreshes.Load(), submits.Load())
	}
	savedSource, err := op.SiteAccountGet(source.ID, ctx)
	if err != nil || savedSource.AccessToken != "subscription-token-refreshed" || savedSource.RefreshToken != "subscription-refresh-two" || savedSource.TokenExpiresAt <= expiresAt {
		t.Fatalf("source session not updated: %+v %v", savedSource, err)
	}
	savedCheckin, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || savedCheckin.AccessToken != "" || savedCheckin.RefreshToken != "" || savedCheckin.TokenExpiresAt != 0 {
		t.Fatalf("session copied to checkin account: %+v %v", savedCheckin, err)
	}
}

func TestSubscriptionTokenStatusRecheckUsesUpdatedTokenWithoutResubmitting(t *testing.T) {
	ctx := setupProjectTestDB(t)
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"balance":10}}`))
	}))
	defer subscription.Close()
	var submits, queries atomic.Int32
	var confirmed atomic.Bool
	var wantToken atomic.Value
	wantToken.Store("subscription-token-one")
	checkin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+wantToken.Load().(string) || r.Header.Get("Cookie") != "" {
			t.Errorf("wrong status/submit credentials: %s %v", r.URL.Path, r.Header)
		}
		if r.URL.Path == "/daily" {
			submits.Add(1)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if r.URL.Path != "/status" {
			t.Errorf("unexpected checkin endpoint: %s", r.URL.Path)
		}
		queries.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked_in": confirmed.Load(), "reward": 3}})
	}))
	defer checkin.Close()
	site, account, source := subscriptionTokenCheckinFixture(t, ctx, subscription.URL, checkin.URL)
	config := &model.SiteCheckinStatusConfig{Path: "/status", CheckedPath: "data.checked_in", MaxAttempts: 1, PollIntervalMs: 1, RewardSource: "status",
		Headers: []model.CustomHeader{{HeaderKey: "Authorization", HeaderValue: "Bearer stale-status-token"}}}
	if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinStatus: config, CheckinStatusSet: true}, ctx); err != nil {
		t.Fatal(err)
	}
	first, err := CheckinAccount(ctx, account.ID)
	if err != nil || first == nil || first.Status != model.SiteExecutionStatusPending || submits.Load() != 1 || queries.Load() != 2 {
		t.Fatalf("initial submission: %+v %v, submits=%d queries=%d", first, err, submits.Load(), queries.Load())
	}
	token := "subscription-token-two"
	wantToken.Store(token)
	if _, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: source.ID, AccessToken: &token}, ctx); err != nil {
		t.Fatal(err)
	}
	confirmed.Store(true)
	// Resume automatically after the token changes, as a later scheduler scan.
	if err := db.GetDB().Model(&model.SiteAccount{}).Where("id = ?", account.ID).Update("next_auto_checkin_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	next, err := checkinAccountWithTrigger(ctx, account.ID, SiteBatchTriggerScheduled)
	if err != nil || next == nil || next.Status != model.SiteExecutionStatusSuccess || next.LogID != first.LogID || next.Reward != "3" || submits.Load() != 1 || queries.Load() != 3 {
		t.Fatalf("status recovery: %+v %v, submits=%d queries=%d", next, err, submits.Load(), queries.Load())
	}
}
