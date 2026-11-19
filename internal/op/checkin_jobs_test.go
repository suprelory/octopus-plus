package op

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestIndependentCheckinScheduleCalendarAndPersistence(t *testing.T) {
	store := independentCheckinStore(t)
	ctx := t.Context()
	site, account := workflowAccount(t, store)
	_, err := store.PatchSite(ctx, site.ID, checkinPatch[model.CheckinSitePatch](t, `{"checkin_timezone":"America/New_York","checkin_window_start":"08:00","checkin_window_end":"09:00"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.PatchAccount(ctx, account.ID, checkinPatch[model.CheckinAccountPatch](t, `{"auto_checkin":true,"random_checkin":true,"checkin_random_window_minutes":120}`))
	if err != nil {
		t.Fatal(err)
	}
	location, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 3, 7, 23, 30, 0, 0, location)
	earliest := time.Date(2026, 3, 8, 8, 0, 0, 0, location)
	var first time.Time
	for range 2 {
		other := NewCheckinStore(store.conn)
		request, err := other.PrepareScheduled(ctx, account.ID, now)
		if err != nil || request != nil {
			t.Fatalf("not yet due: %v, %v", request, err)
		}
		a, err := other.GetAccount(ctx, account.ID)
		if err != nil || a.NextAutoCheckinAt == nil || a.NextAutoCheckinAt.Before(earliest) || a.NextAutoCheckinAt.After(earliest.Add(time.Hour)) {
			t.Fatalf("random schedule outside DST window: %+v, %v", a, err)
		}
		if !first.IsZero() && !first.Equal(*a.NextAutoCheckinAt) {
			t.Fatal("restart redrew the persisted random time")
		}
		first = *a.NextAutoCheckinAt
	}
	request, err := store.PrepareScheduled(ctx, account.ID, first)
	if err != nil || request == nil || request.Confirm {
		t.Fatalf("due account did not become executable: %v", err)
	}
}

func TestIndependentCheckinBatchOwnership(t *testing.T) {
	store := independentCheckinStore(t)
	site, account := workflowAccount(t, store)
	testBatchOwnership(t, store, site.ID, account.ID)
}

func TestIndependentCheckinScheduledReservationRace(t *testing.T) {
	store := independentCheckinStore(t)
	site, account := workflowAccount(t, store)
	testScheduledReservationRace(t, store, site.ID, account.ID)
}

func testScheduledReservationRace(t *testing.T, store *CheckinStore, siteID, credentialAccountID int) {
	t.Helper()
	ctx := t.Context()
	original, err := store.GetAccount(ctx, credentialAccountID)
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateAccount(ctx, model.CheckinAccount{
		SiteID: siteID, Name: "scheduled race", CredentialID: original.CredentialID,
		AuthMode: original.AuthMode, Enabled: true, AutoCheckin: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	requests := make(chan *CheckinInvocation, 4)
	for range 4 {
		wg.Go(func() {
			request, err := NewCheckinStore(store.conn).PrepareScheduled(ctx, account.ID, time.Now().UTC())
			if err != nil || request == nil {
				t.Errorf("due scheduled invocation unavailable: %v", err)
				return
			}
			requests <- request
		})
	}
	wg.Wait()
	close(requests)
	var executions []*CheckinExecution
	for request := range requests {
		execution, err := store.BeginExecution(ctx, account.ID, *request)
		if errors.Is(err, ErrCheckinNotDue) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		executions = append(executions, execution)
	}
	if len(executions) != 1 {
		t.Fatalf("one scheduled version created %d workflows", len(executions))
	}
	manual, err := store.BeginWorkflow(ctx, account.ID, model.CheckinTriggerManual, false)
	if err != nil {
		t.Fatal(err)
	}
	executions = append(executions, manual)
	winners := make(chan *CheckinExecution, 2)
	for _, execution := range executions {
		wg.Go(func() {
			winner, err := store.ReserveWorkflowSubmission(ctx, execution, false)
			if err != nil && !errors.Is(err, ErrCheckinWorkflowConflict) {
				t.Errorf("submission race: %v", err)
			}
			if winner != nil {
				winners <- winner
			}
		})
	}
	wg.Wait()
	close(winners)
	var winner *CheckinExecution
	for item := range winners {
		if winner != nil {
			t.Fatal("manual and scheduled callers both reserved submission")
		}
		winner = item
	}
	if winner == nil {
		t.Fatal("no submission owner")
	}
	if _, err := store.FinishWorkflow(ctx, winner, model.CheckinOutcome{Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonCheckedIn}); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetAccount(ctx, account.ID)
	if err != nil || saved.NextAutoCheckinAt == nil {
		t.Fatalf("success did not persist the next schedule: %v", err)
	}
	site, err := store.GetSite(ctx, siteID)
	if err != nil {
		t.Fatal(err)
	}
	location, _ := time.LoadLocation(site.CheckinTimezone)
	if saved.NextAutoCheckinAt.In(location).Format(time.DateOnly) <= saved.CheckinPending.Date {
		t.Fatal("next schedule still belongs to the completed day")
	}
	if invocation, err := store.PrepareScheduled(ctx, account.ID, time.Now()); err != nil || invocation != nil {
		t.Fatalf("completed schedule became immediately due: %v", err)
	}
}

// Run the same contention and interruption checks against all supported databases.
func testBatchOwnership(t *testing.T, store *CheckinStore, siteID, accountID int) {
	t.Helper()
	ctx := t.Context()
	for _, scope := range []model.CheckinBatchSelection{
		{}, {Scope: "selected"}, {Scope: "selected", SiteIDs: []int{siteID, 0}},
		{Scope: "selected", SiteIDs: []int{siteID, 99999999}}, {Scope: "all", SiteIDs: []int{siteID}},
	} {
		if _, _, err := store.ReserveBatch(ctx, scope); !errors.Is(err, model.ErrCheckinBatchSelection) {
			t.Fatalf("invalid selection accepted: %+v, %v", scope, err)
		}
	}
	all := model.CheckinBatchSelection{Scope: "all"}
	selection := model.CheckinBatchSelection{Scope: "selected", SiteIDs: []int{siteID, siteID}}
	var wg sync.WaitGroup
	var created, claims atomic.Int32
	results := make(chan *model.CheckinBatchJob, 8)
	for range 8 {
		wg.Go(func() {
			job, fresh, err := NewCheckinStore(store.conn).ReserveBatch(ctx, selection)
			if err != nil {
				t.Errorf("reserve: %v", err)
				return
			}
			if fresh {
				created.Add(1)
			}
			results <- job
		})
	}
	wg.Wait()
	close(results)
	var id int64
	for job := range results {
		if id != 0 && id != job.ID || !slices.Equal(job.SiteIDs, []int{siteID}) {
			t.Fatal("selected callers received different batches or scopes")
		}
		id = job.ID
	}
	if created.Load() != 1 || id == 0 {
		t.Fatalf("created batches=%d", created.Load())
	}
	if _, _, err := store.ReserveBatch(ctx, all); !errors.Is(err, ErrCheckinBatchActive) {
		t.Fatalf("expected conflicting active range: %v", err)
	}
	for range 4 {
		wg.Go(func() {
			_, err := store.ClaimBatch(ctx, id)
			if err == nil {
				claims.Add(1)
			} else if !errors.Is(err, ErrCheckinBatchStopped) {
				t.Errorf("claim: %v", err)
			}
		})
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("workers=%d", claims.Load())
	}
	if _, err := store.PatchAccount(ctx, accountID, checkinPatch[model.CheckinAccountPatch](t, `{"auto_checkin":true}`)); err != nil {
		t.Fatal(err)
	}
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	// Use a fresh account so the database integration suite can keep the
	// historical reservation that its other tests have already verified.
	fresh, err := store.CreateAccount(ctx, model.CheckinAccount{SiteID: siteID, Name: "batch member", Enabled: true, AutoCheckin: true, AuthMode: account.AuthMode, CredentialID: account.CredentialID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec, err := store.BeginExecution(ctx, fresh.ID, CheckinInvocation{Trigger: model.CheckinTriggerBatch, BatchJobID: id})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.conn.Model(&model.CheckinBatchLease{}).Where("id = 1").Update("until_milli", 1).Error; err != nil {
		t.Fatal(err)
	}
	replacement, freshBatch, err := store.ReserveBatch(ctx, all)
	if err != nil || !freshBatch || replacement.ID == id {
		t.Fatalf("expired batch not replaced: %v", err)
	}
	if _, err := store.ReserveWorkflowSubmission(ctx, exec, false); !errors.Is(err, ErrCheckinBatchStopped) {
		t.Fatalf("stale batch still authorized a dispatch: %v", err)
	}
	if err := store.RenewBatch(ctx, id); !errors.Is(err, ErrCheckinBatchStopped) {
		t.Fatalf("revived old lease: %v", err)
	}
	old, err := store.GetBatch(ctx, id)
	if err != nil || old.Status != model.SiteCheckinBatchJobStatusInterrupted || old.FinishedAt == nil {
		t.Fatalf("interruption not visible: %v", err)
	}
	if err := store.SaveBatchProgress(ctx, old); !errors.Is(err, ErrCheckinBatchStopped) {
		t.Fatalf("overwrote final state: %v", err)
	}
}

func TestIndependentCheckinScheduleRechecksAvailability(t *testing.T) {
	for _, action := range []string{"disable", "archive", "auto off"} {
		t.Run(action, func(t *testing.T) {
			store := independentCheckinStore(t)
			ctx := context.Background()
			site, account := workflowAccount(t, store)
			if _, err := store.PatchAccount(ctx, account.ID, checkinPatch[model.CheckinAccountPatch](t, `{"auto_checkin":true}`)); err != nil {
				t.Fatal(err)
			}
			request, err := store.PrepareScheduled(ctx, account.ID, time.Now())
			if err != nil || request == nil {
				t.Fatalf("account not due: %v", err)
			}
			switch action {
			case "disable":
				_, err = store.PatchAccount(ctx, account.ID, checkinPatch[model.CheckinAccountPatch](t, `{"enabled":false}`))
			case "archive":
				_, err = store.ArchiveSite(ctx, site.ID)
			case "auto off":
				_, err = store.PatchAccount(ctx, account.ID, checkinPatch[model.CheckinAccountPatch](t, `{"auto_checkin":false}`))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.BeginExecution(ctx, account.ID, *request); !errors.Is(err, ErrCheckinUnavailable) && !errors.Is(err, ErrCheckinNotDue) {
				t.Fatalf("dispatched after configuration change: %v", err)
			}
		})
	}
}
