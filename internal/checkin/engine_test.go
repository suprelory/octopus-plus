package checkin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"gorm.io/gorm"
)

func httpWorkflow(t *testing.T, handler http.HandlerFunc, status bool) (*Engine, *op.CheckinStore, *gorm.DB, *model.CheckinSite, *model.CheckinAccount) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store, conn := sessionStore(t)
	site := model.CheckinSite{Name: "HTTP site", Platform: model.SitePlatformAPI, BaseURL: server.URL,
		Enabled: true, CheckinHTTPEnabled: true, CheckinHTTPPath: "/submit", CheckinTimezone: "Asia/Shanghai",
		CheckinHTTPBody: `{"username":"{{username}}","cookie":"{{cookie}}","date":"{{checkin_date}}","password":"{{password}}"}`,
		CustomHeader:    []model.CustomHeader{{HeaderKey: "Cookie", HeaderValue: "stale-cookie"}, {HeaderKey: "Authorization", HeaderValue: "stale-token"}}}
	if status {
		site.CheckinStatus = &model.SiteCheckinStatusConfig{Path: "/status", CheckedPath: "data.checked", MaxAttempts: 1, PollIntervalMs: 1, TimeoutSeconds: 5, RewardSource: "status"}
	}
	created, err := store.CreateSite(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	c := model.CheckinCredential{Name: "cookie", Platform: model.SitePlatformAPI, Type: model.CheckinCredentialCookie,
		AuthBaseURL: server.URL, Cookie: "session=fixture-secret", Username: "a\"b\\c"}
	account, err := store.CreateAccount(context.Background(), model.CheckinAccount{SiteID: created.ID, Name: "HTTP account", Enabled: true, AuthMode: model.CheckinAuthCookie}, &c)
	if err != nil {
		t.Fatal(err)
	}
	sessions := testSessions(store, NewHTTPAuthenticator(store))
	return NewEngine(store, sessions, store), store, conn, created, account
}

func TestWorkflowHTTPSubmissionConfirmationAndRewardReplay(t *testing.T) {
	var submits, queries atomic.Int32
	engine, store, conn, _, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "session=fixture-secret" || r.Header.Get("Authorization") != "" {
			t.Error("execution credential did not override stale shared headers")
		}
		switch r.URL.Path {
		case "/status":
			queries.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"checked": submits.Load() > 0, "reward": 0}})
		case "/submit":
			submits.Add(1)
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["username"] != "a\"b\\c" || body["cookie"] != "session=fixture-secret" || body["password"] != "" || body["date"] == "" {
				t.Error("checkin template failed JSON escaping or secret selection")
			}
			// Submission itself is ambiguous; the status endpoint is authoritative.
			_, _ = w.Write([]byte(`{"success":false,"message":"fixture-secret"}`))
		default:
			http.NotFound(w, r)
		}
	}, true)
	ctx := context.Background()
	result, err := engine.Execute(ctx, account.ID, model.CheckinTriggerManual)
	if err != nil || result.Status != model.SiteExecutionStatusSuccess || result.Reason != model.SiteCheckinReasonCheckedIn || result.Reward != "0" || !result.Submitted || result.Replayed {
		t.Fatalf("custom checkin failed: %+v %v", result, err)
	}
	for _, action := range []func() (*model.CheckinResult, error){
		func() (*model.CheckinResult, error) {
			return engine.Execute(ctx, account.ID, model.CheckinTriggerBatch)
		},
		func() (*model.CheckinResult, error) { return engine.Confirm(ctx, account.ID) },
	} {
		replayed, err := action()
		if err != nil || replayed.LogID != result.LogID || !replayed.Replayed || replayed.Reward != "0" {
			t.Fatalf("repeat execution did not replay recorded outcome: %v", err)
		}
	}
	if submits.Load() != 1 || queries.Load() != 2 {
		t.Fatalf("unexpected requests: submits=%d queries=%d", submits.Load(), queries.Load())
	}
	var logs []model.CheckinLog
	if err := conn.Find(&logs).Error; err != nil || len(logs) != 1 || strings.Contains(logs[0].Message, "fixture-secret") {
		t.Fatalf("duplicate/unsafe history: %v", err)
	}
	stored, err := store.GetAccount(ctx, account.ID)
	if err != nil || stored.LastCheckinSuccessAt == nil || !stored.CheckinPending.Finished {
		t.Fatalf("account success was not committed: %v", err)
	}
}

