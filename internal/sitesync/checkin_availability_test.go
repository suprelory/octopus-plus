package sitesync

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestCheckinRejectsUnavailableAccountsBeforeAnyUpstreamRequest(t *testing.T) {
	for _, tc := range []struct {
		name, reason   string
		siteUpdates    map[string]any
		accountUpdates map[string]any
	}{
		{"disabled site", model.SiteCheckinReasonSiteDisabled, map[string]any{"enabled": false}, nil},
		{"disabled account", model.SiteCheckinReasonAccountDisabled, nil, map[string]any{"enabled": false}},
		{"archived site", model.SiteCheckinReasonSiteArchived, map[string]any{"archived": true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(`{"success":true}`))
			}))
			defer server.Close()
			site, account := createCheckinFixture(t, ctx, server.URL)
			previous := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Millisecond)
			if err := db.GetDB().Model(account).Updates(map[string]any{
				"last_checkin_at": previous, "last_checkin_success_at": previous,
				"last_checkin_status": model.SiteExecutionStatusSuccess, "checkin_failure_count": 0,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.GetDB().Model(site).Updates(tc.siteUpdates).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.GetDB().Model(account).Updates(tc.accountUpdates).Error; err != nil {
				t.Fatal(err)
			}
			for _, trigger := range []SiteBatchTrigger{SiteBatchTriggerManual, SiteBatchTriggerScheduled} {
				result, err := runAccountCheckin(ctx, account.ID, trigger, trigger == SiteBatchTriggerManual)
				if err != nil || result == nil || result.Status != model.SiteExecutionStatusSkipped || result.Reason != tc.reason || result.LogID == 0 {
					t.Fatalf("unavailable account was not skipped: %+v, %v", result, err)
				}
			}
			if calls.Load() != 0 {
				t.Fatalf("unavailable account made %d upstream requests", calls.Load())
			}
			saved, err := op.SiteAccountGet(account.ID, ctx)
			if err != nil {
				t.Fatal(err)
			}
			if saved.LastCheckinAt == nil || !saved.LastCheckinAt.Equal(previous) || saved.LastCheckinSuccessAt == nil ||
				!saved.LastCheckinSuccessAt.Equal(previous) || saved.LastCheckinStatus != model.SiteExecutionStatusSuccess || saved.CheckinFailureCount != 0 {
				t.Fatalf("skipping changed the last actual outcome: %+v", saved)
			}
			logs, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
			if err != nil || len(logs.Items) != 2 {
				t.Fatalf("skip history missing: %+v, %v", logs, err)
			}
		})
	}
}

func TestManualBatchRechecksAccountDisabledAfterSelection(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()
	_, account := createCheckinFixture(t, ctx, server.URL)
	disabled := false
	summary := CheckinAllWithOptions(ctx, SiteBatchOptions{
		Trigger: SiteBatchTriggerManual,
		OnProgress: func(progress SiteBatchProgress) {
			if !disabled && progress.CurrentAccountID == account.ID {
				disabled = true
				if err := db.GetDB().Model(account).Update("enabled", false).Error; err != nil {
					t.Error(err)
				}
			}
		},
	})
	if !disabled || calls.Load() != 0 || summary.Total != 1 || summary.Skipped != 1 || summary.Success != 0 || summary.Failed != 0 {
		t.Fatalf("batch executed a stale enabled account: calls=%d summary=%+v", calls.Load(), summary)
	}
}
