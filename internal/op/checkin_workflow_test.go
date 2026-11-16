package op

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func workflowAccount(t *testing.T, store *CheckinStore) (*model.CheckinSite, *model.CheckinAccount) {
	t.Helper()
	ctx := context.Background()
	site, err := store.CreateSite(ctx, model.CheckinSite{Name: "workflow", Platform: model.SitePlatformAPI, BaseURL: "https://checkin.example",
		Enabled: true, CheckinHTTPEnabled: true, CheckinHTTPPath: "/submit", CheckinTimezone: "Asia/Shanghai",
		CheckinStatus: &model.SiteCheckinStatusConfig{Path: "/status", CheckedPath: "checked"}})
	if err != nil {
		t.Fatal(err)
	}
	c := model.CheckinCredential{Name: "cookie", Type: model.CheckinCredentialCookie, Platform: model.SitePlatformAPI, AuthBaseURL: site.BaseURL, Cookie: "session=fixture"}
	account, err := store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: "account", Enabled: true, AuthMode: model.CheckinAuthCookie}, &c)
	if err != nil {
		t.Fatal(err)
	}
	return site, account
}

func TestIndependentCheckinWorkflowReservationAndConfirmationCAS(t *testing.T) {
	store := independentCheckinStore(t)
	_, account := workflowAccount(t, store)
	testWorkflowReservationCAS(t, store, account.ID)
}