func TestWorkflowPendingSurvivesRestartAndMissingHistory(t *testing.T) {
	var submits atomic.Int32
	var confirmed atomic.Bool
	engine, store, conn, site, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submits.Add(1)
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"checked": confirmed.Load(), "reward": 0.25}})
	}, true)
	ctx := context.Background()
	first, err := engine.Execute(ctx, account.ID, model.CheckinTriggerManual)
	if err != nil || first.Status != model.SiteExecutionStatusPending || !first.Submitted {
		t.Fatalf("expected submitted pending state: %+v %v", first, err)
	}
	if err := conn.Delete(&model.CheckinLog{}, first.LogID).Error; err != nil {
		t.Fatal(err)
	}
	confirmed.Store(true)
	restartedStore := op.NewCheckinStore(conn)
	restarted := NewEngine(restartedStore, testSessions(restartedStore, NewHTTPAuthenticator(restartedStore)), restartedStore)
	result, err := restarted.Confirm(ctx, account.ID)
	if err != nil || result.LogID != first.LogID || result.Status != model.SiteExecutionStatusSuccess || result.Reward != "0.25" || submits.Load() != 1 {
		t.Fatalf("restart re-submitted or lost confirmation: %+v %v", result, err)
	}
	if err := store.DeleteAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSite(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	var log model.CheckinLog
	if err := conn.First(&log, first.LogID).Error; err != nil || log.SiteName != "HTTP site" || log.AccountName != "HTTP account" {
		t.Fatalf("deletion lost readable history snapshots: %v", err)
	}
}

func TestWorkflowCancelledSubmissionOnlyResumesStatus(t *testing.T) {
	var submits atomic.Int32
	var confirmed atomic.Bool
	submitted := make(chan struct{})
	handlerReleased := make(chan struct{})
	var release sync.Once
	engine, _, _, _, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submits.Add(1)
			close(submitted)
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-handlerReleased
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked": confirmed.Load()}})
	}, true)
	t.Cleanup(func() { release.Do(func() { close(handlerReleased) }) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		result, err := engine.Execute(ctx, account.ID, model.CheckinTriggerManual)
		if err == nil && (result == nil || !result.Submitted || result.Status != model.SiteExecutionStatusPending) {
			err = errors.New("cancellation lost pending submission")
		}
		done <- err
	}()
	select {
	case <-submitted:
	case <-time.After(5 * time.Second):
		t.Fatal("submission not reached")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled execution did not persist and exit")
	}
	release.Do(func() { close(handlerReleased) })
	confirmed.Store(true)
	result, err := engine.Confirm(context.Background(), account.ID)
	if err != nil || result.Status != model.SiteExecutionStatusSuccess || submits.Load() != 1 {
		t.Fatalf("cancelled submission was repeated: %+v %v", result, err)
	}
}

