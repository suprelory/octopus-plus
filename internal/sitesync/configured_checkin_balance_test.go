package sitesync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/notify"
	"github.com/suprelory/octopus-plus/internal/op"
)

func createExternalBalanceFixture(t *testing.T, ctx context.Context, sourceURL, externalURL string) (*model.Site, *model.SiteAccount, *model.SiteAccount) {
	t.Helper()
	sourceSite := &model.Site{Name: "Subscription", Platform: model.SitePlatformNewAPI, BaseURL: sourceURL,
		CustomHeader: []model.CustomHeader{{HeaderKey: "X-Subscription", HeaderValue: "source-header"}}}
	if err := op.SiteCreate(sourceSite, ctx); err != nil {
		t.Fatal(err)
	}
	userID := 42
	source := &model.SiteAccount{SiteID: sourceSite.ID, Name: "Subscription account", CredentialType: model.SiteCredentialTypeAccessToken,
		AccessToken: "subscription-token", PlatformUserID: &userID, Balance: 99}
	if err := op.SiteAccountCreate(source, ctx); err != nil {
		t.Fatal(err)
	}
	site := &model.Site{Name: "External rewards", Kind: model.SiteKindCheckin, LinkedSiteID: &sourceSite.ID, Platform: model.SitePlatformAPI,
		BaseURL: externalURL, CheckinHTTPEnabled: true, CheckinHTTPPath: "/daily",
		CheckinHTTPHeaders: []model.CustomHeader{{HeaderKey: "X-External", HeaderValue: "external-header"}}}
	if err := op.SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "Rewards", CredentialType: model.SiteCredentialTypeCookie,
		Cookie: "session=external-cookie", LinkedAccountID: &source.ID, Balance: 88}
	if err := op.SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	return site, account, source
}

