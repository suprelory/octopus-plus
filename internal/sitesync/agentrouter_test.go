package sitesync

import (
	"context"
	"encoding/json"
	"fmt"
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
)

func TestAgentRouterLinkedCheckinAndDailySchedule(t *testing.T) {
	for _, balanceFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("balance failure=%t", balanceFails), func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			var logins, profiles atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/user/login" {
					logins.Add(1)
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.Method != http.MethodPost || body["username"] != "user@example.com" || body["password"] != "password" {
						t.Error("unexpected login request")
					}
					if r.Header.Get("Origin") == "" || !strings.HasSuffix(r.Header.Get("Referer"), "/login") {
						t.Error("missing login browser headers")
					}
					w.Header().Add("Set-Cookie", "session=login-session; Path=/; HttpOnly")
					_, _ = w.Write([]byte(`{"success":true,"data":{"id":42,"checked_in":true,"quota":12500000}}`))
					return
				}
				wantCookie := "login-session"
				if r.URL.Path == "/api/status" {
					wantCookie = "final-session"
				}
				if cookie, err := r.Cookie("session"); err != nil || cookie.Value != wantCookie {
					t.Errorf("%s did not reuse the latest login session", r.URL.Path)
				}
				if r.URL.Path != "/api/status" && (r.Header.Get("New-API-User") != "42" || r.Header.Get("Authorization") != "") {
					t.Error("login user ID or cookie authentication was lost")
				}
				switch r.URL.Path {
				case "/api/log/self/":
					// A log failure must not discard checked_in=true.
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
				case "/api/user/self":
					profiles.Add(1)
					w.Header().Add("Set-Cookie", "session=final-session; Path=/")
					if balanceFails {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					_, _ = w.Write([]byte(`{"success":true,"data":{"id":42,"quota":12500000,"used_quota":2000000,"today_income":3000000}}`))
				case "/api/status":
					_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":1000000}}`))
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			site, account, source := createLinkedCheckinFixture(t, ctx, server.URL)
			if err := db.GetDB().Model(&model.Site{}).Where("id IN ?", []int{site.ID, source.SiteID}).Updates(map[string]any{
				"platform": model.SitePlatformAgentRouter, "checkin_window_start": "13:37", "checkin_window_end": "13:38",
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.GetDB().Model(source).Updates(map[string]any{
				"credential_type": model.SiteCredentialTypeUsernamePassword, "username": "user@example.com", "password": "password", "balance": 17,
			}).Error; err != nil {
				t.Fatal(err)
			}
			result, err := CheckinAccount(ctx, account.ID)
			if err != nil || result.Status != model.SiteExecutionStatusSuccess || result.Reward != "" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if logins.Load() != 1 || profiles.Load() != 1 {
				t.Fatalf("login or profile was repeated: %d %d", logins.Load(), profiles.Load())
			}
			savedSource, err := op.SiteAccountGet(source.ID, ctx)
			if err != nil || !strings.Contains(savedSource.AccessToken, "session=final-session") || firstManagedPlatformUserID(savedSource) != 42 {
				t.Fatalf("session not saved: %v", err)
			}
			wantBalance := 12.5
			if balanceFails {
				wantBalance = 17
			}
			if savedSource.Balance != wantBalance || (!balanceFails && (savedSource.BalanceUsed != 2 || savedSource.TodayIncome != 3)) {
				t.Fatalf("balance changed incorrectly: %+v", savedSource)
			}
			saved, err := op.SiteAccountGet(account.ID, ctx)
			if err != nil || saved.LastCheckinSuccessAt == nil || saved.NextAutoCheckinAt == nil || saved.AccessToken != "" {
				t.Fatalf("checkin projection: %+v err=%v", saved, err)
			}
			local := saved.NextAutoCheckinAt.In(resolveSiteCheckinWindow(site).location)
			if local.Format("15:04") != "13:37" {
				t.Fatalf("user schedule was changed: %s", local)
			}
			result, err = checkinAccountWithTrigger(ctx, account.ID, SiteBatchTriggerScheduled)
			if err != nil || result.Status != model.SiteExecutionStatusSkipped || logins.Load() != 1 {
				t.Fatalf("scheduled run repeated today's checkin: %+v %v", result, err)
			}
		})
	}
}