func TestWorkflowStatus401RecoversWithoutResubmitting(t *testing.T) {
	store, _ := sessionStore(t)
	var refreshes, submits, queries atomic.Int32
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		if r.URL.Path != "/api/v1/auth/refresh" || r.Header.Get("Authorization") != "" {
			t.Error("refresh used execution address or stale bearer")
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}}`))
	}))
	defer auth.Close()
	execution := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("bearer checkin sent a competing cookie identity")
		}
		if r.URL.Path == "/submit" {
			submits.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"reason":"TOKEN_EXPIRED"}`))
			return
		}
		queries.Add(1)
		if submits.Load() == 0 {
			_, _ = w.Write([]byte(`{"checked":false}`))
		} else if r.Header.Get("Authorization") != "Bearer new-access" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"reason":"TOKEN_EXPIRED"}`))
		} else {
			_, _ = w.Write([]byte(`{"checked":true}`))
		}
	}))
	defer execution.Close()
	ctx := context.Background()
	site, err := store.CreateSite(ctx, model.CheckinSite{Name: "bearer", Platform: model.SitePlatformSub2API, BaseURL: execution.URL, Enabled: true,
		CheckinHTTPEnabled: true, CheckinHTTPPath: "/submit", CheckinStatus: &model.SiteCheckinStatusConfig{Path: "/status", CheckedPath: "checked", MaxAttempts: 1, PollIntervalMs: 1}})
	if err != nil {
		t.Fatal(err)
	}
	c := model.CheckinCredential{Name: "auth", Platform: model.SitePlatformSub2API, Type: model.CheckinCredentialAccessToken,
		AuthBaseURL: auth.URL, AccessToken: "old-access", RefreshToken: "old-refresh", TokenExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	account, err := store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: "bearer account", Enabled: true, AuthMode: model.CheckinAuthBearer}, &c)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(store, testSessions(store, NewHTTPAuthenticator(store)), store)
	result, err := engine.Execute(ctx, account.ID, model.CheckinTriggerManual)
	if err != nil || result.Status != model.SiteExecutionStatusSuccess || submits.Load() != 1 || refreshes.Load() != 1 || queries.Load() != 3 {
		t.Fatalf("401 handling resubmitted or failed recovery: %+v %v; submits=%d refresh=%d query=%d", result, err, submits.Load(), refreshes.Load(), queries.Load())
	}
}

func TestWorkflowConcurrentEnginesSubmitOnce(t *testing.T) {
	var submits atomic.Int32
	engine, store, _, _, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submits.Add(1)
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked": submits.Load() > 0}})
	}, true)
	other := NewEngine(store, testSessions(store, NewHTTPAuthenticator(store)), store)
	var wg sync.WaitGroup
	for _, runner := range []*Engine{engine, other} {
		wg.Go(func() {
			_, err := runner.Execute(context.Background(), account.ID, model.CheckinTriggerManual)
			if err != nil && !errors.Is(err, op.ErrCheckinWorkflowConflict) {
				t.Errorf("concurrent engine failed: %v", err)
			}
		})
	}
	wg.Wait()
	if submits.Load() != 1 {
		t.Fatalf("multiple engine submissions=%d", submits.Load())
	}
}

func TestWorkflowUnknownSubmitAndRedirectAreNotReplayed(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTemporaryRedirect, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var submits, redirects atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirects.Add(1) }))
			defer target.Close()
			engine, _, _, _, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
				submits.Add(1)
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`<html>unknown fixture-secret</html>`))
			}, false)
			result, err := engine.Execute(context.Background(), account.ID, model.CheckinTriggerManual)
			if err != nil || result.Status != model.SiteExecutionStatusFailed || !result.Submitted || strings.Contains(result.Message, "fixture-secret") {
				t.Fatalf("unknown submission was reported as success or leaked response: %+v %v", result, err)
			}
			if _, err := engine.Execute(context.Background(), account.ID, model.CheckinTriggerManual); err != nil {
				t.Fatal(err)
			}
			if submits.Load() != 1 || redirects.Load() != 0 {
				t.Fatal("unconfirmed request was replayed or followed a redirect")
			}
		})
	}
}

type lostReservationAck struct {
	WorkflowStore
}

func (s lostReservationAck) ReserveWorkflowSubmission(ctx context.Context, execution *op.CheckinExecution, before bool) (*op.CheckinExecution, error) {
	if _, err := s.WorkflowStore.ReserveWorkflowSubmission(ctx, execution, before); err != nil {
		return nil, err
	}
	return nil, errors.New("synthetic lost reservation acknowledgement")
}

func TestWorkflowLostReservationAckNeverAuthorizesSend(t *testing.T) {
	var submits atomic.Int32
	var checked atomic.Bool
	engine, store, _, _, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submits.Add(1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked": checked.Load()}})
	}, true)
	engine.store = lostReservationAck{WorkflowStore: store}
	if _, err := engine.Execute(context.Background(), account.ID, model.CheckinTriggerManual); err == nil {
		t.Fatal("lost commit acknowledgement was accepted as submission authority")
	}
	stored, err := store.GetAccount(context.Background(), account.ID)
	if err != nil || stored.CheckinPending == nil || !stored.CheckinPending.Submitted || submits.Load() != 0 {
		t.Fatalf("lost acknowledgement lost reservation or sent HTTP: %v", err)
	}
	checked.Store(true)
	restarted := NewEngine(store, testSessions(store, NewHTTPAuthenticator(store)), store)
	result, err := restarted.Confirm(context.Background(), account.ID)
	if err != nil || result.Status != model.SiteExecutionStatusSuccess || submits.Load() != 0 {
		t.Fatalf("confirmation after uncertain reservation re-submitted: %+v %v", result, err)
	}
}

func TestWorkflowConfigurationChangeBlocksStaleHTTP(t *testing.T) {
	var store *op.CheckinStore
	var site *model.CheckinSite
	var submits atomic.Int32
	engine, createdStore, _, createdSite, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submits.Add(1)
			return
		}
		_, err := store.PatchSite(context.Background(), site.ID, model.CheckinSitePatch{
			CheckinHTTPPath: model.CheckinPatchField[string]{Present: true, Value: "/changed"},
		})
		if err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"data":{"checked":false}}`))
	}, true)
	store, site = createdStore, createdSite
	if _, err := engine.Execute(context.Background(), account.ID, model.CheckinTriggerManual); !errors.Is(err, op.ErrCheckinWorkflowChanged) {
		t.Fatalf("execution failed to detect endpoint edit during precheck: %v", err)
	}
	if submits.Load() != 0 {
		t.Fatal("stale configuration submitted a checkin")
	}
	stored, err := store.GetAccount(context.Background(), account.ID)
	if err != nil || stored.CheckinPending == nil || stored.CheckinPending.Submitted {
		t.Fatalf("configuration edit lost unsubmitted workflow: %v", err)
	}
}

