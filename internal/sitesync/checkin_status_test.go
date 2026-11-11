package sitesync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func statusCheckinFixture(t *testing.T, handler http.HandlerFunc) (context.Context, *model.Site, *model.SiteAccount) {
	t.Helper()
	ctx := setupProjectTestDB(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	site := &model.Site{Name: "Status checkin", Kind: model.SiteKindCheckin, Platform: model.SitePlatformAPI, BaseURL: server.URL,
		Enabled: true, CheckinHTTPEnabled: true, CheckinHTTPMethod: "POST", CheckinHTTPPath: "/api/checkin",
		CheckinStatus: &model.SiteCheckinStatusConfig{Path: "/api/checkin/status", CheckedPath: "data.checked_in", MaxAttempts: 2, PollIntervalMs: 1, RewardSource: "status"}}
	if err := op.SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "Cookie account", CredentialType: model.SiteCredentialTypeCookie, Cookie: "session=status-secret", Enabled: true, AutoCheckin: true}
	if err := op.SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	return ctx, site, account
}

func TestStatusCheckinPollsAndAwardsOnce(t *testing.T) {
	var submits, polls atomic.Int32
	ctx, _, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "session=status-secret" {
			t.Error("missing account cookie")
		}
		if r.URL.Path == "/api/checkin" {
			submits.Add(1)
			// An accepted operation is not a success response in the old adapter.
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"code":0}`))
			return
		}
		if submits.Load() > 0 && polls.Add(1) >= 2 {
			_, _ = w.Write([]byte(`{"data":{"checked_in":true,"reward":0.75}}`))
		} else {
			_, _ = w.Write([]byte(`{"data":{"checked_in":false}}`))
		}
	})
	result, err := CheckinAccount(ctx, account.ID)
	if err != nil || result.Status != model.SiteExecutionStatusSuccess || result.Reason != model.SiteCheckinReasonCheckedIn || result.Reward != "0.75" {
		t.Fatalf("result: %+v, %v", result, err)
	}
	again, err := CheckinAccount(ctx, account.ID)
	if err != nil || again.Reason != model.SiteCheckinReasonAlreadyCheckedIn || again.LogID != result.LogID || again.Reward != "" || submits.Load() != 1 {
		t.Fatalf("replay: %+v, %v, submits=%d", again, err, submits.Load())
	}
	logs, _ := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
	stats, _ := op.SiteCheckinStats(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
	if len(logs.Items) != 1 || stats.TotalReward != .75 {
		t.Fatalf("duplicate log/reward: %+v %+v", logs, stats)
	}
}

func TestStatusCheckinRecoveryQueriesOnlyAndKeepsLog(t *testing.T) {
	var submits atomic.Int32
	var confirmed atomic.Bool
	ctx, site, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/checkin" {
			submits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked_in": confirmed.Load(), "reward": 2}})
	})
	first, err := CheckinAccount(ctx, account.ID)
	if err != nil || first.Status != model.SiteExecutionStatusPending || first.Reward != "" {
		t.Fatalf("pending: %+v %v", first, err)
	}
	saved, _ := op.SiteAccountGet(account.ID, ctx)
	if saved.CheckinFailureCount != 0 || saved.LastCheckinSuccessAt != nil || saved.CheckinPending == nil || !saved.CheckinPending.Submitted {
		t.Fatalf("invalid pending state: %+v", saved)
	}
	// Reloading from the DB has the same state as a fresh process; no memory state
	// or response body is required to resume.
	confirmed.Store(true)
	next, err := checkinAccountWithTrigger(ctx, account.ID, SiteBatchTriggerManual)
	if err != nil || next.Status != model.SiteExecutionStatusSuccess || next.LogID != first.LogID || next.Reward != "2" || !next.SkipBalanceReward || submits.Load() != 1 {
		t.Fatalf("recovery: %+v %v", next, err)
	}
	logs, _ := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{SiteID: site.ID})
	if len(logs.Items) != 1 || logs.Items[0].Status != model.SiteExecutionStatusSuccess {
		t.Fatalf("logs: %+v", logs)
	}
}

func TestStatusCheckinDoesNotSubmitWhenPrecheckUnknownOrAlreadyChecked(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     model.SiteExecutionStatus
	}{
		{"already", `{"data":{"checked_in":true,"reward":99}}`, model.SiteExecutionStatusSuccess},
		{"missing", `{"code":0}`, model.SiteExecutionStatusPending},
		{"wrong type", `{"data":{"checked_in":"false"}}`, model.SiteExecutionStatusPending},
		{"login", `<html>Login</html>`, model.SiteExecutionStatusPending},
		{"business rejection", `{"success":false,"data":{"checked_in":false}}`, model.SiteExecutionStatusPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var submits atomic.Int32
			ctx, _, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					submits.Add(1)
				}
				_, _ = w.Write([]byte(tc.body))
			})
			result, err := CheckinAccount(ctx, account.ID)
			if err != nil || result.Status != tc.status || result.Reward != "" || submits.Load() != 0 {
				t.Fatalf("result: %+v %v", result, err)
			}
		})
	}
}

func TestStatusCheckinReservationSurvivesLostSubmissionResponse(t *testing.T) {
	var submitted atomic.Bool
	var submits atomic.Int32
	ctx, _, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			submitted.Store(true)
			submits.Add(1)
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked_in": submitted.Load()}})
	})
	result, err := CheckinAccount(ctx, account.ID)
	if err != nil || result.Status != model.SiteExecutionStatusSuccess || submits.Load() != 1 {
		t.Fatalf("lost response: %+v %v", result, err)
	}
}

func TestStatusCheckinExpiryAndConfigChangesNeverResubmit(t *testing.T) {
	for _, change := range []string{"expired", "cookie", "disable status", "midnight"} {
		t.Run(change, func(t *testing.T) {
			var submits atomic.Int32
			ctx, site, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					submits.Add(1)
				}
				_, _ = w.Write([]byte(`{"data":{"checked_in":false}}`))
			})
			first, _ := CheckinAccount(ctx, account.ID)
			saved, _ := op.SiteAccountGet(account.ID, ctx)
			switch change {
			case "expired":
				saved.CheckinPending.Deadline = time.Now().Add(-time.Minute)
			case "cookie":
				saved.Cookie = "session=changed"
			case "disable status":
				if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinStatusSet: true}, ctx); err != nil {
					t.Fatal(err)
				}
			case "midnight":
				saved.CheckinPending.Date = time.Now().In(resolveSiteCheckinWindow(site).location).AddDate(0, 0, -1).Format("2006-01-02")
			}
			if err := db.GetDB().Model(saved).Select("cookie", "checkin_pending").Updates(saved).Error; err != nil {
				t.Fatal(err)
			}
			result, _ := CheckinAccount(ctx, account.ID)
			if result == nil || result.LogID != first.LogID || result.Status == model.SiteExecutionStatusSuccess || submits.Load() != 1 {
				t.Fatalf("unsafe recovery: %+v submits=%d", result, submits.Load())
			}
			if change != "midnight" {
				saved, err := op.SiteAccountGet(account.ID, ctx)
				if err != nil {
					t.Fatal(err)
				}
				if saved.CheckinPending == nil || !saved.CheckinPending.Finished || saved.NextAutoCheckinAt == nil || saved.LastCheckinAt == nil ||
					!saved.NextAutoCheckinAt.After(*saved.LastCheckinAt) || sameLocalDate(*saved.NextAutoCheckinAt, *saved.LastCheckinAt, resolveSiteCheckinWindow(site).location) {
					t.Fatalf("finished workflow should wait until the next day: %+v", saved)
				}
			}
		})
	}
}

func TestStatusCheckinRuleStrictValuesDateAndScript(t *testing.T) {
	for _, tc := range []struct {
		name, body, code, datePath, want string
		status                           int
	}{
		{"checked", `{"data":{"checked_in":true}}`, "", "", checkinStateChecked, 200},
		{"unchecked", `{"data":{"checked_in":false}}`, "", "", checkinStateUnchecked, 200},
		{"missing", `{}`, "", "", checkinStateUnknown, 200},
		{"string", `{"data":{"checked_in":"true"}}`, "", "", checkinStateUnknown, 200},
		{"wrong date", `{"data":{"checked_in":true,"date":"2026-11-10"}}`, "", "data.date", checkinStateUnknown, 200},
		{"unauthorized", `{"data":{"checked_in":true}}`, "", "", checkinStateUnknown, 401},
		{"code", `{"code":0}`, `return response.code === 0 && context.checkinDate === "2026-11-11" ? "checked_in" : "unknown";`, "", checkinStateChecked, 200},
		{"script unknown", `{}`, `return "unknown";`, "", checkinStateUnknown, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := &model.SiteCheckinStatusConfig{Path: "/status", CheckedPath: "data.checked_in", Code: tc.code, DatePath: tc.datePath}
			state, err := TestCheckinStatusRule(context.Background(), config, tc.status, json.RawMessage(tc.body), "2026-11-11")
			if err != nil || state != tc.want {
				t.Fatalf("state=%s err=%v", state, err)
			}
		})
	}
	for _, code := range []string{`return true;`, `while (true) {}`, `return fetch("https://example.com");`} {
		config := &model.SiteCheckinStatusConfig{Path: "/status", Code: code}
		if _, err := TestCheckinStatusRule(context.Background(), config, 200, json.RawMessage(`{}`), "2026-11-11"); err == nil {
			t.Fatalf("unsafe or invalid code accepted: %s", code)
		}
	}
}

func TestStatusCheckinRestoresMissingHistoryAndSkippedPrecheck(t *testing.T) {
	for _, skipPrecheck := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore history", true: "skip precheck"}[skipPrecheck], func(t *testing.T) {
			var submits atomic.Int32
			var confirmed atomic.Bool
			ctx, site, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					submits.Add(1)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked_in": confirmed.Load(), "reward": 7}})
			})
			if skipPrecheck {
				site.CheckinStatus.SkipPrecheck = true
				if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinStatusSet: true, CheckinStatus: site.CheckinStatus}, ctx); err != nil {
					t.Fatal(err)
				}
			}
			pending, err := CheckinAccount(ctx, account.ID)
			if err != nil || pending.Status != model.SiteExecutionStatusPending {
				t.Fatalf("pending: %+v %v", pending, err)
			}
			if err := db.GetDB().Delete(&model.SiteCheckinLog{}, pending.LogID).Error; err != nil {
				t.Fatal(err)
			}
			confirmed.Store(true)
			result, err := CheckinAccount(ctx, account.ID)
			if err != nil || result.Status != model.SiteExecutionStatusSuccess || result.LogID != pending.LogID || submits.Load() != 1 {
				t.Fatalf("restored: %+v %v", result, err)
			}
			if skipPrecheck && (result.Reward != "" || result.Reason != model.SiteCheckinReasonAlreadyCheckedIn) {
				t.Fatalf("unattributed reward: %+v", result)
			}
		})
	}
}

func TestStatusCheckinResumesOutsideSubmissionWindow(t *testing.T) {
	var submits atomic.Int32
	var confirmed atomic.Bool
	ctx, site, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			submits.Add(1)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked_in": confirmed.Load()}})
	})
	pending, _ := CheckinAccount(ctx, account.ID)
	// The next day's window excludes now. Confirmation is read-only and should
	// still run as soon as its scheduled retry becomes due.
	clock := time.Now().In(resolveSiteCheckinWindow(site).location).Add(2 * time.Hour).Format("15:04")
	if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinWindowStart: &clock, CheckinWindowEnd: &clock}, ctx); err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(-time.Minute)
	if err := db.GetDB().Model(account).Update("next_auto_checkin_at", due).Error; err != nil {
		t.Fatal(err)
	}
	confirmed.Store(true)
	result, err := checkinAccountWithTrigger(ctx, account.ID, SiteBatchTriggerScheduled)
	if err != nil || result.LogID != pending.LogID || result.Status != model.SiteExecutionStatusSuccess || submits.Load() != 1 {
		t.Fatalf("scheduled recovery: %+v %v", result, err)
	}
}

func TestStatusCheckinPrecheckRecoveryDoesNotSubmitOutsideWindow(t *testing.T) {
	var submits atomic.Int32
	var readable atomic.Bool
	ctx, site, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			submits.Add(1)
		}
		if !readable.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"checked_in":false}}`))
	})
	if _, err := CheckinAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	clock := time.Now().In(resolveSiteCheckinWindow(site).location).Add(2 * time.Hour).Format("15:04")
	if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinWindowStart: &clock, CheckinWindowEnd: &clock}, ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Model(account).Update("next_auto_checkin_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	readable.Store(true)
	result, err := checkinAccountWithTrigger(ctx, account.ID, SiteBatchTriggerScheduled)
	if err != nil || result.Status != model.SiteExecutionStatusPending || submits.Load() != 0 {
		t.Fatalf("submitted outside window: %+v %v", result, err)
	}
	var saved model.SiteAccount
	if err := db.GetDB().First(&saved, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.CheckinPending.Submitted {
		t.Fatal("marked submitted while outside window")
	}
}
