package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/notify"
	"github.com/suprelory/octopus-plus/internal/op"
)

func (f *checkinAPIFixture) waitBatch(t *testing.T, id int64) model.CheckinBatchJob {
	t.Helper()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		job := decodeCheckinAPI[model.CheckinBatchJob](t, f.request(t, "GET", fmt.Sprintf("/batches/%d", id), nil, 200))
		if job.FinishedAt != nil {
			return job
		}
		select {
		case <-deadline:
			t.Fatal("batch did not finish")
		case <-ticker.C:
		}
	}
}

func (f *checkinAPIFixture) waitNotifications(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.services.Notifications.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCheckinAPIBatchesAndConcurrentExecution(t *testing.T) {
	started, unblock := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	var submits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if submits.Add(1) == 1 {
			close(started)
		}
		select {
		case <-unblock:
			fmt.Fprint(w, `{"success":true}`)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	f := newCheckinAPIFixture(t)
	site := f.site(t, server.URL, false)
	account := f.account(t, site)
	job := decodeCheckinAPI[model.CheckinBatchJob](t, f.request(t, "POST", "/batches", `{"scope":"all"}`, 200))
	t.Cleanup(func() { release(); f.waitBatch(t, job.ID) })
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not start its detached worker")
	}
	reused := decodeCheckinAPI[model.CheckinBatchJob](t, f.request(t, "POST", "/batches", `{"scope":"all"}`, 200))
	if reused.ID != job.ID || reused.Status != model.SiteCheckinBatchJobStatusRunning {
		t.Fatal("same-scope active batch was not reused")
	}
	f.request(t, "POST", "/batches", fmt.Sprintf(`{"scope":"selected","site_ids":[%d]}`, site.ID), 409, "checkin.batch_active")
	f.request(t, "POST", fmt.Sprintf("/accounts/%d/execute", account.ID), nil, 409, "checkin.already_running")
	f.request(t, "POST", fmt.Sprintf("/accounts/%d/confirm", account.ID), nil, 409, "checkin.already_running")
	release()
	finished := f.waitBatch(t, job.ID)
	if finished.Status != model.SiteCheckinBatchJobStatusCompleted || finished.Total != 1 || finished.Success != 1 || submits.Load() != 1 {
		t.Fatalf("unexpected batch result: %+v, submissions=%d", finished, submits.Load())
	}
	logs := decodeCheckinAPI[checkinPage[checkinLogResponse]](t, f.request(t, "GET", fmt.Sprintf("/logs?source=batch&batch_id=%d&account_id=%d", job.ID, account.ID), nil, 200))
	if len(logs.Items) != 1 || logs.Items[0].BatchJobID != fmt.Sprint(job.ID) {
		t.Fatal("batch history did not retain the batch ID and source")
	}
}

func TestCheckinAPILongIDPagination(t *testing.T) {
	f := newCheckinAPIFixture(t)
	const older int64 = 9007199254740993
	const newer int64 = older + 2
	now := time.Now().UTC()
	for _, id := range []int64{older, newer} {
		for _, row := range []any{
			&model.CheckinLog{ID: id, SiteID: 1, AccountID: 1, Platform: model.SitePlatformAPI, Source: "batch", Status: model.SiteExecutionStatusSuccess, BatchJobID: id, StartedAt: now, FinishedAt: now},
			&model.CheckinBatchJob{ID: id, Status: model.SiteCheckinBatchJobStatusCompleted, Trigger: "manual", StartedAt: now, UpdatedAt: now, FinishedAt: &now},
			&model.CheckinNotificationDelivery{ID: id, CompletionID: fmt.Sprintf("%d:success", id), Destination: strings.Repeat("a", 64), CooldownKey: fmt.Sprint(id), Event: "site_checkin_success", State: "delivered"},
		} {
			if err := f.conn.Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	type page struct {
		Items []struct {
			ID      string `json:"id"`
			BatchID string `json:"batch_job_id"`
		} `json:"items"`
		Next string `json:"next_before_id"`
	}
	for _, path := range []string{"/logs", "/batches", "/notifications"} {
		first := decodeCheckinAPI[page](t, f.request(t, "GET", path+"?limit=1", nil, 200))
		if len(first.Items) != 1 || first.Items[0].ID != fmt.Sprint(newer) || first.Next != fmt.Sprint(newer) {
			t.Fatalf("%s lost ID precision or cursor: %+v", path, first)
		}
		if path == "/logs" && first.Items[0].BatchID != fmt.Sprint(newer) {
			t.Fatal("history lost batch ID precision")
		}
		last := decodeCheckinAPI[page](t, f.request(t, "GET", path+"?limit=1&before_id="+first.Next, nil, 200))
		if len(last.Items) != 1 || last.Items[0].ID != fmt.Sprint(older) || last.Next != "" {
			t.Fatalf("%s last page incorrect: %+v", path, last)
		}
		empty := decodeCheckinAPI[page](t, f.request(t, "GET", fmt.Sprintf("%s?before_id=%d", path, older), nil, 200))
		if empty.Items == nil || len(empty.Items) != 0 || empty.Next != "" {
			t.Fatalf("%s empty page incorrect: %+v", path, empty)
		}
	}
	f.request(t, "GET", fmt.Sprintf("/batches/%d", newer), nil, 200)
}

func TestCheckinAPINotificationRetryIsExplicit(t *testing.T) {
	var deliveries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		if deliveries.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	t.Cleanup(server.Close)
	f := newCheckinAPIFixtureWithConfig(t, func() checkin.NotificationConfig {
		return checkin.NotificationConfig{Enabled: true, ManualEnabled: true, SuccessEnabled: true, Channels: notify.Config{WebhookURL: server.URL + "/local-webhook-secret"}}
	})
	site := f.site(t, server.URL, false)
	account := f.account(t, site)
	f.request(t, "POST", fmt.Sprintf("/accounts/%d/execute", account.ID), nil, 200)
	f.waitNotifications(t)
	raw := f.request(t, "GET", "/notifications", nil, 200)
	if strings.Contains(string(raw), "local-webhook-secret") || strings.Contains(string(raw), "api-fixture-secret") {
		t.Fatalf("notification history leaked a secret: %s", raw)
	}
	page := decodeCheckinAPI[model.CheckinNotificationPage](t, raw)
	if len(page.Items) != 1 || page.Items[0].State != "uncertain" || page.Items[0].Attempts != 1 || deliveries.Load() != 1 {
		t.Fatalf("unknown delivery state was lost: %+v", page.Items)
	}
	delivery := page.Items[0]
	path := fmt.Sprintf("/notifications/%d/retry", delivery.ID)
	f.request(t, "POST", path, nil, 409, "checkin.notification_state")
	if err := f.services.Notifications.RunDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if deliveries.Load() != 1 {
		t.Fatal("uncertain notification retried automatically")
	}
	if err := f.conn.Model(&delivery).Update("started_at", time.Now().Add(-op.CheckinNotificationSendTimeout-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", path, nil, 200)
	f.waitNotifications(t)
	page = decodeCheckinAPI[model.CheckinNotificationPage](t, f.request(t, "GET", "/notifications", nil, 200))
	if page.Items[0].State != "delivered" || page.Items[0].Attempts != 2 || deliveries.Load() != 2 {
		t.Fatalf("explicit retry did not finish: %+v", page.Items)
	}
	f.request(t, "POST", path, nil, 409, "checkin.notification_state")
	f.request(t, "POST", "/notifications/9223372036854775807/retry", nil, 404, "checkin.not_found")
}

func TestCheckinAPIBalanceFailureDoesNotUndoCheckin(t *testing.T) {
	var submits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submits.Add(1)
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		w.WriteHeader(503)
		fmt.Fprint(w, `{"message":"upstream-balance-secret"}`)
	}))
	t.Cleanup(server.Close)
	f := newCheckinAPIFixture(t)
	site := f.site(t, server.URL, false)
	account := f.account(t, site)
	credential := decodeCheckinAPI[model.CheckinCredentialSummary](t, f.request(t, "POST", "/credentials", map[string]any{"name": "balance", "type": "access_token", "platform": "sub2api", "auth_base_url": server.URL, "access_token": "balance-secret"}, 200))
	f.request(t, "PATCH", fmt.Sprintf("/accounts/%d", account.ID), map[string]any{"balance_config": map[string]any{"mode": "custom", "platform": "sub2api", "base_url": server.URL, "credential_id": credential.ID}}, 200)
	result := decodeCheckinAPI[model.CheckinResult](t, f.request(t, "POST", fmt.Sprintf("/accounts/%d/execute", account.ID), nil, 200))
	if result.Status != model.SiteExecutionStatusSuccess || submits.Load() != 1 {
		t.Fatal("balance failure overwrote successful check-in")
	}
	f.request(t, "POST", fmt.Sprintf("/accounts/%d/balance", account.ID), nil, 502, "checkin.balance_unavailable")
	result = decodeCheckinAPI[model.CheckinResult](t, f.request(t, "POST", fmt.Sprintf("/accounts/%d/execute", account.ID), nil, 200))
	if !result.Replayed || submits.Load() != 1 {
		t.Fatal("balance failure caused a repeated check-in submission")
	}
}

func TestCheckinAPIUpstreamAuthenticationErrors(t *testing.T) {
	for _, test := range []struct {
		name, code string
		challenge  bool
	}{
		{"expired session", "checkin.session_expired", false},
		{"verification", "checkin.verification_required", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if test.challenge {
					w.Header().Set("Cf-Mitigated", "challenge")
				}
				w.WriteHeader(401)
				fmt.Fprint(w, `{"message":"do-not-return-upstream-secret"}`)
			}))
			t.Cleanup(server.Close)
			f := newCheckinAPIFixture(t)
			site := f.site(t, server.URL, false)
			account := f.account(t, site)
			credential := decodeCheckinAPI[model.CheckinCredentialSummary](t, f.request(t, "POST", "/credentials", map[string]any{"name": "balance", "type": "access_token", "platform": "sub2api", "auth_base_url": server.URL, "access_token": "expired-token"}, 200))
			f.request(t, "PATCH", fmt.Sprintf("/accounts/%d", account.ID), map[string]any{"balance_config": map[string]any{"mode": "custom", "platform": "sub2api", "base_url": server.URL, "credential_id": credential.ID}}, 200)
			f.request(t, "POST", fmt.Sprintf("/accounts/%d/balance", account.ID), nil, 409, test.code)
			if requests.Load() != 1 {
				t.Fatal("balance recovery sent an unauthorized login or check-in")
			}
		})
	}
}
