package op

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin/finance"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestIndependentCheckinBalanceAndNotificationCAS(t *testing.T) {
	store := independentCheckinStore(t)
	_, account := workflowAccount(t, store)
	testBalanceNotificationCAS(t, store, account.ID)
}

func testBalanceNotificationCAS(t *testing.T, store *CheckinStore, accountID int) {
	t.Helper()
	ctx := context.Background()
	c, err := store.CreateCredential(ctx, testCheckinCredential())
	if err != nil {
		t.Fatal(err)
	}
	patch := checkinPatch[model.CheckinAccountPatch](t, fmt.Sprintf(`{"balance_config":{"mode":"custom","platform":"sub2api","base_url":"https://balance.example","credential_id":%d}}`, c.ID))
	if _, err := store.PatchAccount(ctx, accountID, patch); err != nil {
		t.Fatal(err)
	}
	first, err := store.BeginBalance(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.BeginBalance(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	session := CheckinLoginSession{AccessToken: c.AccessToken}
	if err := store.SaveBalance(ctx, second, finance.Balance{Value: 0}, session); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBalance(ctx, first, finance.Balance{Value: 99}, session); !errors.Is(err, ErrCheckinBalanceChanged) {
		t.Fatalf("old response overwrote new: %v", err)
	}
	third, err := store.BeginBalance(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchAccount(ctx, accountID, checkinPatch[model.CheckinAccountPatch](t, `{"balance_config":null}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchAccount(ctx, accountID, patch); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBalance(ctx, third, finance.Balance{Value: 99}, session); !errors.Is(err, ErrCheckinBalanceChanged) {
		t.Fatalf("ABA configuration change allowed stale response: %v", err)
	}
	current, err := store.BeginBalance(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	candidate := current.Credential
	candidate.AccessToken = "manual-replacement"
	if _, err := store.EditCredential(ctx, candidate, candidate.CredentialVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBalance(ctx, current, finance.Balance{Value: 99}, session); !errors.Is(err, ErrCheckinBalanceChanged) {
		t.Fatalf("identity change allowed stale response: %v", err)
	}
	var wg sync.WaitGroup
	versions := make(chan int64, 12)
	for range 12 {
		wg.Go(func() {
			value, err := store.BeginBalance(ctx, accountID)
			if err != nil {
				t.Error(err)
				return
			}
			versions <- value.Account.BalanceVersion
		})
	}
	wg.Wait()
	close(versions)
	unique := map[int64]bool{}
	for version := range versions {
		if unique[version] {
			t.Fatal("duplicate observation")
		}
		unique[version] = true
	}
	if len(unique) != 12 {
		t.Fatal("missing concurrent observations")
	}
	entry := model.CheckinLog{ID: 9001, AccountID: accountID, SiteID: 1, Source: string(model.CheckinTriggerManual), Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonCheckedIn, FinishedAt: time.Now()}
	completion := model.CheckinCompletion{ID: completionID(entry.ID, entry.Status), LogID: entry.ID, Status: entry.Status, Entry: entry, ReadyAt: 0}
	if err := store.conn.Create(&completion).Error; err != nil {
		t.Fatal(err)
	}
	target := strings.Repeat("a", 64)
	for range 12 {
		wg.Go(func() {
			if err := store.ExpandCompletion(ctx, completion, "site_checkin_success", entry.Reason, []string{target}, time.Hour, 0); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	deliveries, err := store.PendingNotifications(ctx)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("duplicate expansion: %v count=%d", err, len(deliveries))
	}
	owners := make(chan *model.CheckinNotificationDelivery, 12)
	for range 12 {
		wg.Go(func() {
			owner, _, err := store.ClaimNotification(ctx, deliveries[0].ID, true, true)
			if err != nil {
				t.Error(err)
			}
			if owner != nil {
				owners <- owner
			}
		})
	}
	wg.Wait()
	close(owners)
	var owner *model.CheckinNotificationDelivery
	count := 0
	for result := range owners {
		owner = result
		count++
	}
	if count != 1 {
		t.Fatalf("notification owners=%d", count)
	}
	if err := store.FinishNotification(ctx, *owner, true); err != nil {
		t.Fatal(err)
	}
	// Cooldown survives constructing a second store/process.
	entry.ID++
	completion.ID = completionID(entry.ID, entry.Status)
	completion.LogID = entry.ID
	completion.Entry = entry
	completion.Expanded = false
	if err := store.conn.Create(&completion).Error; err != nil {
		t.Fatal(err)
	}
	restarted := NewCheckinStore(store.conn)
	if err := restarted.ExpandCompletion(ctx, completion, "site_checkin_success", entry.Reason, []string{target}, time.Hour, 0); err != nil {
		t.Fatal(err)
	}
	pending, err := restarted.PendingNotifications(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatal("missing second completion", err)
	}
	if owner, _, err := restarted.ClaimNotification(ctx, pending[0].ID, true, true); err != nil || owner != nil {
		t.Fatal("cooldown did not suppress", err)
	}
	history, err := restarted.ListNotifications(ctx, 0, 100)
	if err != nil || len(history) != 2 || history[0].State != "suppressed" || history[1].State != "delivered" {
		t.Fatalf("delivery state lost: %+v %v", history, err)
	}
}

func TestIndependentCheckinNotificationCrashAndExplicitRetry(t *testing.T) {
	store := independentCheckinStore(t)
	ctx := context.Background()
	entry := model.CheckinLog{ID: 1, AccountID: 1, Status: model.SiteExecutionStatusFailed, Reason: "failure", Source: "scheduled"}
	completion := model.CheckinCompletion{ID: "1:failed", LogID: 1, Status: entry.Status, Entry: entry}
	if err := store.conn.Create(&completion).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.ExpandCompletion(ctx, completion, "site_checkin_failed", "failure", []string{strings.Repeat("a", 64)}, time.Hour, 0); err != nil {
		t.Fatal(err)
	}
	pending, _ := store.PendingNotifications(ctx)
	owner, _, err := store.ClaimNotification(ctx, pending[0].ID, true, true)
	if err != nil || owner == nil {
		t.Fatal(err)
	}
	if err := store.conn.Model(owner).Update("started_at", time.Now().Add(-time.Minute).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if pending, err := store.PendingNotifications(ctx); err != nil || len(pending) != 0 {
		t.Fatal("crashed send was replayed", err)
	}
	history, _ := store.ListNotifications(ctx, 0, 10)
	if history[0].State != "uncertain" {
		t.Fatal("lost ambiguous outcome")
	}
	old := *owner
	if err := store.RetryNotification(ctx, owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishNotification(ctx, old, true); !errors.Is(err, ErrCheckinNotificationState) {
		t.Fatal("old worker completed explicit retry", err)
	}
	owner, _, err = store.ClaimNotification(ctx, owner.ID, true, true)
	if err != nil || owner == nil {
		t.Fatal(err)
	}
	if err := store.FinishNotification(ctx, *owner, false); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.PendingNotifications(ctx); err != nil || len(pending) != 0 {
		t.Fatal("failed delivery auto-retried", err)
	}
}

func TestIndependentCheckinHistoryAndRewardStats(t *testing.T) {
	store := independentCheckinStore(t)
	ctx := context.Background()
	site, _ := workflowAccount(t, store)
	now := time.Now().UTC()
	entries := []model.CheckinLog{
		{ID: 1, SiteID: site.ID, SiteName: "old", AccountID: 1, Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonCheckedIn, Reward: "0", FinishedAt: now, Source: "manual"},
		{ID: 2, SiteID: site.ID, SiteName: "old", AccountID: 1, Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonCheckedIn, Reward: "0.25", FinishedAt: now, Source: "manual"},
		{ID: 3, SiteID: 999, SiteName: "deleted", AccountID: 2, Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonAlreadyCheckedIn, Reward: "999", FinishedAt: now, Source: "scheduled"},
		{ID: 4, SiteID: 999, SiteName: "deleted", AccountID: 2, Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonCheckedIn, Reward: "", FinishedAt: now, Source: "scheduled"},
		{ID: 5, SiteID: 999, SiteName: "deleted", AccountID: 2, Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonCheckedIn, Reward: "NaN", FinishedAt: now, Source: "scheduled"},
	}
	if err := store.conn.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}
	stats, err := store.RewardStats(ctx, SiteCheckinLogFilter{})
	if err != nil || stats.TotalReward != .25 || stats.TotalCount != 5 || stats.UnknownRewardCount != 1 || stats.InvalidRewardCount != 1 || stats.BySite[0].SiteName != site.Name || stats.BySite[1].SiteName != "deleted" {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	filtered, err := store.RewardStats(ctx, SiteCheckinLogFilter{Source: "manual"})
	if err != nil || filtered.TotalCount != 2 {
		t.Fatal("stats source filter lost", err)
	}
	page, err := store.ListHistory(ctx, SiteCheckinLogFilter{Limit: 2})
	if err != nil || len(page.Items) != 2 || page.NextBeforeID != 4 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	page, err = store.ListHistory(ctx, SiteCheckinLogFilter{Limit: 2, BeforeID: page.NextBeforeID})
	if err != nil || page.Items[0].ID != 3 {
		t.Fatal("cursor skipped history", err)
	}
}

func TestIndependentCheckinCompletionUsesLatestBalancePolicy(t *testing.T) {
	store := independentCheckinStore(t)
	ctx := context.Background()
	entry := model.CheckinLog{ID: 1, AccountID: 1, Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonCheckedIn}
	completion := model.CheckinCompletion{ID: "1:success", LogID: 1, Status: entry.Status, Entry: entry}
	if err := store.conn.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.conn.Create(&completion).Error; err != nil {
		t.Fatal(err)
	}
	before, err := store.DueCompletions(ctx)
	if err != nil || len(before) != 1 {
		t.Fatal(err)
	}
	zero := 0.0
	result := &model.CheckinResult{LogID: 1, CheckinOutcome: model.CheckinOutcome{Status: model.SiteExecutionStatusSuccess}}
	if err := store.ReadyCompletion(ctx, result, &zero); err != nil {
		t.Fatal(err)
	}
	if err := store.ExpandCompletion(ctx, before[0], "site_checkin_success", "checked_in", []string{strings.Repeat("a", 64)}, time.Hour, 1); !errors.Is(err, ErrCheckinNotificationState) {
		t.Fatalf("stale policy snapshot expanded: %v", err)
	}
}
