package sitesync

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestStatusCheckinModeSwitchPreservesScheduling(t *testing.T) {
	for _, mode := range []string{"response", "builtin"} {
		for _, outcome := range []struct {
			name       string
			httpStatus int
			body       string
			retryDelay time.Duration
		}{
			{"success", http.StatusOK, `{"success":true,"data":{"reward":2}}`, 0},
			{"unauthorized", http.StatusUnauthorized, `{"message":"Unauthorized"}`, 6 * time.Hour},
			{"server error", http.StatusInternalServerError, `{"message":"Unavailable"}`, time.Hour},
		} {
			t.Run(mode+"/"+outcome.name, func(t *testing.T) {
				var submits atomic.Int32
				ctx, site, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/checkin", "/api/user/checkin":
						submits.Add(1)
						w.WriteHeader(outcome.httpStatus)
						_, _ = w.Write([]byte(outcome.body))
					case "/api/user/self":
						_, _ = w.Write([]byte(`{"success":true,"data":{"quota":1000000,"used_quota":0,"today_income":0}}`))
					default:
						t.Errorf("unexpected request after mode switch: %s", r.URL.Path)
						http.NotFound(w, r)
					}
				})
				previous := time.Now().UTC().Add(-24 * time.Hour)
				account.CheckinPending = &model.SiteCheckinPending{
					LogID: 1, Date: previous.In(resolveSiteCheckinWindow(site).location).Format("2006-01-02"),
					StartedAt: previous, Submitted: true, Finished: true, Status: model.SiteExecutionStatusFailed,
				}
				account.CheckinFailureCount = 2
				if mode == "builtin" {
					account.CredentialType = model.SiteCredentialTypeAccessToken
					account.AccessToken = "mode-switch-token"
				}
				if err := db.GetDB().Model(account).Select("checkin_pending", "checkin_failure_count", "credential_type", "access_token").Updates(account).Error; err != nil {
					t.Fatal(err)
				}
				update := &model.SiteUpdateRequest{ID: site.ID, CheckinStatusSet: mode == "response"}
				if mode == "builtin" {
					enabled, platform := false, model.SitePlatformOneAPI
					update.CheckinHTTPEnabled, update.Platform = &enabled, &platform
				}
				if _, err := op.SiteUpdate(update, ctx); err != nil {
					t.Fatal(err)
				}

				result, runErr := CheckinAccount(ctx, account.ID)
				wantStatus := model.SiteExecutionStatusSuccess
				if outcome.retryDelay > 0 {
					wantStatus = model.SiteExecutionStatusFailed
				}
				if result == nil || result.Status != wantStatus || result.Workflow != nil || (runErr != nil) != (outcome.retryDelay > 0) {
					t.Fatalf("unexpected outcome after mode switch: %+v, %v", result, runErr)
				}
				saved, err := op.SiteAccountGet(account.ID, ctx)
				if err != nil {
					t.Fatal(err)
				}
				if saved.LastCheckinAt == nil || saved.NextAutoCheckinAt == nil {
					t.Fatalf("missing checkin state: %+v", saved)
				}
				if outcome.retryDelay == 0 {
					if saved.LastCheckinSuccessAt == nil || !saved.LastCheckinSuccessAt.Equal(*saved.LastCheckinAt) || saved.CheckinFailureCount != 0 {
						t.Fatalf("success reused an old workflow timestamp: %+v", saved)
					}
					if !saved.NextAutoCheckinAt.After(*saved.LastCheckinAt) || sameLocalDate(*saved.NextAutoCheckinAt, *saved.LastCheckinAt, resolveSiteCheckinWindow(site).location) {
						t.Fatalf("successful checkin is still due today: %v", saved.NextAutoCheckinAt)
					}
				} else if saved.CheckinFailureCount != 3 || saved.NextAutoCheckinAt.Before(saved.LastCheckinAt.Add(outcome.retryDelay)) {
					t.Fatalf("failure backoff was lost: failures=%d, last=%v, next=%v", saved.CheckinFailureCount, saved.LastCheckinAt, saved.NextAutoCheckinAt)
				}
				again, err := checkinAccountWithTrigger(ctx, account.ID, SiteBatchTriggerScheduled)
				if err != nil || again == nil || again.Reason != string(SiteBatchReasonScheduledLater) || submits.Load() != 1 {
					t.Fatalf("scheduler repeated the submission: %+v, %v, submits=%d", again, err, submits.Load())
				}
			})
		}
	}
}

func TestStatusCheckinDeletionKeepsConfirmedOutcome(t *testing.T) {
	for _, deleted := range []string{"account", "site"} {
		t.Run(deleted, func(t *testing.T) {
			var accountID, siteID int
			var submitted atomic.Bool
			var original model.SiteCheckinLog
			ctx, site, account := statusCheckinFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					if err := db.GetDB().Where("account_id = ?", accountID).First(&original).Error; err != nil {
						t.Error(err)
					}
					var err error
					if deleted == "account" {
						err = op.SiteAccountDel(accountID, context.Background())
					} else {
						err = op.SiteDel(siteID, context.Background())
					}
					if err != nil {
						t.Error(err)
					}
					submitted.Store(true)
					_, _ = w.Write([]byte(`{"code":0}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked_in": submitted.Load(), "reward": 2}})
			})
			accountID, siteID = account.ID, site.ID
			result, err := CheckinAccount(ctx, account.ID)
			if err != nil || result == nil || result.Status != model.SiteExecutionStatusSuccess || result.Reward != "2" || result.LogID != original.ID || result.Replayed {
				t.Fatalf("deletion discarded confirmed outcome: %+v, %v", result, err)
			}
			logs, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
			if err != nil || len(logs.Items) != 1 {
				t.Fatalf("missing historical outcome: %+v, %v", logs, err)
			}
			entry := logs.Items[0]
			if entry.Status != model.SiteExecutionStatusSuccess || entry.Reward != "2" || entry.SiteName != site.Name || entry.AccountName != account.Name || !entry.StartedAt.Equal(original.StartedAt) {
				t.Fatalf("incorrect historical outcome: %+v", entry)
			}
			// A second late writer must not overwrite the final log or award again,
			// even though the account's workflow reservation no longer exists.
			duplicate := *result
			duplicate.Reward = "99"
			again, err := persistCheckinOutcome(ctx, site, account, SiteBatchTriggerManual, original.StartedAt, &duplicate, "", true)
			if err != nil || again == nil || !again.Replayed || again.Reward != "" {
				t.Fatalf("deleted workflow was awarded twice: %+v, %v", again, err)
			}
			stats, err := op.SiteCheckinStats(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
			if err != nil || stats.TotalCount != 1 || stats.SuccessCount != 1 || stats.TotalReward != 2 {
				t.Fatalf("duplicate or missing reward: %+v, %v", stats, err)
			}
			var count int64
			if err := db.GetDB().Model(&model.SiteAccount{}).Where("id = ?", account.ID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("deleted account was recreated: count=%d, %v", count, err)
			}
		})
	}
}
