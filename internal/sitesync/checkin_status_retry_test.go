package sitesync

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func expireStatusCheckin(t *testing.T, ctx context.Context, accountID int) *model.SiteAccount {
	t.Helper()
	account, err := op.SiteAccountGet(accountID, ctx)
	if err != nil || account.CheckinPending == nil {
		t.Fatalf("load workflow: %+v, %v", account, err)
	}
	account.CheckinPending.Deadline = time.Now().Add(-time.Minute)
	due := time.Now().Add(-time.Minute)
	account.NextAutoCheckinAt = &due
	if err := db.GetDB().Model(account).Select("checkin_pending", "next_auto_checkin_at").Updates(account).Error; err != nil {
		t.Fatal(err)
	}
	return account
}

func TestStatusCheckinManualRetryBeforeSubmission(t *testing.T) {
	for _, change := range []string{"cookie", "rule", "expired", "finished", "cross day", "disable status"} {
		t.Run(change, func(t *testing.T) {
			var readable atomic.Bool
			var submits atomic.Int32
			ctx, site, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/checkin" {
					if !readable.Load() {
						t.Error("submitted before fixing the precheck")
					}
					submits.Add(1)
					_, _ = w.Write([]byte(`{"success":true,"data":{"reward":3}}`))
					return
				}
				if !readable.Load() {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if change == "cookie" && r.Header.Get("Cookie") != "session=fixed" {
					t.Error("retry did not use the updated cookie")
				}
				field := "checked_in"
				if change == "rule" {
					field = "done"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{field: submits.Load() > 0, "reward": 3}})
			})
			first, err := CheckinAccount(ctx, account.ID)
			if err != nil || first == nil || first.Status != model.SiteExecutionStatusPending || first.Workflow.Submitted || submits.Load() != 0 {
				t.Fatalf("unexpected precheck: %+v, %v", first, err)
			}
			switch change {
			case "cookie":
				cookie := "session=fixed"
				if _, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: account.ID, Cookie: &cookie}, ctx); err != nil {
					t.Fatal(err)
				}
			case "rule":
				site.CheckinStatus.CheckedPath = "data.done"
				if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinStatusSet: true, CheckinStatus: site.CheckinStatus}, ctx); err != nil {
					t.Fatal(err)
				}
			case "expired", "finished":
				expireStatusCheckin(t, ctx, account.ID)
				if change == "finished" {
					failed, err := checkinAccountWithTrigger(ctx, account.ID, SiteBatchTriggerScheduled)
					if err == nil || failed == nil || failed.Status != model.SiteExecutionStatusFailed || !failed.Workflow.Finished {
						t.Fatalf("expiry did not finish the attempt: %+v, %v", failed, err)
					}
				}
			case "cross day":
				saved, err := op.SiteAccountGet(account.ID, ctx)
				if err != nil {
					t.Fatal(err)
				}
				saved.CheckinPending.Date = time.Now().In(resolveSiteCheckinWindow(site).location).AddDate(0, 0, -1).Format("2006-01-02")
				if err := db.GetDB().Model(saved).Select("checkin_pending").Updates(saved).Error; err != nil {
					t.Fatal(err)
				}
			case "disable status":
				if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinStatusSet: true}, ctx); err != nil {
					t.Fatal(err)
				}
			}
			readable.Store(true)
			result, err := CheckinAccount(ctx, account.ID)
			if err != nil || result == nil || result.Status != model.SiteExecutionStatusSuccess || result.Reward != "3" || result.LogID == first.LogID || submits.Load() != 1 {
				t.Fatalf("manual retry did not recover: %+v, %v, submits=%d", result, err, submits.Load())
			}
			logs, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
			if err != nil || len(logs.Items) != 2 {
				t.Fatalf("retry lost the original attempt: %+v, %v", logs, err)
			}
			for _, entry := range logs.Items {
				if entry.ID == first.LogID && (entry.Status != model.SiteExecutionStatusFailed || entry.Message == "" || entry.Reward != "") {
					t.Fatalf("incorrect original attempt: %+v", entry)
				}
			}
		})
	}
}

