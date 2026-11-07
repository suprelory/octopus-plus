package sitesync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"gorm.io/gorm"
)

func createCheckinFixture(t *testing.T, ctx context.Context, baseURL string) (*model.Site, *model.SiteAccount) {
	t.Helper()
	site := &model.Site{Name: "Checkin site", Kind: model.SiteKindCheckin, BaseURL: baseURL, Platform: model.SitePlatformOneAPI, Enabled: true}
	if err := op.SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "Checkin account", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "checkin-secret-value", Enabled: true, AutoCheckin: true}
	if err := op.SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	return site, account
}

func TestCheckinLogsCaptureOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason, reward string
		httpStatus                 int
		status                     model.SiteExecutionStatus
	}{
		{"earned reward", `{"success":true,"data":{"reward":12.5}}`, model.SiteCheckinReasonCheckedIn, "12.5", 200, model.SiteExecutionStatusSuccess},
		{"already checked in", `{"success":false,"message":"今天已经签到过了","data":{"reward":12.5}}`, model.SiteCheckinReasonAlreadyCheckedIn, "", 200, model.SiteExecutionStatusSuccess},
		{"negative message", `{"success":false,"message":"今日尚未签到过"}`, string(SiteBatchReasonUpstreamBusinessError), "", 200, model.SiteExecutionStatusFailed},
		{"upstream error", `{"message":"failed for checkin-secret-value"}`, string(SiteBatchReasonUpstreamHTTPError), "", 500, model.SiteExecutionStatusFailed},
		{"unsupported", `{"message":"not found"}`, model.SiteCheckinReasonUnsupported, "", 404, model.SiteExecutionStatusSkipped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.httpStatus)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			site, account := createCheckinFixture(t, ctx, server.URL)
			result, runErr := CheckinAccount(ctx, account.ID)
			if result == nil || result.Status != tc.status || result.Reason != tc.reason || result.Reward != tc.reward || result.LogID == 0 {
				t.Fatalf("unexpected outcome: %+v, error=%v", result, runErr)
			}
			if (runErr != nil) != (tc.status == model.SiteExecutionStatusFailed) {
				t.Fatalf("unexpected error: %v", runErr)
			}
			page, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("expected one persisted outcome: %+v, %v", page, err)
			}
			entry := page.Items[0]
			if entry.ID != result.LogID || entry.SiteID != site.ID || entry.SiteName != site.Name || entry.AccountName != account.Name || entry.Source != "manual" || entry.Status != tc.status || entry.Reason != tc.reason || entry.Reward != tc.reward {
				t.Fatalf("unexpected log: %+v", entry)
			}
			if entry.DurationMs < 0 || entry.StartedAt.IsZero() || entry.FinishedAt.Before(entry.StartedAt) || strings.Contains(entry.Message, account.AccessToken) {
				t.Fatalf("invalid or unsafe log metadata: %+v", entry)
			}
			saved, err := op.SiteAccountGet(account.ID, ctx)
			if err != nil || saved.LastCheckinStatus != tc.status || saved.LastCheckinAt == nil {
				t.Fatalf("latest state was not updated: %+v, %v", saved, err)
			}
			if tc.status == model.SiteExecutionStatusSuccess && (saved.LastCheckinSuccessAt == nil || saved.CheckinFailureCount != 0) {
				t.Fatalf("success state was not updated: %+v", saved)
			}
		})
	}
}

