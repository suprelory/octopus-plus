package checkin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func enableJobAccount(t *testing.T, store *op.CheckinStore, id int) {
	t.Helper()
	_, err := store.PatchAccount(t.Context(), id, model.CheckinAccountPatch{
		AutoCheckin: model.CheckinPatchField[bool]{Present: true, Value: true},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCheckinJobsCrossDayConfirmationAndDailyDispatch(t *testing.T) {
	ctx := t.Context()
	var submits, reads atomic.Int32
	location, _ := time.LoadLocation("Asia/Shanghai")
	yesterday := time.Now().In(location).AddDate(0, 0, -1).Format(time.DateOnly)
	engine, store, conn, site, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submits.Add(1)
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		reads.Add(1)
		day := r.URL.Query().Get("day")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked": day == yesterday || submits.Load() > 0, "date": day}})
	}, true)
	config := *site.CheckinStatus
	config.DatePath, config.Path = "data.date", "/status?day={{checkin_date}}"
	if _, err := store.PatchSite(ctx, site.ID, model.CheckinSitePatch{CheckinStatus: model.CheckinPatchField[*model.SiteCheckinStatusConfig]{Present: true, Value: &config}}); err != nil {
		t.Fatal(err)
	}
	enableJobAccount(t, store, account.ID)
	original, err := store.BeginWorkflow(ctx, account.ID, model.CheckinTriggerManual, false)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := store.ReserveWorkflowSubmission(ctx, original, true)
	if err != nil {
		t.Fatal(err)
	}
	reserved.Workflow().Date = yesterday
	reserved.Workflow().Deadline = time.Now().Add(time.Hour)
	if err := conn.Select("checkin_pending").Updates(&reserved.Account).Error; err != nil {
		t.Fatal(err)
	}
	jobs := NewJobs(store, engine)
	jobs.interval = 0
	if err := jobs.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := store.GetAccount(ctx, account.ID)
	if err != nil || a.CheckinPending.Date != yesterday || a.CheckinPending.Status != model.SiteExecutionStatusSuccess || reads.Load() != 1 || submits.Load() != 0 {
		t.Fatalf("yesterday's confirmation submitted again or used the wrong day: %+v, %v", a, err)
	}
	if a.NextAutoCheckinAt == nil || a.NextAutoCheckinAt.In(location).Format(time.DateOnly) != time.Now().In(location).Format(time.DateOnly) {
		t.Fatal("confirmation consumed today's slot")
	}
	for range 2 {
		if err := jobs.RunDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if submits.Load() != 1 {
		t.Fatalf("today's submissions=%d", submits.Load())
	}
	var logs []model.CheckinLog
	if err := conn.Order("id").Find(&logs).Error; err != nil || len(logs) != 2 || logs[0].ID != original.Workflow().LogID || logs[0].Source != "manual" || logs[1].Source != "scheduled" {
		t.Fatalf("incorrect historical attribution: %+v, %v", logs, err)
	}
}

func TestCheckinJobsBatchProgressAndConcurrentDispatch(t *testing.T) {
	ctx := t.Context()
	release := make(chan struct{})
	started := make(chan struct{})
	var releaseOnce, entered sync.Once
	var submits atomic.Int32
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	engine, store, conn, site, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
		submits.Add(1)
		entered.Do(func() { close(started) })
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}, false)
	t.Cleanup(unblock)
	enableJobAccount(t, store, account.ID)
	jobs := NewJobs(store, engine)
	jobs.interval = 0
	initCtx, cancel := context.WithCancel(ctx)
	selection := model.CheckinBatchSelection{Scope: "selected", SiteIDs: []int{site.ID}}
	batch, err := jobs.StartBatch(initCtx, selection)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("batch never reached HTTP")
	}
	other := checkinCompetitor(store)
	joined, err := other.StartBatch(ctx, selection)
	if err != nil || joined.ID != batch.ID {
		t.Fatalf("same range started another job: %v", err)
	}
	if _, err := other.StartBatch(ctx, model.CheckinBatchSelection{Scope: "all"}); !errors.Is(err, op.ErrCheckinBatchActive) {
		t.Fatalf("different range did not conflict: %v", err)
	}
	current, err := store.GetBatch(ctx, batch.ID)
	if err != nil || current.Status != model.SiteCheckinBatchJobStatusRunning || current.CurrentAccountID != account.ID || current.Total != 1 {
		t.Fatalf("missing live progress: %+v, %v", current, err)
	}
	if err := other.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	observed, err := other.engine.Execute(ctx, account.ID, model.CheckinTriggerManual)
	if err != nil || observed.Status != model.SiteExecutionStatusPending || !observed.Replayed {
		t.Fatalf("manual contender settled an in-flight submission: %+v, %v", observed, err)
	}
	unblock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err = store.GetBatch(ctx, batch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.FinishedAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("batch did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if current.Status != model.SiteCheckinBatchJobStatusCompleted || current.Success != 1 || current.Attempted != 1 || current.CurrentAccountID != 0 || submits.Load() != 1 {
		t.Fatalf("inconsistent finished job: %+v, submitted=%d", current, submits.Load())
	}
	var logs []model.CheckinLog
	if err := conn.Find(&logs).Error; err != nil || len(logs) != 1 || logs[0].BatchJobID != batch.ID || logs[0].Source != "batch" {
		t.Fatalf("batch log ownership: %+v, %v", logs, err)
	}
	// A later batch can see the completed workflow, and must keep its original owner.
	replay, _, err := store.ReserveBatch(ctx, selection)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.RunBatch(ctx, replay.ID); err != nil {
		t.Fatal(err)
	}
	current, err = store.GetBatch(ctx, replay.ID)
	if err != nil || current.Skipped != 1 || current.Success != 0 || submits.Load() != 1 {
		t.Fatalf("replayed account counted as new: %+v, %v", current, err)
	}
}