func TestWorkflowCrossDayConfirmationRequiresDateEvidence(t *testing.T) {
	for _, dateAware := range []bool{false, true} {
		name := "today-only"
		if dateAware {
			name = "date-aware"
		}
		t.Run(name, func(t *testing.T) {
			var submits, queries atomic.Int32
			var checked atomic.Bool
			location, _ := time.LoadLocation("Asia/Shanghai")
			yesterday := time.Now().In(location).AddDate(0, 0, -1).Format("2006-01-02")
			engine, store, conn, site, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/submit" {
					submits.Add(1)
					_, _ = w.Write([]byte(`{"success":true}`))
					return
				}
				queries.Add(1)
				date := r.URL.Query().Get("date")
				if date == "" {
					date = time.Now().In(location).Format("2006-01-02")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked": checked.Load(), "date": date, "reward": 2}})
			}, true)
			if dateAware {
				config := *site.CheckinStatus
				config.DatePath, config.Path = "data.date", "/status?date={{checkin_date}}"
				if _, err := store.PatchSite(context.Background(), site.ID, model.CheckinSitePatch{CheckinStatus: model.CheckinPatchField[*model.SiteCheckinStatusConfig]{Present: true, Value: &config}}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := engine.Execute(context.Background(), account.ID, model.CheckinTriggerManual)
			if err != nil || result.Status != model.SiteExecutionStatusPending {
				t.Fatalf("initial pending state: %v", err)
			}
			stored, err := store.GetAccount(context.Background(), account.ID)
			if err != nil {
				t.Fatal(err)
			}
			stored.CheckinPending.Date, stored.CheckinPending.Deadline = yesterday, time.Now().Add(-time.Minute)
			if err := conn.Select("checkin_pending").Updates(stored).Error; err != nil {
				t.Fatal(err)
			}
			expired, err := engine.Execute(context.Background(), account.ID, model.CheckinTriggerManual)
			if err != nil || expired.Status != model.SiteExecutionStatusFailed || expired.LogID != result.LogID {
				t.Fatalf("expired submission was replaced: %+v %v", expired, err)
			}
			queryCount := queries.Load()
			checked.Store(true)
			confirmed, err := engine.Confirm(context.Background(), account.ID)
			if err != nil || confirmed.LogID != result.LogID || submits.Load() != 1 {
				t.Fatalf("cross-day confirmation changed submission: %+v %v", confirmed, err)
			}
			if dateAware {
				if confirmed.Status != model.SiteExecutionStatusSuccess || confirmed.Date != yesterday || queries.Load() != queryCount+1 {
					t.Fatal("explicit date evidence did not confirm the original day")
				}
			} else if confirmed.Status != model.SiteExecutionStatusFailed || queries.Load() != queryCount {
				t.Fatal("today's endpoint incorrectly confirmed yesterday's submission")
			}
		})
	}
}

