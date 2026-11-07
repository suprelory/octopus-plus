package sitesync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/notify"
)

func TestScheduledCheckinNotificationsUsePersistedSanitizedOutcome(t *testing.T) {
	ctx := setupProjectTestDB(t)
	received := make(chan checkinNotification, 4)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Error("invalid webhook request")
		}
		var event checkinNotification
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		received <- event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sink.Close()
	n := newCheckinNotifier(func() checkinNotificationConfig {
		return checkinNotificationConfig{enabled: true, channels: notify.Config{WebhookURL: sink.URL}, cooldown: time.Hour}
	})
	previous := checkinNotifications
	checkinNotifications = n
	t.Cleanup(func() { n.wg.Wait(); checkinNotifications = previous })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"invalid checkin-secret-value"}`))
	}))
	defer upstream.Close()
	_, account := createCheckinFixture(t, ctx, upstream.URL)
	if result, err := CheckinAccount(ctx, account.ID); err == nil || result == nil {
		t.Fatal("expected manual failure")
	}
	n.wg.Wait()
	if len(received) != 0 {
		t.Fatal("manual check-in sent a notification")
	}
	for i := 0; i < 2; i++ {
		due := time.Now().Add(-time.Minute)
		if err := db.GetDB().Model(account).Update("next_auto_checkin_at", due).Error; err != nil {
			t.Fatal(err)
		}
		result, err := checkinAccountWithTrigger(ctx, account.ID, SiteBatchTriggerScheduled)
		if err == nil || result == nil || result.LogID == 0 {
			t.Fatalf("missing scheduled failure: %+v, %v", result, err)
		}
		n.wg.Wait()
	}
	if len(received) != 1 {
		t.Fatalf("cooldown did not coalesce repeated failures: %+v", received)
	}
	event := <-received
	if event.Event != "site_checkin_failed" || event.AccountID != account.ID || event.FailureCount != 2 || strings.Contains(event.Message, account.AccessToken) {
		t.Fatalf("incorrect or unsafe failure notification: %+v", event)
	}
	var entry model.SiteCheckinLog
	if err := db.GetDB().First(&entry, event.LogID).Error; err != nil || event.Message != entry.Message || entry.Source != "scheduled" {
		t.Fatalf("notification does not reference a persisted log: %+v, %v", entry, err)
	}
}

func TestLowBalanceNotificationRequiresFreshBalance(t *testing.T) {
	events := make(chan checkinNotification, 4)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event checkinNotification
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		events <- event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sink.Close()
	config := checkinNotificationConfig{enabled: true, channels: notify.Config{WebhookURL: sink.URL}, threshold: 1}
	n := newCheckinNotifier(func() checkinNotificationConfig { return config })
	site := &model.Site{ID: 1, Name: "Site"}
	account := &model.SiteAccount{ID: 2, Name: "Account", Balance: 0}
	result := &model.SiteCheckinResult{LogID: 3, Status: model.SiteExecutionStatusSuccess}
	n.notify(site, account, result, siteBalanceFetchResult{}, SiteBatchTriggerScheduled)
	n.notify(site, account, result, siteBalanceFetchResult{ok: true, balance: 1}, SiteBatchTriggerScheduled)
	n.wg.Wait()
	if len(events) != 0 {
		t.Fatal("stale balance or threshold equality triggered an alert")
	}
	n.notify(site, account, result, siteBalanceFetchResult{ok: true, balance: 0}, SiteBatchTriggerScheduled)
	n.wg.Wait()
	if len(events) != 1 {
		t.Fatal("a real zero balance was not reported")
	}
	event := <-events
	if event.Event != "site_checkin_low_balance" || event.Balance == nil || *event.Balance != 0 || *event.Threshold != 1 {
		t.Fatalf("incorrect balance notification: %+v", event)
	}
	config.threshold = 0
	n.notify(site, account, result, siteBalanceFetchResult{ok: true, balance: -1}, SiteBatchTriggerScheduled)
	n.wg.Wait()
	config.enabled = false
	result.Status = model.SiteExecutionStatusFailed
	n.notify(site, account, result, siteBalanceFetchResult{}, SiteBatchTriggerScheduled)
	n.wg.Wait()
	if len(events) != 0 {
		t.Fatal("disabled notifications were sent")
	}
}

func TestCheckinNotificationDeliveryIsAsyncCoalescedAndRetriable(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sink.Close()
	defer unblock()
	n := newCheckinNotifier(nil)
	config := checkinNotificationConfig{enabled: true, channels: notify.Config{WebhookURL: sink.URL}, cooldown: time.Hour}
	event := checkinNotification{AccountID: 1, Event: "site_checkin_failed", Reason: "upstream_http_error"}
	if !n.enqueue(config, event) {
		t.Fatal("first event was not queued")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("delivery did not start")
	}
	var callers sync.WaitGroup
	var duplicates atomic.Int32
	for i := 0; i < 20; i++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			if n.enqueue(config, event) {
				duplicates.Add(1)
			}
		}()
	}
	callers.Wait()
	if duplicates.Load() != 0 {
		t.Fatal("concurrent duplicate was queued")
	}
	unblock()
	n.wg.Wait()
	if !n.enqueue(config, event) {
		t.Fatal("failed delivery consumed the cooldown")
	}
	n.wg.Wait()
	if calls.Load() != 2 || n.enqueue(config, event) {
		t.Fatal("successful delivery did not start cooldown")
	}
	// Expired entries are pruned; no timer or unbounded retry loop is needed.
	n.mu.Lock()
	for key := range n.recent {
		n.recent[key] = checkinNotificationReservation{until: time.Now().Add(-time.Second)}
	}
	n.mu.Unlock()
	if !n.enqueue(config, event) {
		t.Fatal("expired cooldown blocked delivery")
	}
	n.wg.Wait()
}

func TestCheckinNotificationDoesNotFollowRedirectsAndTimesOut(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	n := newCheckinNotifier(nil)
	if err := n.deliver((notify.Config{WebhookURL: redirect.URL}).Targets()[0], checkinNotification{}); err == nil || redirected.Load() != 0 {
		t.Fatal("webhook followed a redirect")
	}
	ctx, cancel := context.WithCancel(context.Background())
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-ctx.Done():
		}
	}))
	defer slow.Close()
	defer cancel()
	n.client.Timeout = 20 * time.Millisecond
	if err := n.deliver((notify.Config{WebhookURL: slow.URL}).Targets()[0], checkinNotification{}); err == nil {
		t.Fatal("slow webhook did not time out")
	}
}

func TestCheckinNotificationChannelsHaveIndependentCooldowns(t *testing.T) {
	var webhookCalls, barkCalls, replacementCalls atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/webhook":
			webhookCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case "/bark":
			if barkCalls.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"code":500,"message":"rejected"}`))
			} else {
				_, _ = w.Write([]byte(`{"code":200}`))
			}
		case "/replacement":
			replacementCalls.Add(1)
			_, _ = w.Write([]byte(`{"code":200}`))
		default:
			t.Error("unexpected destination")
		}
	}))
	defer sink.Close()
	n := newCheckinNotifier(nil)
	config := checkinNotificationConfig{channels: notify.Config{WebhookURL: sink.URL + "/webhook", BarkURL: sink.URL + "/bark"}, cooldown: time.Hour}
	event := checkinNotification{AccountID: 1, Event: "site_checkin_failed", Reason: "upstream_http_error"}
	if !n.enqueue(config, event) {
		t.Fatal("initial notification was not queued")
	}
	n.wg.Wait()
	if !n.enqueue(config, event) {
		t.Fatal("failed channel could not retry")
	}
	n.wg.Wait()
	if webhookCalls.Load() != 1 || barkCalls.Load() != 2 || n.enqueue(config, event) {
		t.Fatal("partial failure resent a successful channel or failed to start cooldown")
	}
	config.channels.BarkURL = sink.URL + "/replacement"
	if !n.enqueue(config, event) {
		t.Fatal("changed destination inherited the old cooldown")
	}
	n.wg.Wait()
	if webhookCalls.Load() != 1 || replacementCalls.Load() != 1 {
		t.Fatal("changing one channel affected another channel")
	}
}