func TestConfiguredCheckinRewardPrecedenceAndLinkedBalance(t *testing.T) {
	for _, tc := range []struct {
		name, response, code, before, after, reward string
		failed                                      bool
	}{
		{name: "balance fallback", response: `{"success":true}`, reward: "0.25"},
		{name: "default reward", response: `{"success":true,"data":{"reward":2}}`, reward: "2"},
		{name: "default zero", response: `{"success":true,"data":{"reward":0}}`, reward: "0"},
		{name: "custom wins", response: `{"success":true,"data":{"amount":0.5,"reward":3}}`, code: `return response.data.amount;`, reward: "0.5"},
		{name: "custom zero", response: `{"success":true,"data":{"reward":3}}`, code: `return 0;`, reward: "0"},
		{name: "empty custom uses default", response: `{"success":true,"data":{"reward":3}}`, code: `return null;`, reward: "3"},
		{name: "failed custom uses default", response: `{"success":true,"data":{"reward":3}}`, code: `throw new Error("secret");`, reward: "3"},
		{name: "failed custom uses balance", response: `{"success":true}`, code: `return -1;`, reward: "0.25"},
		{name: "missing baseline", response: `{"success":true}`, before: `{"success":true,"data":{"quota":null}}`},
		{name: "failed refresh", response: `{"success":true}`, after: `{"success":true,"data":{"quota":null}}`},
		{name: "negative delta", response: `{"success":true}`, after: `{"success":true,"data":{"quota":250000,"today_income":0}}`},
		{name: "zero delta", response: `{"success":true}`, after: `{"success":true,"data":{"quota":500000,"today_income":0}}`, reward: "0"},
		{name: "already checked in", response: `{"success":false,"message":"今日已签到","data":{"reward":9}}`, code: `return 10;`},
		{name: "failed checkin", response: `{"success":false,"message":"checkin failed"}`, code: `return 10;`, failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			var checkedIn atomic.Bool
			var balanceReads, checkins atomic.Int32
			var accountID int
			subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/user/self" || r.Header.Get("Authorization") != "Bearer subscription-token" || r.Header.Get("New-Api-User") != "42" ||
					r.Header.Get("X-Subscription") != "source-header" || r.Header.Get("X-External") != "" || strings.Contains(r.Header.Get("Cookie"), "external-cookie") {
					t.Errorf("wrong subscription request: %s %v", r.URL.Path, r.Header)
				}
				balanceReads.Add(1)
				w.Header().Set("Content-Type", "application/json")
				body := tc.before
				if body == "" {
					body = `{"success":true,"data":{"quota":500000,"today_income":0}}`
				}
				if checkedIn.Load() {
					var count int64
					if err := db.GetDB().Model(&model.SiteCheckinLog{}).Where("account_id = ?", accountID).Count(&count).Error; err != nil || count != 1 {
						t.Errorf("balance refreshed before outcome commit: count=%d, %v", count, err)
					}
					body = tc.after
					if body == "" {
						body = `{"success":true,"data":{"quota":625000,"today_income":0}}`
					}
				}
				_, _ = w.Write([]byte(body))
			}))
			defer subscription.Close()
			external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/daily" || r.Header.Get("Cookie") != "session=external-cookie" || r.Header.Get("Authorization") != "" ||
					r.Header.Get("New-Api-User") != "" || r.Header.Get("X-Subscription") != "" || r.Header.Get("X-External") != "external-header" {
					t.Errorf("wrong external request: %s %v", r.URL.Path, r.Header)
				}
				if balanceReads.Load() != 1 {
					t.Error("fresh baseline was not captured before check-in")
				}
				checkins.Add(1)
				checkedIn.Store(true)
				_, _ = w.Write([]byte(tc.response))
			}))
			defer external.Close()
			site, account, source := createExternalBalanceFixture(t, ctx, subscription.URL, external.URL)
			accountID = account.ID
			if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinRewardExtractor: &tc.code}, ctx); err != nil {
				t.Fatal(err)
			}
			result, err := CheckinAccount(ctx, account.ID)
			if (err != nil) != tc.failed || result == nil || result.Reward != tc.reward || checkins.Load() != 1 {
				t.Fatalf("unexpected checkin: %+v, err=%v, checkins=%d", result, err, checkins.Load())
			}
			wantReads := int32(2)
			if tc.failed {
				wantReads = 1
			}
			if balanceReads.Load() != wantReads {
				t.Fatalf("unexpected balance queries: %d", balanceReads.Load())
			}
			page, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
			if err != nil || len(page.Items) != 1 || page.Items[0].Reward != tc.reward {
				t.Fatalf("incorrect history: %+v, %v", page, err)
			}
			stats, err := op.SiteCheckinStats(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
			wantTotal, _ := strconv.ParseFloat(tc.reward, 64)
			if err != nil || stats.TotalReward != wantTotal {
				t.Fatalf("incorrect statistics: %+v, %v", stats, err)
			}
			saved, _ := op.SiteAccountGet(account.ID, ctx)
			savedSource, _ := op.SiteAccountGet(source.ID, ctx)
			if saved.Cookie != account.Cookie || saved.AccessToken != "" || savedSource.AccessToken != source.AccessToken {
				t.Fatal("credentials were copied or changed")
			}
			if !tc.failed && tc.after == "" && (saved.Balance != 1.25 || savedSource.Balance != 1.25) {
				t.Fatal("fresh balance not mirrored")
			}
			if tc.name == "failed refresh" && (saved.Balance != 88 || savedSource.Balance != 99) {
				t.Fatal("failed refresh corrupted cached balances")
			}
		})
	}
}