func checkinCompetitor(store *op.CheckinStore) *Jobs {
	jobs := NewJobs(store, NewEngine(store, testSessions(store, NewHTTPAuthenticator(store)), store))
	jobs.interval = 0
	return jobs
}

func TestCheckinJobsExpiredOrChangedWorkflowOnlyClosesOriginalLog(t *testing.T) {
	for _, scenario := range []string{"expired", "changed"} {
		t.Run(scenario, func(t *testing.T) {
			var requests atomic.Int32
			engine, store, conn, _, account := httpWorkflow(t, func(http.ResponseWriter, *http.Request) {
				requests.Add(1)
			}, true)
			enableJobAccount(t, store, account.ID)
			started, err := store.BeginWorkflow(t.Context(), account.ID, model.CheckinTriggerManual, false)
			if err != nil {
				t.Fatal(err)
			}
			reserved, err := store.ReserveWorkflowSubmission(t.Context(), started, false)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "expired" {
				reserved.Workflow().Deadline = time.Now().Add(-time.Minute)
			} else {
				reserved.Workflow().Fingerprint = "previous-configuration"
			}
			if err := conn.Select("checkin_pending").Updates(&reserved.Account).Error; err != nil {
				t.Fatal(err)
			}
			if err := checkinCompetitor(store).RunDue(t.Context()); err != nil {
				t.Fatal(err)
			}
			result, err := store.WorkflowResult(t.Context(), account.ID)
			if err != nil || result.Status != model.SiteExecutionStatusFailed || !result.Submitted || result.LogID != started.Workflow().LogID || requests.Load() != 0 {
				t.Fatalf("cleanup did not preserve original submission: %+v, %v", result, err)
			}
			jobs := NewJobs(store, engine)
			jobs.interval = 0
			if err := jobs.RunDue(t.Context()); err != nil || requests.Load() != 0 {
				t.Fatalf("closed workflow submitted again: %v", err)
			}
		})
	}
}

