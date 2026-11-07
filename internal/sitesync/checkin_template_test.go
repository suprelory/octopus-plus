package sitesync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/notify"
)

func TestCheckinTemplateUsesEventDetails(t *testing.T) {
	var received checkinNotification
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sink.Close()
	config := notify.Config{WebhookURL: sink.URL, Templates: map[notify.Kind]notify.Template{notify.Webhook: {
		Title: "{{emoji}} {{site}} / {{event}}", Body: "{{account}}|{{source}}|{{detail}}|{{reward}}|{{balance}}|{{threshold}}|{{failure_count}}|{{time}}",
	}}}
	n := newCheckinNotifier(nil)
	balance, threshold := 0.0, 5.0
	event := checkinNotification{Event: "site_checkin_low_balance", Title: "余额不足", Level: "warning", SiteName: "站点", AccountName: "账号", Source: SiteBatchTriggerManual, Message: "签到成功", Reward: "0.50 USD", Balance: &balance, Threshold: &threshold, OccurredAt: time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC), AccountID: 7}
	if err := n.deliver(config.Targets()[0], event); err != nil {
		t.Fatal(err)
	}
	if received.Title != "⚠️ 站点 / 余额不足" || received.Message != "账号|手动签到|签到成功|0.50 USD|0.0000|5.0000|0|2026-01-02T04:00:00+08:00" {
		t.Fatalf("event fields missing: %+v", received)
	}
	if !received.OccurredAt.Equal(event.OccurredAt) || received.OccurredAt.Format(time.RFC3339) != "2026-01-02T04:00:00+08:00" {
		t.Fatalf("unexpected notification timestamp: %s", received.OccurredAt.Format(time.RFC3339))
	}
	if received.Event != event.Event || received.AccountID != 7 || received.Balance == nil || *received.Balance != 0 {
		t.Fatalf("structured metadata changed: %+v", received)
	}
}
