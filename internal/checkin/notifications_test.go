package checkin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/notify"
)

func TestNotificationsRestartDedupAndSafeWebhook(t *testing.T) {
	engine, store, conn, _, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"success":true}`) }, false)
	result, err := engine.Execute(context.Background(), account.ID, model.CheckinTriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	var deliveries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deliveries.Add(1)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "fixture-secret") || strings.Contains(string(body), "access_token") || strings.Contains(string(body), "base_url") {
			t.Error("notification leaked authentication")
		}
		var payload notificationPayload
		if err := json.Unmarshal(body, &payload); err != nil || payload.LogID != result.LogID || payload.Event != "site_checkin_success" || payload.Source != "manual" {
			t.Errorf("invalid notification: %s", body)
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	config := func() NotificationConfig {
		return NotificationConfig{Enabled: true, ManualEnabled: true, SuccessEnabled: true, Cooldown: time.Hour, Channels: notify.Config{WebhookURL: server.URL}}
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := NewNotifications(store, config).RunDue(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if deliveries.Load() != 1 {
		t.Fatalf("delivered %d times", deliveries.Load())
	}
	if _, err := engine.Execute(context.Background(), account.ID, model.CheckinTriggerManual); err != nil {
		t.Fatal(err)
	}
	if err := NewNotifications(store, config).RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := conn.Model(&model.CheckinCompletion{}).Count(&count).Error; err != nil || count != 1 || deliveries.Load() != 1 {
		t.Fatal("replay created another notification", err)
	}
	history, err := store.ListNotifications(context.Background(), 0, 10)
	if err != nil || len(history) != 1 || history[0].State != "delivered" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}

func TestNotificationsRecoverySelectionAndUncertainDelivery(t *testing.T) {
	for _, scenario := range []string{"disabled", "manual_disabled", "low_balance", "failure", "uncertain"} {
		t.Run(scenario, func(t *testing.T) {
			store, conn := sessionStore(t)
			entry := model.CheckinLog{ID: 1, SiteID: 1, AccountID: 1, Source: "batch", SiteName: "site", AccountName: "account", Status: model.SiteExecutionStatusSuccess, Reason: model.SiteCheckinReasonCheckedIn, Reward: "0.25", FinishedAt: time.Now()}
			if scenario == "failure" {
				entry.Status = model.SiteExecutionStatusFailed
				entry.Reason = "authentication_unavailable"
			}
			balance := 0.0
			completion := model.CheckinCompletion{ID: fmt.Sprintf("1:%s", entry.Status), LogID: 1, Status: entry.Status, Entry: entry, Balance: &balance}
			// Simulate a crash between outcome/reward commit and postprocessing.
			completion.Entry.Reward = ""
			if err := conn.Create(&entry).Error; err != nil {
				t.Fatal(err)
			}
			if err := conn.Create(&completion).Error; err != nil {
				t.Fatal(err)
			}
			config := NotificationConfig{Enabled: scenario != "disabled", ManualEnabled: scenario != "manual_disabled", SuccessEnabled: true, Cooldown: time.Hour, Threshold: 1, Channels: notify.Config{WebhookURL: "http://127.0.0.1:1/local-test-only"}}
			service := NewNotifications(store, func() NotificationConfig { return config })
			var calls atomic.Int32
			service.deliver = func(ctx context.Context, _ *http.Client, _ notify.Target, message notify.Message) error {
				calls.Add(1)
				event := message.Payload.(notificationPayload)
				if scenario == "failure" {
					if event.Event != "site_checkin_failed" {
						t.Error("failure priority lost")
					}
				} else if event.Event != "site_checkin_low_balance" || event.Balance == nil || *event.Balance != 0 || event.Reward != "0.25" {
					t.Errorf("event=%+v", event)
				}
				if message.Variables["source"] != "手动签到" {
					t.Error("manual batch mislabeled")
				}
				if scenario == "uncertain" {
					return errors.New("transport failed secret")
				}
				return nil
			}
			for range 2 {
				if err := service.RunDue(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			want := int32(1)
			if scenario == "disabled" || scenario == "manual_disabled" {
				want = 0
			}
			if calls.Load() != want {
				t.Fatalf("calls=%d want=%d", calls.Load(), want)
			}
			if scenario == "uncertain" {
				history, err := store.ListNotifications(context.Background(), 0, 10)
				if err != nil || history[0].State != "uncertain" {
					t.Fatal("ambiguous delivery lost", err)
				}
			}
		})
	}
}

func TestNotificationsDisableQueuedDestination(t *testing.T) {
	store, conn := sessionStore(t)
	entry := model.CheckinCompletion{ID: "1:failed", LogID: 1, Status: model.SiteExecutionStatusFailed, Entry: model.CheckinLog{ID: 1, AccountID: 1, Source: "scheduled", Status: model.SiteExecutionStatusFailed}}
	if err := conn.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	config := NotificationConfig{Enabled: true, Channels: notify.Config{WebhookURL: "http://127.0.0.1:1/test"}}
	for target := range notificationTargets(config) {
		if err := store.ExpandCompletion(context.Background(), entry, "site_checkin_failed", "failure", []string{target}, time.Hour, 0); err != nil {
			t.Fatal(err)
		}
	}
	config.Channels = notify.Config{}
	service := NewNotifications(store, func() NotificationConfig { return config })
	service.deliver = func(context.Context, *http.Client, notify.Target, notify.Message) error {
		t.Error("sent removed destination")
		return nil
	}
	if err := service.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	history, err := store.ListNotifications(context.Background(), 0, 10)
	if err != nil || len(history) != 1 || history[0].State != "canceled" {
		t.Fatal("queue did not honor settings change", err)
	}
}