func TestAgentRouterCheckinRequiresExplicitEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, login, logs        string
		status                   int
		wantSuccess, wantAlready bool
	}{
		{name: "confirmed without session", login: `{"success":true,"data":{"checked_in":true}}`, wantSuccess: true},
		{name: "login alone", login: `{"success":true,"data":{"id":42}}`},
		{name: "false checkin", login: `{"success":true,"data":{"id":42,"checked_in":false}}`},
		{name: "string checkin", login: `{"success":true,"data":{"id":42,"checked_in":"false"}}`},
		{name: "invalid credentials", login: `{"success":false,"message":"invalid password","data":{"checked_in":true}}`},
		{name: "string success", login: `{"success":"true","data":{"checked_in":true}}`},
		{name: "missing data", login: `{"success":true}`},
		{name: "HTML", login: `<html>login</html>`},
		{name: "rate limit", status: 429, login: `{"success":true,"data":{"checked_in":true}}`},
		{name: "server error", status: 503, login: `{"success":true,"data":{"checked_in":true}}`},
		{name: "today log", login: `{"success":true,"data":{"id":42,"checked_in":false}}`, logs: `{"success":true,"data":{"items":[{"type":4,"content":"每日签到成功，增加额度","created_at":%d}]}}`, wantSuccess: true, wantAlready: true},
		{name: "unrelated credit", login: `{"success":true,"data":{"id":42}}`, logs: `{"success":true,"data":{"items":[{"type":4,"content":"充值成功","created_at":%d}]}}`},
		{name: "wrong log type", login: `{"success":true,"data":{"id":42}}`, logs: `{"success":true,"data":{"items":[{"type":1,"content":"签到成功","created_at":%d}]}}`},
		{name: "failed log response", login: `{"success":true,"data":{"id":42}}`, logs: `{"success":false,"data":{"items":[{"type":4,"content":"签到成功","created_at":%d}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logins atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/user/login":
					logins.Add(1)
					if tc.name != "confirmed without session" {
						w.Header().Add("Set-Cookie", "session=test-session; Path=/")
					}
					if tc.status != 0 {
						w.WriteHeader(tc.status)
					}
					_, _ = w.Write([]byte(tc.login))
				case "/api/log/self/":
					if tc.logs == "" {
						http.NotFound(w, r)
						return
					}
					_, _ = fmt.Fprintf(w, tc.logs, time.Now().Add(-time.Second).Unix())
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			}))
			defer server.Close()
			result, _, err := checkinAccountState(context.Background(), &model.Site{Platform: model.SitePlatformAgentRouter, BaseURL: server.URL},
				&model.SiteAccount{CredentialType: model.SiteCredentialTypeUsernamePassword, Username: "user", Password: "password"}, func(string) error {
					t.Error("login-triggered checkin must not take a post-login baseline")
					return nil
				})
			ok := err == nil && result != nil && result.Status == model.SiteExecutionStatusSuccess
			if ok != tc.wantSuccess || logins.Load() != 1 {
				t.Fatalf("result=%+v err=%v logins=%d", result, err, logins.Load())
			}
			if result != nil && result.Reward != "" {
				t.Fatal("invented a reward")
			}
			if tc.wantAlready && result.Reason != model.SiteCheckinReasonAlreadyCheckedIn {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestAgentRouterLogsUseCalendarDay(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 1, 0, 0, time.FixedZone("test", 8*3600))
	for _, stamp := range []time.Time{now.Add(-2 * time.Minute), now.Add(time.Minute), now.Add(-time.Second)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, `{"success":true,"data":{"items":[{"type":4,"content":"每日签到成功","created_at":%d}]}}`, stamp.Unix())
		}))
		id := 42
		got := agentRouterCheckinLogTime(context.Background(), &model.Site{BaseURL: server.URL, CheckinTimezone: "Asia/Shanghai"}, &model.SiteAccount{PlatformUserID: &id}, "session=test", now)
		server.Close()
		want := stamp.Equal(now.Add(-time.Second))
		if !got.IsZero() != want {
			t.Fatalf("stamp=%s got=%s", stamp, got)
		}
	}
}

func TestAgentRouterQuotaFallbackAndSessionIsolation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/login":
			var data map[string]string
			_ = json.NewDecoder(r.Body).Decode(&data)
			w.Header().Add("Set-Cookie", "session="+data["username"]+"; Path=/")
			_, _ = fmt.Fprintf(w, `{"success":true,"data":{"id":%s,"checked_in":true}}`, data["username"])
		case "/api/log/self/":
			cookie, err := r.Cookie("session")
			if err != nil || cookie.Value != r.Header.Get("New-API-User") {
				t.Error("accounts shared a session")
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"items":[]}}`))
		case "/api/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":0}}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	site := &model.Site{BaseURL: server.URL, Platform: model.SitePlatformAgentRouter}
	var wg sync.WaitGroup
	for _, name := range []string{"42", "84"} {
		wg.Go(func() {
			result, _, err := checkinAgentRouter(context.Background(), site, &model.SiteAccount{CredentialType: model.SiteCredentialTypeUsernamePassword, Username: name, Password: "password"})
			if err != nil || result.Status != model.SiteExecutionStatusSuccess {
				t.Errorf("result=%+v err=%v", result, err)
			}
		})
	}
	wg.Wait()
	if scale := agentRouterQuotaPerUnit(context.Background(), site, nil); scale != 500000 {
		t.Fatalf("invalid quota scale fallback: %f", scale)
	}
}