func TestWorkflowScriptStatusAndRewardUseSharedSandbox(t *testing.T) {
	var submits atomic.Int32
	engine, store, _, site, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submits.Add(1)
			_, _ = w.Write([]byte(`{"success":true,"data":{"bonus":0}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"done": submits.Load() > 0})
	}, true)
	config := *site.CheckinStatus
	config.Code = `return response.done === true ? "checked_in" : "not_checked_in";`
	config.TimeoutSeconds, config.RewardSource = 30, "submit"
	if _, err := store.PatchSite(context.Background(), site.ID, model.CheckinSitePatch{
		CheckinStatus:          model.CheckinPatchField[*model.SiteCheckinStatusConfig]{Present: true, Value: &config},
		CheckinRewardExtractor: model.CheckinPatchField[string]{Present: true, Value: `return responses.submit.data.bonus;`},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(context.Background(), account.ID, model.CheckinTriggerManual)
	if err != nil || result.Status != model.SiteExecutionStatusSuccess || result.Reward != "0" || submits.Load() != 1 {
		t.Fatalf("status/reward scripts lost isolated evaluation or zero reward: %+v %v", result, err)
	}
}

func TestWorkflowMidnightDuringStatusRequestDoesNotConfirmPreviousDay(t *testing.T) {
	var submits, queries atomic.Int32
	var nextDay atomic.Bool
	engine, _, _, _, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submits.Add(1)
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		queries.Add(1)
		checked := submits.Load() > 0
		if checked {
			nextDay.Store(true)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"checked": checked, "reward": 1}})
	}, true)
	engine.now = func() time.Time {
		now := time.Now()
		if nextDay.Load() {
			return now.Add(24 * time.Hour)
		}
		return now
	}
	result, err := engine.Execute(context.Background(), account.ID, model.CheckinTriggerManual)
	if err != nil || result.Status != model.SiteExecutionStatusFailed || result.Reward != "" || !result.Submitted || submits.Load() != 1 || queries.Load() != 2 {
		t.Fatalf("status crossing midnight confirmed the wrong day: %+v %v", result, err)
	}
	confirmed, err := engine.Confirm(context.Background(), account.ID)
	if err != nil || confirmed.Status != model.SiteExecutionStatusFailed || confirmed.LogID != result.LogID || submits.Load() != 1 || queries.Load() != 2 {
		t.Fatalf("today-only confirmation resumed yesterday's request: %+v %v", confirmed, err)
	}
}
