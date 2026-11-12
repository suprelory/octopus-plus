package sitesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/notify"
	"github.com/suprelory/octopus-plus/internal/op"
	"gorm.io/gorm"
)

func TestCheckinRefreshesBalanceWithoutCorruptingLastSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, checkin, self   string
		platform              model.SitePlatform
		balance, used, income float64
	}{
		{"quota total", `{"success":true,"data":{"reward":1}}`, `{"success":true,"data":{"quota":5000000,"used_quota":500000,"today_income":250000}}`, model.SitePlatformOneAPI, 9, 1, .5},
		{"quota remaining", `{"success":true}`, `{"success":true,"data":{"quota":5000000,"used_quota":500000}}`, model.SitePlatformNewAPI, 10, 1, 3},
		{"real zero", `{"success":true}`, `{"success":true,"data":{"quota":0,"used_quota":0,"today_income":0}}`, model.SitePlatformOneAPI, 0, 0, 0},
		{"already checked in", `{"success":false,"message":"already checked in today","data":{"reward":99}}`, `{"success":true,"data":{"quota":5000000,"used_quota":500000,"today_income":0}}`, model.SitePlatformOneAPI, 9, 1, 0},
		{"business failure", `{"success":true}`, `{"success":false,"data":{"quota":0,"used_quota":0}}`, model.SitePlatformOneAPI, 42, 5, 3},
		{"missing quota", `{"success":true}`, `{"success":true,"data":{"used_quota":0}}`, model.SitePlatformNewAPI, 42, 5, 3},
		{"null quota", `{"success":true}`, `{"success":true,"data":{"quota":null,"used_quota":0}}`, model.SitePlatformOneAPI, 42, 5, 3},
		{"invalid number", `{"success":true}`, `{"success":true,"data":{"quota":"NaN","used_quota":0}}`, model.SitePlatformNewAPI, 42, 5, 3},
		{"missing usage for total", `{"success":true}`, `{"success":true,"data":{"quota":500000}}`, model.SitePlatformOneAPI, 42, 5, 3},
		{"missing optional fields", `{"success":true}`, `{"success":true,"data":{"quota":"500000"}}`, model.SitePlatformNewAPI, 1, 5, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			var accountID int
			var probes atomic.Int32
			var checkedIn atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/user/checkin":
					checkedIn.Store(true)
					_, _ = w.Write([]byte(tc.checkin))
				case "/api/user/self":
					probes.Add(1)
					// The baseline is read before check-in; the refresh still waits
					// until the outcome transaction has committed.
					var wantLogs int64
					if checkedIn.Load() {
						wantLogs = 1
					}
					var count int64
					if err := db.GetDB().Model(&model.SiteCheckinLog{}).Where("account_id = ?", accountID).Count(&count).Error; err != nil || count != wantLogs {
						t.Errorf("unexpected balance probe order: count=%d, want=%d, err=%v", count, wantLogs, err)
					}
					_, _ = w.Write([]byte(tc.self))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			site, account := createCheckinFixture(t, ctx, server.URL)
			accountID = account.ID
			if err := db.GetDB().Model(site).Update("platform", tc.platform).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.GetDB().Model(account).Updates(map[string]any{"balance": 42, "balance_used": 5, "today_income": 3, "platform_user_id": 1}).Error; err != nil {
				t.Fatal(err)
			}
			result, err := CheckinAccount(ctx, account.ID)
			if err != nil || result.Status != model.SiteExecutionStatusSuccess || result.LogID == 0 || probes.Load() == 0 {
				t.Fatalf("check-in did not succeed and refresh: %+v, %v, probes=%d", result, err, probes.Load())
			}
			if result.Reason == model.SiteCheckinReasonAlreadyCheckedIn && result.Reward != "" {
				t.Fatal("already checked in counted a reward")
			}
			saved, err := op.SiteAccountGet(account.ID, ctx)
			if err != nil || saved.Balance != tc.balance || saved.BalanceUsed != tc.used || saved.TodayIncome != tc.income {
				t.Fatalf("incorrect refreshed account: %+v, %v", saved, err)
			}
		})
	}
}