func TestManualFullCheckinPersistsQueryableBatchProgress(t *testing.T) {
	ctx := setupProjectTestDB(t)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/checkin" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"reward":1}}`))
	}))
	defer server.Close()
	_, account := createCheckinFixture(t, ctx, server.URL)

	job, err := StartCheckinBatch(ctx)
	if err != nil || job == nil || job.ID == 0 {
		t.Fatalf("start batch failed: %+v, %v", job, err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("batch did not reach the upstream")
	}
	duplicate, err := StartCheckinBatch(ctx)
	if err != nil || duplicate == nil || duplicate.ID != job.ID {
		t.Fatalf("duplicate trigger did not return active task: %+v, %v", duplicate, err)
	}
	active, err := op.SiteCheckinBatchJobGet(ctx, job.ID)
	if err != nil || active == nil || active.Status != model.SiteCheckinBatchJobStatusRunning || active.Total != 1 || active.CurrentAccountID != account.ID {
		t.Fatalf("active progress was not queryable: %+v, %v", active, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate trigger started another batch: calls=%d", calls.Load())
	}

	unblock()
	deadline := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	for {
		finished, err := op.SiteCheckinBatchJobGet(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if finished != nil && finished.Status != model.SiteCheckinBatchJobStatusQueued && finished.Status != model.SiteCheckinBatchJobStatusRunning {
			if finished.Status != model.SiteCheckinBatchJobStatusCompleted || finished.Attempted != 1 || finished.Success != 1 || finished.CurrentAccountID != 0 {
				t.Fatalf("unexpected final batch: %+v", finished)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("batch did not finish")
		case <-deadline.C:
		}
	}
	logs, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{BatchJobID: job.ID})
	if err != nil || len(logs.Items) != 1 || logs.Items[0].BatchJobID != job.ID || logs.Items[0].AccountID != account.ID {
		t.Fatalf("batch-linked check-in log missing: %+v, %v", logs, err)
	}
}

func TestCheckinGuardCoversScheduledBatchAndManualRuns(t *testing.T) {
	ctx := setupProjectTestDB(t)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var mainCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/checkin" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer checkin-secret-value" {
			if mainCalls.Add(1) == 1 {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			} else {
				_, _ = w.Write([]byte(`{"success":true,"message":"already checked in today","data":{"reward":100}}`))
				return
			}
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"reward":1}}`))
	}))
	defer server.Close()
	defer unblock()
	site, account := createCheckinFixture(t, ctx, server.URL)
	done := make(chan SiteBatchSummary, 1)
	go func() { done <- CheckinAllWithOptions(ctx, SiteBatchOptions{Trigger: SiteBatchTriggerScheduled}) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("scheduled batch never reached upstream")
	}
	before, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := CheckinAccount(ctx, account.ID)
	if err != nil || duplicate.Status != model.SiteExecutionStatusSkipped || duplicate.Reason != model.SiteCheckinReasonAlreadyRunning {
		t.Fatalf("duplicate was not skipped: %+v, %v", duplicate, err)
	}
	after, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || after.LastCheckinAt != nil || after.CheckinFailureCount != 0 || after.NextAutoCheckinAt == nil || !after.NextAutoCheckinAt.Equal(*before.NextAutoCheckinAt) {
		t.Fatalf("duplicate changed the running account state: %+v, %v", after, err)
	}
	other := &model.SiteAccount{SiteID: site.ID, Name: "Other account", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "other-secret", Enabled: true}
	if err := op.SiteAccountCreate(other, ctx); err != nil {
		t.Fatal(err)
	}
	if result, err := CheckinAccount(ctx, other.ID); err != nil || result.Status != model.SiteExecutionStatusSuccess {
		t.Fatalf("unrelated account was blocked: %+v, %v", result, err)
	}
	unblock()
	select {
	case summary := <-done:
		if summary.Success != 1 || summary.Failed != 0 {
			t.Fatalf("unexpected batch: %+v", summary)
		}
	case <-ctx.Done():
		t.Fatal("scheduled batch did not finish")
	}
	stale, err := checkinAccountWithTrigger(ctx, account.ID, SiteBatchTriggerScheduled)
	if err != nil || stale.Reason != string(SiteBatchReasonScheduledLater) || mainCalls.Load() != 1 {
		t.Fatalf("stale scheduled work hit upstream again: %+v, %v, calls=%d", stale, err, mainCalls.Load())
	}
	again, err := CheckinAccount(ctx, account.ID)
	if err != nil || again.Reason != model.SiteCheckinReasonAlreadyCheckedIn || again.Reward != "" || mainCalls.Load() != 2 {
		t.Fatalf("guard was not released or reward was counted twice: %+v, %v", again, err)
	}
	page, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{AccountID: account.ID})
	if err != nil || len(page.Items) != 4 {
		t.Fatalf("missing execution history: %+v, %v", page, err)
	}
	foundScheduledSuccess := false
	for _, entry := range page.Items {
		if entry.Source == "scheduled" && entry.Reason == model.SiteCheckinReasonCheckedIn {
			foundScheduledSuccess = true
		}
	}
	if !foundScheduledSuccess {
		t.Fatal("batch source was not persisted")
	}
}

func TestCheckinCanceledRequestStillRecordsOutcome(t *testing.T) {
	dbCtx := setupProjectTestDB(t)
	ctx, cancel := context.WithCancel(dbCtx)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	_, account := createCheckinFixture(t, dbCtx, server.URL)
	result, err := CheckinAccount(ctx, account.ID)
	if !errors.Is(err, context.Canceled) || result == nil || result.Reason != string(SiteBatchReasonContextCanceled) {
		t.Fatalf("unexpected canceled outcome: %+v, %v", result, err)
	}
	page, err := op.SiteCheckinLogList(dbCtx, op.SiteCheckinLogFilter{AccountID: account.ID})
	if err != nil || len(page.Items) != 1 || page.Items[0].Status != model.SiteExecutionStatusFailed {
		t.Fatalf("cancellation lost the log: %+v, %v", page, err)
	}
}

func TestCheckinLogAndLatestStateCommitAtomically(t *testing.T) {
	ctx := setupProjectTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()
	_, account := createCheckinFixture(t, ctx, server.URL)
	callback := "test:fail_checkin_state"
	if err := db.GetDB().Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "site_accounts" {
			tx.AddError(errors.New("state write failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.GetDB().Callback().Update().Remove(callback) })
	if result, err := CheckinAccount(ctx, account.ID); err == nil || result != nil {
		t.Fatalf("failed transaction reported a saved outcome: %+v, %v", result, err)
	}
	page, err := op.SiteCheckinLogList(ctx, op.SiteCheckinLogFilter{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("log was not rolled back: %+v, %v", page, err)
	}
	saved, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || saved.LastCheckinAt != nil {
		t.Fatalf("state was not rolled back: %+v, %v", saved, err)
	}
	if err := db.GetDB().Callback().Update().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if result, err := CheckinAccount(ctx, account.ID); err != nil || result.Status != model.SiteExecutionStatusSuccess {
		t.Fatalf("persistence failure leaked account guard: %+v, %v", result, err)
	}
}