func testWorkflowReservationCAS(t *testing.T, store *CheckinStore, accountID int) {
	t.Helper()
	ctx := context.Background()
	first, err := store.BeginWorkflow(ctx, accountID, model.CheckinTriggerManual, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.BeginWorkflow(ctx, accountID, model.CheckinTriggerBatch, false)
	if err != nil || second.Workflow().LogID != first.Workflow().LogID || second.Account.CheckinWorkflowVersion != first.Account.CheckinWorkflowVersion {
		t.Fatalf("concurrent entry created a new reservation: %v", err)
	}
	var wg sync.WaitGroup
	winners := make(chan *CheckinExecution, 2)
	for _, snapshot := range []*CheckinExecution{first, second} {
		wg.Go(func() {
			reserved, err := store.ReserveWorkflowSubmission(ctx, snapshot, true)
			if err != nil && !errors.Is(err, ErrCheckinWorkflowConflict) {
				t.Errorf("reserve failed: %v", err)
			}
			if reserved != nil {
				winners <- reserved
			}
		})
	}
	wg.Wait()
	close(winners)
	var winner *CheckinExecution
	count := 0
	for value := range winners {
		winner, count = value, count+1
	}
	if count != 1 || !winner.Workflow().Submitted || !winner.Workflow().BeforeUnchecked {
		t.Fatalf("submission owners=%d", count)
	}
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := store.FinishWorkflow(ctx, winner, model.CheckinOutcome{Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonCheckedIn, Message: "confirmed", Reward: "0"})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	writes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			writes++
		} else if errors.Is(err, ErrCheckinWorkflowConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if writes != 1 || conflicts != 1 {
		t.Fatalf("confirmation writes=%d conflicts=%d", writes, conflicts)
	}
	result, err := store.WorkflowResult(ctx, accountID)
	if err != nil || result.Reward != "0" || result.Status != model.SiteExecutionStatusSuccess || !result.Replayed {
		t.Fatalf("workflow replay lost outcome or zero reward: %v", err)
	}
	var logs int64
	if err := store.conn.Model(&model.CheckinLog{}).Where("account_id = ?", accountID).Count(&logs).Error; err != nil || logs != 1 {
		t.Fatalf("duplicate history: count=%d err=%v", logs, err)
	}
	stored, err := store.GetAccount(ctx, accountID)
	if err != nil || stored.LastCheckinSuccessAt == nil || stored.CheckinFailureCount != 0 || !stored.CheckinPending.Finished {
		t.Fatalf("account outcome not atomic with log: %v", err)
	}
}

func TestIndependentCheckinWorkflowSurvivesMissingHistoryAndInvalidation(t *testing.T) {
	store := independentCheckinStore(t)
	site, account := workflowAccount(t, store)
	ctx := context.Background()
	started, err := store.BeginWorkflow(ctx, account.ID, model.CheckinTriggerManual, false)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := store.ReserveWorkflowSubmission(ctx, started, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.conn.Delete(&model.CheckinLog{}, reserved.Workflow().LogID).Error; err != nil {
		t.Fatal(err)
	}
	restored, err := store.BeginWorkflow(ctx, account.ID, model.CheckinTriggerManual, true)
	if err != nil || !restored.Workflow().Submitted || restored.Workflow().LogID != reserved.Workflow().LogID {
		t.Fatalf("missing log cleared submitted reservation: %v", err)
	}
	if _, err := store.ReserveWorkflowSubmission(ctx, restored, true); !errors.Is(err, ErrCheckinWorkflowConflict) {
		t.Fatalf("restored workflow could submit again: %v", err)
	}
	if _, err := store.PatchSite(ctx, site.ID, checkinPatch[model.CheckinSitePatch](t, `{"checkin_http_path":"/changed"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckWorkflow(ctx, restored); !errors.Is(err, ErrCheckinWorkflowChanged) {
		t.Fatalf("changed endpoint retained workflow authorization: %v", err)
	}
	if _, err := store.FinishWorkflow(ctx, restored, model.CheckinOutcome{Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonCheckedIn}); !errors.Is(err, ErrCheckinWorkflowChanged) {
		t.Fatalf("stale result crossed a configuration edit: %v", err)
	}
	stored, err := store.GetAccount(ctx, account.ID)
	if err != nil || !stored.CheckinPending.Submitted || stored.CheckinPending.Fingerprint != reserved.Workflow().Fingerprint {
		t.Fatalf("invalidation erased or reauthorized old submission: %v", err)
	}
}

func TestIndependentCheckinWorkflowAvailabilityAndScheduleRechecked(t *testing.T) {
	store := independentCheckinStore(t)
	site, account := workflowAccount(t, store)
	ctx := context.Background()
	if _, err := store.BeginWorkflow(ctx, account.ID, model.CheckinTriggerManual, true); !errors.Is(err, ErrCheckinNotSubmitted) {
		t.Fatalf("confirmation created a workflow: %v", err)
	}
	if _, err := store.BeginWorkflow(ctx, account.ID, model.CheckinTriggerScheduled, false); !errors.Is(err, ErrCheckinNotDue) {
		t.Fatalf("scheduler ignored automatic checkin setting: %v", err)
	}
	started, err := store.BeginWorkflow(ctx, account.ID, model.CheckinTriggerManual, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ArchiveSite(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveWorkflowSubmission(ctx, started, true); !errors.Is(err, ErrCheckinUnavailable) {
		t.Fatalf("archived site submitted after precheck: %v", err)
	}
	if _, err := store.RestoreSite(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchAccount(ctx, account.ID, checkinPatch[model.CheckinAccountPatch](t, `{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckWorkflow(ctx, started); !errors.Is(err, ErrCheckinUnavailable) {
		t.Fatalf("disabled account continued executing: %v", err)
	}
}

func TestIndependentCheckinUnsubmittedWorkflowCanRestart(t *testing.T) {
	store := independentCheckinStore(t)
	_, account := workflowAccount(t, store)
	ctx := context.Background()
	first, err := store.BeginWorkflow(ctx, account.ID, model.CheckinTriggerManual, false)
	if err != nil {
		t.Fatal(err)
	}
	first.Workflow().Deadline = time.Now().Add(-time.Hour)
	if err := store.conn.Select("checkin_pending").Updates(&first.Account).Error; err != nil {
		t.Fatal(err)
	}
	second, err := store.BeginWorkflow(ctx, account.ID, model.CheckinTriggerManual, false)
	if err != nil || second.Workflow().LogID == first.Workflow().LogID || second.Workflow().Submitted {
		t.Fatalf("expired unsubmitted workflow did not restart: %v", err)
	}
	if _, err := store.ReserveWorkflowSubmission(ctx, first, true); !errors.Is(err, ErrCheckinWorkflowConflict) {
		t.Fatalf("superseded precheck retained submission authority: %v", err)
	}
	var oldLog model.CheckinLog
	if err := store.conn.First(&oldLog, first.Workflow().LogID).Error; err != nil || oldLog.Status != model.SiteExecutionStatusFailed {
		t.Fatalf("replaced precheck history was lost: %v", err)
	}
}