func TestCheckinFailureAndUnsupportedDoNotRefresh(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		ctx := setupProjectTestDB(t)
		var probes atomic.Int32
		var checkedIn atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/user/checkin" {
				checkedIn.Store(true)
			} else if checkedIn.Load() {
				probes.Add(1)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		}))
		_, account := createCheckinFixture(t, ctx, server.URL)
		result, _ := CheckinAccount(ctx, account.ID)
		server.Close()
		if result == nil || probes.Load() != 0 {
			t.Fatalf("unexpected balance probe after status %d", status)
		}
	}
}

func TestCheckinRewardFallsBackToFreshBalance(t *testing.T) {
	const (
		beforeRemaining = `{"success":true,"data":{"id":42,"quota":100000}}`
		afterRemaining  = `{"success":true,"data":{"id":42,"quota":150000,"today_income":0}}`
		beforeTotal     = `{"success":true,"data":{"id":42,"quota":1000000,"used_quota":900000}}`
		afterTotal      = `{"success":true,"data":{"id":42,"quota":1050000,"used_quota":900000,"today_income":0}}`
	)
	for _, tc := range []struct {
		name, checkin, before, after, reward string
		platform                             model.SitePlatform
	}{
		{"newapi", `{"success":true}`, beforeRemaining, afterRemaining, "0.1", model.SitePlatformNewAPI},
		{"oneapi total quota", `{"success":true}`, beforeTotal, afterTotal, "0.1", model.SitePlatformOneAPI},
		{"onehub total quota", `{"success":true}`, beforeTotal, afterTotal, "0.1", model.SitePlatformOneHub},
		{"donehub", `{"success":true}`, beforeRemaining, afterRemaining, "0.1", model.SitePlatformDoneHub},
		{"anyrouter", `{"success":true}`, beforeRemaining, afterRemaining, "0.1", model.SitePlatformAnyRouter},
		{"anyrouter concurrent usage", `{"success":true}`, `{"success":true,"data":{"id":42,"quota":50000000,"used_quota":5000000}}`, `{"success":true,"data":{"id":42,"quota":62000000,"used_quota":5500000,"today_income":0}}`, "25", model.SitePlatformAnyRouter},
		{"usage exceeds reward", `{"success":true}`, `{"success":true,"data":{"id":42,"quota":50000000,"used_quota":5000000}}`, `{"success":true,"data":{"id":42,"quota":49500000,"used_quota":18000000,"today_income":0}}`, "25", model.SitePlatformAnyRouter},
		{"usage reset uses balance delta", `{"success":true}`, `{"success":true,"data":{"id":42,"quota":50000000,"used_quota":5000000}}`, `{"success":true,"data":{"id":42,"quota":62000000,"used_quota":0,"today_income":0}}`, "24", model.SitePlatformAnyRouter},
		{"missing usage uses balance delta", `{"success":true}`, `{"success":true,"data":{"id":42,"quota":50000000}}`, `{"success":true,"data":{"id":42,"quota":62000000,"used_quota":5500000,"today_income":0}}`, "24", model.SitePlatformAnyRouter},
		{"null reward", `{"success":true,"data":{"reward":null}}`, beforeRemaining, afterRemaining, "0.1", model.SitePlatformNewAPI},
		{"blank reward", `{"success":true,"data":{"reward":"  "}}`, beforeRemaining, afterRemaining, "0.1", model.SitePlatformNewAPI},
		{"upstream zero wins", `{"success":true,"data":{"reward":0}}`, beforeRemaining, afterRemaining, "0", model.SitePlatformNewAPI},
		{"upstream reward wins", `{"success":true,"data":{"reward":12.5}}`, beforeRemaining, afterRemaining, "12.5", model.SitePlatformNewAPI},
		{"zero difference", `{"success":true}`, afterRemaining, afterRemaining, "0", model.SitePlatformNewAPI},
		{"zero baseline", `{"success":true}`, `{"success":true,"data":{"quota":0}}`, afterRemaining, "0.3", model.SitePlatformNewAPI},
		{"negative difference", `{"success":true}`, afterRemaining, beforeRemaining, "", model.SitePlatformNewAPI},
		{"missing baseline", `{"success":true}`, `{"success":true,"data":{}}`, afterRemaining, "", model.SitePlatformNewAPI},
		{"invalid baseline", `{"success":true}`, `{"success":true,"data":{"quota":"NaN"}}`, afterRemaining, "", model.SitePlatformNewAPI},
		{"failed baseline", `{"success":true}`, `{"success":false}`, afterRemaining, "", model.SitePlatformNewAPI},
		{"missing usage for total", `{"success":true}`, beforeRemaining, afterTotal, "", model.SitePlatformOneAPI},
		{"missing refreshed balance", `{"success":true}`, beforeRemaining, `{"success":true,"data":{}}`, "", model.SitePlatformNewAPI},
		{"null refreshed balance", `{"success":true}`, beforeRemaining, `{"success":true,"data":{"quota":null}}`, "", model.SitePlatformNewAPI},
		{"already checked in", `{"success":false,"message":"already checked in today"}`, beforeRemaining, afterRemaining, "", model.SitePlatformNewAPI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			var checkins, beforeReads, afterReads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/user/checkin":
					if beforeReads.Load() == 0 {
						t.Error("check-in did not capture a fresh baseline")
					}
					checkins.Add(1)
					_, _ = w.Write([]byte(tc.checkin))
				case "/api/user/self":
					if checkins.Load() == 0 {
						beforeReads.Add(1)
						_, _ = w.Write([]byte(tc.before))
					} else {
						afterReads.Add(1)
						_, _ = w.Write([]byte(tc.after))
					}
				case "/api/log/self":
					if checkins.Load() == 0 {
						t.Error("baseline probe fetched unrelated income logs")
					}
					http.NotFound(w, r)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			site, account := createCheckinFixture(t, ctx, server.URL)
			if err := db.GetDB().Model(site).Update("platform", tc.platform).Error; err != nil {
				t.Fatal(err)
			}
			// This deliberately stale cache must never participate in the reward.
			if err := db.GetDB().Model(account).Updates(map[string]any{"balance": 42, "platform_user_id": 42}).Error; err != nil {
				t.Fatal(err)
			}
			result, err := CheckinAccount(ctx, account.ID)
			if err != nil || result == nil || result.Status != model.SiteExecutionStatusSuccess || result.Reward != tc.reward || checkins.Load() != 1 || afterReads.Load() == 0 {
				t.Fatalf("incorrect balance reward: %+v, err=%v, checkins=%d, refreshes=%d", result, err, checkins.Load(), afterReads.Load())
			}
			page, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
			if err != nil || len(page.Items) != 1 || page.Items[0].Reward != tc.reward {
				t.Fatalf("reward missing from check-in history: %+v, %v", page, err)
			}
			stats, err := op.SiteCheckinStats(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
			wantTotal, _ := strconv.ParseFloat(tc.reward, 64)
			wantUnknown := 0
			if tc.reward == "" && result.Reason != model.SiteCheckinReasonAlreadyCheckedIn {
				wantUnknown = 1
			}
			if err != nil || stats.TotalReward != wantTotal || stats.UnknownRewardCount != wantUnknown {
				t.Fatalf("reward missing from check-in statistics: %+v, %v", stats, err)
			}
		})
	}
}