func TestCheckinJobsPendingPrecheckResumesWithinOriginalWorkflow(t *testing.T) {
	var known atomic.Bool
	var submissions atomic.Int32
	engine, store, conn, _, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submissions.Add(1)
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		if !known.Load() {
			_, _ = w.Write([]byte(`{"data":{}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked": submissions.Load() > 0}})
	}, true)
	enableJobAccount(t, store, account.ID)
	jobs := NewJobs(store, engine)
	jobs.interval = 0
	if err := jobs.RunDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending, err := store.WorkflowResult(t.Context(), account.ID)
	if err != nil || pending.Submitted || pending.Status != model.SiteExecutionStatusPending || submissions.Load() != 0 {
		t.Fatalf("unknown precheck sent a submission: %+v, %v", pending, err)
	}
	if err := jobs.RunDue(t.Context()); err != nil || submissions.Load() != 0 {
		t.Fatalf("pending work ignored its next check: %v", err)
	}
	if err := conn.Model(account).Update("next_auto_checkin_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	known.Store(true)
	if err := checkinCompetitor(store).RunDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	confirmed, err := store.WorkflowResult(t.Context(), account.ID)
	if err != nil || confirmed.LogID != pending.LogID || confirmed.Status != model.SiteExecutionStatusSuccess || !confirmed.Submitted || submissions.Load() != 1 {
		t.Fatalf("precheck continuation lost the original workflow: %+v, %v", confirmed, err)
	}
}

func TestCheckinJobsPendingBatchRetainsAssociationThroughRecovery(t *testing.T) {
	var confirmed atomic.Bool
	var submissions atomic.Int32
	engine, store, conn, site, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submissions.Add(1)
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked": confirmed.Load()}})
	}, true)
	enableJobAccount(t, store, account.ID)
	job, _, err := store.ReserveBatch(t.Context(), model.CheckinBatchSelection{Scope: "selected", SiteIDs: []int{site.ID}})
	if err != nil {
		t.Fatal(err)
	}
	jobs := NewJobs(store, engine)
	jobs.interval = 0
	if err := jobs.RunBatch(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	finished, err := store.GetBatch(t.Context(), job.ID)
	if err != nil || finished.Pending != 1 || finished.Attempted != 1 || finished.Success != 0 {
		t.Fatalf("pending batch outcome: %+v, %v", finished, err)
	}
	a, err := store.GetAccount(t.Context(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate restoration without history; the workflow carries its source.
	if err := conn.Delete(&model.CheckinLog{}, a.CheckinPending.LogID).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(account).Update("next_auto_checkin_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	confirmed.Store(true)
	if err := checkinCompetitor(store).RunDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	var entry model.CheckinLog
	if err := conn.First(&entry, a.CheckinPending.LogID).Error; err != nil || entry.BatchJobID != job.ID || entry.Source != "batch" || entry.Status != model.SiteExecutionStatusSuccess || submissions.Load() != 1 {
		t.Fatalf("recovery changed source or repeated submission: %+v, %v", entry, err)
	}
	// Batch totals describe its dispatch pass. The log reflects later confirmation.
	page, err := store.ListBatches(t.Context(), 1, 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].Pending != 1 {
		t.Fatalf("batch snapshot lost on confirmation: %+v, %v", page, err)
	}
}

func TestCheckinJobsCancelPreservesSubmissionAndStopsRemainingAccounts(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var submissions atomic.Int32
	engine, store, _, site, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submissions.Add(1)
			cancel()
		}
		_, _ = w.Write([]byte(`{"data":{"checked":false}}`))
	}, true)
	enableJobAccount(t, store, account.ID)
	second, err := store.CreateAccount(t.Context(), model.CheckinAccount{
		SiteID: site.ID, Name: "second", CredentialID: account.CredentialID,
		Enabled: true, AutoCheckin: true, AuthMode: account.AuthMode,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.ReserveBatch(t.Context(), model.CheckinBatchSelection{Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	jobs := NewJobs(store, engine)
	jobs.interval = 0
	if err := jobs.RunBatch(ctx, job.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel cause lost: %v", err)
	}
	saved, err := store.GetBatch(t.Context(), job.ID)
	if err != nil || saved.Status != model.SiteCheckinBatchJobStatusCanceled || !saved.Canceled || saved.Total != 2 || saved.Attempted != 1 || saved.Pending != 1 || saved.Skipped != 1 || saved.FinishedAt == nil {
		t.Fatalf("canceled batch progress: %+v, %v", saved, err)
	}
	firstAccount, err := store.GetAccount(t.Context(), account.ID)
	if err != nil || firstAccount.CheckinPending == nil || !firstAccount.CheckinPending.Submitted || firstAccount.CheckinPending.Finished {
		t.Fatalf("cancellation lost the submission reservation: %v", err)
	}
	secondAccount, err := store.GetAccount(t.Context(), second.ID)
	if err != nil || secondAccount.CheckinPending != nil || submissions.Load() != 1 {
		t.Fatalf("canceled batch dispatched another account: %v", err)
	}
}

func TestCheckinJobsBackoffOnlyRetriesUnsubmittedWorkflow(t *testing.T) {
	ctx := t.Context()
	var submits atomic.Int32
	engine, store, conn, _, account := httpWorkflow(t, func(w http.ResponseWriter, _ *http.Request) {
		submits.Add(1)
		_, _ = w.Write([]byte(`{"success":false}`))
	}, false)
	enableJobAccount(t, store, account.ID)
	first, err := store.BeginWorkflow(ctx, account.ID, model.CheckinTriggerManual, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.FinishWorkflow(ctx, first, failedOutcome("authentication_unavailable", "session expired"))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetAccount(ctx, account.ID)
	if err != nil || saved.NextAutoCheckinAt == nil || saved.NextAutoCheckinAt.Before(saved.LastCheckinAt.Add(6*time.Hour).Truncate(time.Millisecond)) {
		t.Fatalf("no credential backoff: %+v, %v", saved, err)
	}
	jobs := NewJobs(store, engine)
	jobs.interval = 0
	if err := jobs.RunDue(ctx); err != nil || submits.Load() != 0 {
		t.Fatalf("bypassed cooldown: %v", err)
	}
	past := time.Now().UTC().Add(-7 * time.Hour)
	if err := conn.Model(account).Updates(map[string]any{"last_checkin_at": past, "next_auto_checkin_at": time.Now().UTC().Add(-time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := jobs.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	saved, err = store.GetAccount(ctx, account.ID)
	if err != nil || !saved.CheckinPending.Submitted || !saved.CheckinPending.Finished || saved.CheckinPending.LogID == first.Workflow().LogID || submits.Load() != 1 {
		t.Fatalf("unsubmitted retry did not start a new workflow: %+v, %v", saved, err)
	}
	if err := jobs.RunDue(ctx); err != nil || submits.Load() != 1 {
		t.Fatalf("ambiguous response was resubmitted: %v", err)
	}
}