func TestStatusCheckinManualRecheckAfterSubmission(t *testing.T) {
	for _, failure := range []string{"expired", "cookie", "disabled then enabled"} {
		t.Run(failure, func(t *testing.T) {
			var confirmed atomic.Bool
			var submits, queries atomic.Int32
			ctx, site, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/checkin" {
					submits.Add(1)
					return
				}
				queries.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked_in": confirmed.Load(), "reward": 4}})
			})
			first, err := CheckinAccount(ctx, account.ID)
			if err != nil || first == nil || first.Status != model.SiteExecutionStatusPending || submits.Load() != 1 {
				t.Fatalf("submission: %+v, %v", first, err)
			}
			switch failure {
			case "expired":
				expireStatusCheckin(t, ctx, account.ID)
			case "cookie":
				cookie := "session=refreshed"
				if _, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: account.ID, Cookie: &cookie}, ctx); err != nil {
					t.Fatal(err)
				}
			case "disabled then enabled":
				if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinStatusSet: true}, ctx); err != nil {
					t.Fatal(err)
				}
			}
			failed, _ := CheckinAccount(ctx, account.ID)
			if failed == nil || failed.Status != model.SiteExecutionStatusFailed || failed.LogID != first.LogID || !failed.Workflow.Finished {
				t.Fatalf("expected a finalized failure: %+v", failed)
			}
			if failure == "disabled then enabled" {
				again, _ := CheckinAccount(ctx, account.ID)
				if again == nil || !again.Replayed || again.Message != failed.Message {
					t.Fatalf("disabled mode lost the original reason: %+v", again)
				}
				if _, err := op.SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinStatusSet: true, CheckinStatus: site.CheckinStatus}, ctx); err != nil {
					t.Fatal(err)
				}
			}
			beforeQueries := queries.Load()
			savedBefore, err := op.SiteAccountGet(account.ID, ctx)
			if err != nil {
				t.Fatal(err)
			}
			unknown, _ := CheckinAccount(ctx, account.ID)
			if unknown == nil || !unknown.Replayed || unknown.Status != model.SiteExecutionStatusFailed || unknown.Reason != failed.Reason || !strings.Contains(unknown.Message, failed.Message) || queries.Load() <= beforeQueries || submits.Load() != 1 {
				t.Fatalf("failed recheck lost the reason or resubmitted: %+v, queries=%d, submits=%d", unknown, queries.Load(), submits.Load())
			}
			savedAfter, err := op.SiteAccountGet(account.ID, ctx)
			if err != nil || savedAfter.CheckinFailureCount != savedBefore.CheckinFailureCount || !savedAfter.LastCheckinAt.Equal(*savedBefore.LastCheckinAt) || !savedAfter.NextAutoCheckinAt.Equal(*savedBefore.NextAutoCheckinAt) {
				t.Fatalf("unsuccessful recheck changed scheduling: %+v, %v", savedAfter, err)
			}
			confirmed.Store(true)
			recovered, err := CheckinAccount(ctx, account.ID)
			if err != nil || recovered == nil || recovered.Status != model.SiteExecutionStatusSuccess || recovered.Replayed || recovered.LogID != first.LogID || recovered.Reward != "4" || !recovered.SkipBalanceReward || submits.Load() != 1 {
				t.Fatalf("failed workflow did not recover: %+v, %v", recovered, err)
			}
			beforeQueries = queries.Load()
			again, err := CheckinAccount(ctx, account.ID)
			if err != nil || again == nil || !again.Replayed || again.Reason != model.SiteCheckinReasonAlreadyCheckedIn || again.Reward != "" || queries.Load() != beforeQueries || submits.Load() != 1 {
				t.Fatalf("confirmed result was queried or awarded twice: %+v, %v", again, err)
			}
			stats, err := op.SiteCheckinStats(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
			if err != nil || stats.TotalCount != 1 || stats.SuccessCount != 1 || stats.FailedCount != 0 || stats.TotalReward != 4 {
				t.Fatalf("recheck did not update the original log once: %+v, %v", stats, err)
			}
		})
	}
}

func TestStatusCheckinScheduledFailureReplayKeepsReason(t *testing.T) {
	var queries atomic.Int32
	ctx, site, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
		queries.Add(1)
		_, _ = w.Write([]byte(`{"data":{"checked_in":false}}`))
	})
	if _, err := CheckinAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	expireStatusCheckin(t, ctx, account.ID)
	before := queries.Load()
	failed, err := checkinAccountWithTrigger(ctx, account.ID, SiteBatchTriggerScheduled)
	if err == nil || failed == nil || failed.Status != model.SiteExecutionStatusFailed || queries.Load() != before {
		t.Fatalf("scheduler queried after expiry: %+v, %v", failed, err)
	}
	loaded, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := checkinWithStatus(context.WithValue(ctx, checkinTriggerKey{}, SiteBatchTriggerScheduled), site, loaded, "")
	if err != nil || result == nil || !result.Replayed || result.Message != failed.Message || queries.Load() != before {
		t.Fatalf("scheduled replay hid the original reason: %+v, %v", result, err)
	}
}