func TestLinkedCheckinBalanceRewardUsesFreshLoginAndNotifies(t *testing.T) {
	ctx := setupProjectTestDB(t)
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
	const token = "fresh-checkin-login-token"
	var logins, balanceReads atomic.Int32
	var checkedIn atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/user/login" {
			logins.Add(1)
			_, _ = fmt.Fprintf(w, `{"success":true,"data":"%s"}`, token)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("New-Api-User") != "42" {
			t.Errorf("check-in or balance probe did not use resolved credentials: %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/user/self":
			balanceReads.Add(1)
			quota := 5000000
			if checkedIn.Load() {
				quota = 6250000
			}
			_, _ = fmt.Fprintf(w, `{"success":true,"data":{"quota":%d,"today_income":0}}`, quota)
		case "/api/user/checkin":
			if balanceReads.Load() != 1 {
				t.Error("check-in ran before the balance snapshot")
			}
			checkedIn.Store(true)
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	_, account, source := createLinkedCheckinFixture(t, ctx, server.URL)
	kind, username, password := model.SiteCredentialTypeUsernamePassword, "source-user", "source-password"
	if _, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: source.ID, CredentialType: &kind, Username: &username, Password: &password}, ctx); err != nil {
		t.Fatal(err)
	}
	result, err := CheckinAccount(ctx, account.ID)
	if err != nil || result == nil || result.Reward != "2.5" || logins.Load() != 1 || balanceReads.Load() != 2 {
		t.Fatalf("linked balance reward: %+v, %v, logins=%d, probes=%d", result, err, logins.Load(), balanceReads.Load())
	}
	for _, id := range []int{account.ID, source.ID} {
		saved, err := op.SiteAccountGet(id, ctx)
		if err != nil || saved.Balance != 12.5 {
			t.Fatalf("linked balance was not refreshed: %+v, %v", saved, err)
		}
		if id == source.ID && saved.AccessToken != token || id == account.ID && saved.AccessToken != "" {
			t.Fatal("refreshed token was not kept on the subscription account alone")
		}
	}
	n.wg.Wait()
	if len(events) != 1 {
		t.Fatal("missing success notification")
	}
	event := <-events
	var entry model.SiteCheckinLog
	if err := db.GetDB().First(&entry, result.LogID).Error; err != nil || entry.Reward != "2.5" || entry.AccountID != account.ID ||
		event.Reward != entry.Reward || event.Balance == nil || *event.Balance != 12.5 {
		t.Fatalf("notification and history did not use the computed reward: %+v, %+v, %v", event, entry, err)
	}
}