func TestCheckinNotificationResultPolicy(t *testing.T) {
	events := make(chan checkinNotification, 4)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event checkinNotification
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		events <- event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sink.Close()
	for _, tc := range []struct {
		name                     string
		enabled, success, manual bool
		trigger                  SiteBatchTrigger
		status                   model.SiteExecutionStatus
		reason                   string
		logID                    int64
		balance                  siteBalanceFetchResult
		want                     string
	}{
		{"disabled", false, true, true, SiteBatchTriggerScheduled, model.SiteExecutionStatusFailed, "", 1, siteBalanceFetchResult{}, ""},
		{"manual defaults off", true, false, false, SiteBatchTriggerManual, model.SiteExecutionStatusFailed, "", 1, siteBalanceFetchResult{}, ""},
		{"manual failure enabled", true, false, true, SiteBatchTriggerManual, model.SiteExecutionStatusFailed, "", 1, siteBalanceFetchResult{}, "site_checkin_failed"},
		{"success defaults off", true, false, false, SiteBatchTriggerScheduled, model.SiteExecutionStatusSuccess, model.SiteCheckinReasonCheckedIn, 1, siteBalanceFetchResult{}, ""},
		{"scheduled success", true, true, false, SiteBatchTriggerScheduled, model.SiteExecutionStatusSuccess, model.SiteCheckinReasonCheckedIn, 1, siteBalanceFetchResult{}, "site_checkin_success"},
		{"manual success", true, true, true, SiteBatchTriggerManual, model.SiteExecutionStatusSuccess, model.SiteCheckinReasonCheckedIn, 1, siteBalanceFetchResult{}, "site_checkin_success"},
		{"already checked in", true, true, false, SiteBatchTriggerScheduled, model.SiteExecutionStatusSuccess, model.SiteCheckinReasonAlreadyCheckedIn, 1, siteBalanceFetchResult{}, "site_checkin_success"},
		{"low balance takes priority", true, true, true, SiteBatchTriggerManual, model.SiteExecutionStatusSuccess, model.SiteCheckinReasonCheckedIn, 1, siteBalanceFetchResult{ok: true, balance: 0}, "site_checkin_low_balance"},
		{"skipped", true, true, true, SiteBatchTriggerManual, model.SiteExecutionStatusSkipped, model.SiteCheckinReasonAlreadyRunning, 1, siteBalanceFetchResult{}, ""},
		{"not persisted", true, true, true, SiteBatchTriggerManual, model.SiteExecutionStatusFailed, "", 0, siteBalanceFetchResult{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newCheckinNotifier(func() checkinNotificationConfig {
				return checkinNotificationConfig{
					enabled: tc.enabled, successEnabled: tc.success, manualEnabled: tc.manual,
					channels: notify.Config{WebhookURL: sink.URL}, threshold: 1,
				}
			})
			result := &model.SiteCheckinResult{LogID: tc.logID, Status: tc.status, Reason: tc.reason, Message: "persisted result", Reward: "2.5"}
			n.notify(&model.Site{ID: 1, Name: "Site"}, &model.SiteAccount{ID: 2, Name: "Account"}, result, tc.balance, tc.trigger)
			n.wg.Wait()
			if tc.want == "" {
				if len(events) != 0 {
					t.Fatal("unexpected notification")
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("expected one notification, got %d", len(events))
			}
			event := <-events
			if event.Event != tc.want || event.Source != tc.trigger || event.Message != result.Message || event.Reward != result.Reward || event.Level == "" || event.Title == "" {
				t.Fatalf("incorrect result notification: %+v", event)
			}
		})
	}
}

func TestManualCheckinNotificationOptInUsesPersistedResult(t *testing.T) {
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
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/checkin" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"message":"signed checkin-secret-value","data":{"reward":2.5}}`))
	}))
	defer upstream.Close()
	_, account := createCheckinFixture(t, ctx, upstream.URL)
	result, err := CheckinAccount(ctx, account.ID)
	if err != nil || result == nil || result.LogID == 0 {
		t.Fatalf("manual check-in failed: %+v, %v", result, err)
	}
	n.wg.Wait()
	if len(events) != 1 {
		t.Fatal("manual result was not delivered")
	}
	event := <-events
	var entry model.SiteCheckinLog
	if err := db.GetDB().First(&entry, event.LogID).Error; err != nil || event.Source != SiteBatchTriggerManual || event.Message != entry.Message || event.Reward != entry.Reward || strings.Contains(event.Message, account.AccessToken) {
		t.Fatalf("manual notification did not use the sanitized persisted result: %+v, %v", event, err)
	}
}