func TestAgentRouterSubscriptionSync(t *testing.T) {
	for _, credentialType := range []model.SiteCredentialType{model.SiteCredentialTypeUsernamePassword, model.SiteCredentialTypeAccessToken} {
		t.Run(string(credentialType), func(t *testing.T) {
			var logins, profiles atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/user/login":
					logins.Add(1)
					w.Header().Add("Set-Cookie", "session=initial; Path=/")
					_, _ = w.Write([]byte(`{"success":true,"data":{"id":42,"checked_in":true}}`))
				case "/v1/models", "/models":
					if r.Header.Get("Authorization") != "Bearer sk-model-token" {
						t.Error("management session was used as a model API key")
					}
					_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o-mini"}]}`))
				default:
					wantCookie := "rotated"
					if r.URL.Path == "/api/user/self" && profiles.Add(1) == 1 {
						wantCookie = "initial"
					}
					if cookie, err := r.Cookie("session"); err != nil || cookie.Value != wantCookie {
						t.Errorf("%s lost the session", r.URL.Path)
					}
					switch r.URL.Path {
					case "/api/user/self":
						w.Header().Add("Set-Cookie", "session=rotated; Path=/")
						_, _ = w.Write([]byte(`{"success":true,"data":{"id":42,"quota":12500000,"used_quota":500000,"today_income":0}}`))
					case "/api/token/":
						_, _ = w.Write([]byte(`{"success":true,"data":{"items":[{"key":"sk-model-token","group":"default"}]}}`))
					case "/api/user/self/groups", "/api/user_group_map":
						_, _ = w.Write([]byte(`{"success":true,"data":["default"]}`))
					case "/api/pricing":
						_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
					case "/api/status":
						_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":1000000}}`))
					default:
						t.Errorf("unexpected endpoint: %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}
			}))
			defer server.Close()
			id := 42
			snapshot, err := syncAccountState(context.Background(), &model.Site{Platform: model.SitePlatformAgentRouter, BaseURL: server.URL},
				&model.SiteAccount{CredentialType: credentialType, Username: "user", Password: "password", AccessToken: "session=initial", PlatformUserID: &id})
			if err != nil || snapshot == nil || len(snapshot.tokens) != 1 || len(snapshot.models) != 1 || snapshot.balance != 12.5 || !strings.Contains(snapshot.accessToken, "session=rotated") {
				t.Fatalf("snapshot=%+v err=%v", snapshot, err)
			}
			wantLogins := int32(0)
			if credentialType == model.SiteCredentialTypeUsernamePassword {
				wantLogins = 1
			}
			if logins.Load() != wantLogins {
				t.Fatalf("unexpected logins: %d", logins.Load())
			}
		})
	}
}