func TestCheckinRewardWriteFailurePreservesSuccess(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var checkedIn atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/checkin":
			checkedIn.Store(true)
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/api/user/self":
			quota := 500000
			if checkedIn.Load() {
				quota = 1000000
			}
			_, _ = fmt.Fprintf(w, `{"success":true,"data":{"quota":%d,"used_quota":0,"today_income":0}}`, quota)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	_, account := createCheckinFixture(t, ctx, server.URL)
	if err := db.GetDB().Model(account).Update("platform_user_id", 42).Error; err != nil {
		t.Fatal(err)
	}
	callback := "test:fail_checkin_reward"
	if err := db.GetDB().Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "site_checkin_logs" {
			tx.AddError(errors.New("reward write failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.GetDB().Callback().Update().Remove(callback) })
	result, err := CheckinAccount(ctx, account.ID)
	if err != nil || result == nil || result.Status != model.SiteExecutionStatusSuccess || result.Reward != "" {
		t.Fatalf("reward write failure changed the outcome: %+v, %v", result, err)
	}
	var entry model.SiteCheckinLog
	if err := db.GetDB().First(&entry, result.LogID).Error; err != nil || entry.Status != model.SiteExecutionStatusSuccess || entry.Reward != "" {
		t.Fatalf("reward write failure lost the outcome: %+v, %v", entry, err)
	}
	saved, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || saved.Balance != 2 || saved.LastCheckinSuccessAt == nil {
		t.Fatalf("reward write failure changed the account state: %+v, %v", saved, err)
	}
}

func TestSub2APIBalanceRequiresValidFieldAndHonorsCancellation(t *testing.T) {
	for _, body := range []string{`{"code":0,"data":{"balance":0}}`, `{"code":0,"data":{"balance":null}}`, `{"code":0,"data":{}}`} {
		t.Run(body, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			site, account := createCheckinFixture(t, ctx, server.URL)
			site.Platform = model.SitePlatformSub2API
			if err := db.GetDB().Model(account).Update("balance", 42).Error; err != nil {
				t.Fatal(err)
			}
			result := refreshAccountBalanceAfterCheckin(ctx, site, account, account.AccessToken)
			if result.ok != (body == `{"code":0,"data":{"balance":0}}`) {
				t.Fatalf("invalid balance accepted: %s, %+v", body, result)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if refreshAccountBalanceAfterCheckin(canceled, site, account, account.AccessToken).ok {
				t.Fatal("canceled refresh succeeded")
			}
		})
	}
}