func TestStatusCheckinManualRecheckCannotConfirmAnotherDate(t *testing.T) {
	var confirmed atomic.Bool
	var submits atomic.Int32
	ctx, site, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			submits.Add(1)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked_in": confirmed.Load()}})
	})
	if _, err := CheckinAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	loaded := expireStatusCheckin(t, ctx, account.ID)
	loaded.CheckinPending.Date = time.Now().In(resolveSiteCheckinWindow(site).location).AddDate(0, 0, -1).Format("2006-01-02")
	if err := db.GetDB().Model(loaded).Select("checkin_pending").Updates(loaded).Error; err != nil {
		t.Fatal(err)
	}
	confirmed.Store(true)
	result, _ := CheckinAccount(ctx, account.ID)
	if result == nil || result.Status != model.SiteExecutionStatusFailed || submits.Load() != 1 {
		t.Fatalf("today's status confirmed yesterday's submission: %+v, submits=%d", result, submits.Load())
	}
}

func TestStatusCheckinRecheckRestoresHistoryOrSurvivesDeletion(t *testing.T) {
	for _, change := range []string{"missing history", "delete account", "delete site"} {
		t.Run(change, func(t *testing.T) {
			var accountID, siteID int
			var confirmed, deleteOnQuery atomic.Bool
			var submits atomic.Int32
			ctx, site, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					submits.Add(1)
					return
				}
				if deleteOnQuery.Swap(false) {
					var err error
					if change == "delete account" {
						err = op.SiteAccountDel(accountID, context.Background())
					} else {
						err = op.SiteDel(siteID, context.Background())
					}
					if err != nil {
						t.Error(err)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked_in": confirmed.Load(), "reward": 5}})
			})
			accountID, siteID = account.ID, site.ID
			first, err := CheckinAccount(ctx, account.ID)
			if err != nil || first == nil || first.Status != model.SiteExecutionStatusPending {
				t.Fatalf("initial submission: %+v, %v", first, err)
			}
			expireStatusCheckin(t, ctx, account.ID)
			failed, _ := checkinAccountWithTrigger(ctx, account.ID, SiteBatchTriggerScheduled)
			if failed == nil || !failed.Workflow.Finished || failed.Status != model.SiteExecutionStatusFailed {
				t.Fatalf("expiry: %+v", failed)
			}
			if change == "missing history" {
				if err := db.GetDB().Delete(&model.SiteCheckinLog{}, first.LogID).Error; err != nil {
					t.Fatal(err)
				}
				unknown, _ := CheckinAccount(ctx, account.ID)
				if unknown == nil || !unknown.Replayed || !strings.Contains(unknown.Message, failed.Message) {
					t.Fatalf("missing history lost the original failure: %+v", unknown)
				}
			} else {
				deleteOnQuery.Store(true)
			}
			confirmed.Store(true)
			result, err := CheckinAccount(ctx, account.ID)
			if err != nil || result == nil || result.Status != model.SiteExecutionStatusSuccess || result.Replayed || result.Reward != "5" || result.LogID != first.LogID || submits.Load() != 1 {
				t.Fatalf("lost confirmed recovery: %+v, %v", result, err)
			}
			stats, err := op.SiteCheckinStats(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
			if err != nil || stats.TotalCount != 1 || stats.SuccessCount != 1 || stats.TotalReward != 5 {
				t.Fatalf("incorrect recovery history: %+v, %v", stats, err)
			}
		})
	}
}

func TestStatusCheckinRestartCannotClearConcurrentSubmission(t *testing.T) {
	ctx, site, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	first, err := CheckinAccount(ctx, account.ID)
	if err != nil || first == nil || first.Workflow.Submitted {
		t.Fatalf("initial precheck: %+v, %v", first, err)
	}
	stale, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil {
		t.Fatal(err)
	}
	otherWorker, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := markStatusCheckinSubmitted(ctx, otherWorker, otherWorker.CheckinPending); err != nil {
		t.Fatal(err)
	}
	if err := resetUnsubmittedStatusCheckin(ctx, site, stale); err == nil {
		t.Fatal("restart cleared another worker's submission reservation")
	}
	saved, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || saved.CheckinPending == nil || !saved.CheckinPending.Submitted || saved.CheckinPending.LogID != first.LogID {
		t.Fatalf("submission reservation was lost: %+v, %v", saved, err)
	}
	logs, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
	if err != nil || len(logs.Items) != 1 || logs.Items[0].Status != model.SiteExecutionStatusPending {
		t.Fatalf("restart changed an in-flight log: %+v, %v", logs, err)
	}
}