func TestConfiguredCheckinUsesSubscriptionProxyAndLoginAndNotifies(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var checkedIn atomic.Bool
	var logins, balanceReads atomic.Int32
	const token = "fresh-subscription-token"
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "subscription.invalid" || strings.Contains(r.Header.Get("Cookie"), "external-cookie") {
			t.Error("wrong proxy or cookie")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/user/login" {
			logins.Add(1)
			_, _ = fmt.Fprintf(w, `{"success":true,"data":"%s"}`, token)
			return
		}
		if r.URL.Path != "/api/user/self" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("wrong subscription credentials")
		}
		balanceReads.Add(1)
		quota := 500000
		if checkedIn.Load() {
			quota = 1000000
		}
		_, _ = fmt.Fprintf(w, `{"success":true,"data":{"quota":%d,"today_income":0}}`, quota)
	}))
	defer proxy.Close()
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/daily" || r.Header.Get("Cookie") != "session=external-cookie" || r.Header.Get("Authorization") != "" {
			t.Error("wrong external credentials")
		}
		checkedIn.Store(true)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer external.Close()
	_, account, source := createExternalBalanceFixture(t, ctx, "http://subscription.invalid", external.URL)
	proxyConfig := &model.ProxyConfiguration{Name: "Balance proxy", URL: proxy.URL, Enabled: true}
	if err := db.GetDB().Create(proxyConfig).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Model(&model.Site{}).Where("id = ?", source.SiteID).Updates(map[string]any{"proxy_mode": model.ProxyUsageModePool, "proxy_config_id": proxyConfig.ID}).Error; err != nil {
		t.Fatal(err)
	}
	kind, username, password := model.SiteCredentialTypeUsernamePassword, "subscription-user", "subscription-password"
	if _, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: source.ID, CredentialType: &kind, Username: &username, Password: &password}, ctx); err != nil {
		t.Fatal(err)
	}
	events := make(chan checkinNotification, 1)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event checkinNotification
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		events <- event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sink.Close()
	n := newCheckinNotifier(func() checkinNotificationConfig {
		return checkinNotificationConfig{enabled: true, manualEnabled: true, successEnabled: true, channels: notify.Config{WebhookURL: sink.URL}}
	})
	previous := checkinNotifications
	checkinNotifications = n
	t.Cleanup(func() { n.wg.Wait(); checkinNotifications = previous })
	result, err := CheckinAccount(ctx, account.ID)
	if err != nil || result == nil || result.Reward != "1" || logins.Load() != 1 || balanceReads.Load() != 2 {
		t.Fatalf("checkin: %+v, %v", result, err)
	}
	saved, _ := op.SiteAccountGet(account.ID, ctx)
	savedSource, _ := op.SiteAccountGet(source.ID, ctx)
	if saved.AccessToken != "" || savedSource.AccessToken != token {
		t.Fatal("fresh token was not saved on source alone")
	}
	n.wg.Wait()
	if len(events) != 1 {
		t.Fatal("missing notification")
	}
	event := <-events
	if event.Reward != "1" || event.Balance == nil || *event.Balance != 2 {
		t.Fatalf("notification omitted inferred reward: %+v", event)
	}
}

func TestConfiguredCheckinContinuesWhenBalanceAccountUnavailable(t *testing.T) {
	for _, scenario := range []string{"deleted account", "wrong site", "login failure"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			var probes, checkins atomic.Int32
			subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				probes.Add(1)
				if scenario != "login failure" || r.URL.Path != "/api/user/login" {
					t.Error("queried invalid linked account")
				}
				_, _ = w.Write([]byte(`{"success":false,"message":"login failed"}`))
			}))
			defer subscription.Close()
			external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checkins.Add(1)
				_, _ = w.Write([]byte(`{"success":true}`))
			}))
			defer external.Close()
			site, account, source := createExternalBalanceFixture(t, ctx, subscription.URL, external.URL)
			switch scenario {
			case "deleted account":
				if err := op.SiteAccountDel(source.ID, ctx); err != nil {
					t.Fatal(err)
				}
			case "wrong site":
				if err := db.GetDB().Model(site).Update("linked_site_id", site.ID).Error; err != nil {
					t.Fatal(err)
				}
			case "login failure":
				kind, user, password := model.SiteCredentialTypeUsernamePassword, "user", "password"
				if _, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: source.ID, CredentialType: &kind, Username: &user, Password: &password}, ctx); err != nil {
					t.Fatal(err)
				}
			}
			result, err := CheckinAccount(ctx, account.ID)
			if err != nil || result == nil || result.Status != model.SiteExecutionStatusSuccess || result.Reward != "" || checkins.Load() != 1 {
				t.Fatalf("balance failure interrupted checkin: %+v, %v", result, err)
			}
			if scenario != "login failure" && probes.Load() != 0 {
				t.Fatal("queried wrong balance account")
			}
		})
	}
}
