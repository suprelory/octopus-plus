package checkin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/checkin/finance"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func customBalance(t *testing.T, store *op.CheckinStore, id int, platform model.SitePlatform, base string, kind model.CheckinCredentialType) *model.CheckinCredential {
	t.Helper()
	c := model.CheckinCredential{Name: "balance", Platform: platform, Type: kind, AuthBaseURL: base, AccessToken: "balance-secret", APIKey: "key-secret", Username: "user", Password: "password"}
	credential, err := store.CreateCredential(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	var patch model.CheckinAccountPatch
	raw, _ := json.Marshal(map[string]any{"balance_config": model.CheckinBalanceConfig{Mode: model.CheckinBalanceCustom, Platform: platform, BaseURL: base, CredentialID: &credential.ID}})
	if err := json.Unmarshal(raw, &patch); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchAccount(context.Background(), id, patch); err != nil {
		t.Fatal(err)
	}
	return credential
}

func TestBalancePlatformsAndMissingFields(t *testing.T) {
	for _, platform := range []model.SitePlatform{model.SitePlatformNewAPI, model.SitePlatformOneAPI, model.SitePlatformOneHub, model.SitePlatformDoneHub, model.SitePlatformAnyRouter, model.SitePlatformAgentRouter, model.SitePlatformSub2API} {
		t.Run(string(platform), func(t *testing.T) {
			var phase atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path == "/api/user/login" {
					t.Error("balance caused a mutation")
					w.WriteHeader(500)
					return
				}
				if r.Header.Get("Authorization") != "Bearer balance-secret" {
					t.Error("wrong balance credential")
				}
				if r.URL.Path == "/api/status" {
					fmt.Fprint(w, `{"success":true,"data":{"quota_per_unit":1000}}`)
					return
				}
				if platform == model.SitePlatformSub2API {
					if r.URL.Path != "/api/v1/auth/me" {
						t.Error("wrong Sub2API endpoint")
					}
					switch phase.Load() {
					case 0:
						fmt.Fprint(w, `{"code":0,"data":{"balance":"2.5"}}`)
					case 1:
						fmt.Fprint(w, `{"balance":0}`)
					default:
						fmt.Fprint(w, `{"code":0,"data":{"balance":null}}`)
					}
					return
				}
				switch phase.Load() {
				case 0:
					fmt.Fprint(w, `{"success":true,"data":{"id":9,"quota":1000000,"used_quota":"250000","today_income":125000}}`)
				case 1:
					if platform == model.SitePlatformOneAPI || platform == model.SitePlatformOneHub {
						fmt.Fprint(w, `{"success":true,"data":{"quota":0,"used_quota":0}}`)
					} else {
						fmt.Fprint(w, `{"success":true,"data":{"quota":0}}`)
					}
				default:
					fmt.Fprint(w, `{"success":true,"data":{"quota":"NaN"}}`)
				}
			}))
			defer server.Close()
			engine, store, _, _, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {}, false)
			customBalance(t, store, account.ID, platform, server.URL, model.CheckinCredentialAccessToken)
			value, err := engine.balances.Query(context.Background(), account.ID, false)
			want := 2.0
			if platform == model.SitePlatformOneAPI || platform == model.SitePlatformOneHub {
				want = 1.5
			}
			if platform == model.SitePlatformAgentRouter {
				want = 1000
			}
			if platform == model.SitePlatformSub2API {
				want = 2.5
			}
			if err != nil || value.Value != want {
				t.Fatalf("balance=%+v err=%v want=%v", value, err, want)
			}
			phase.Store(1)
			zero, err := engine.balances.Query(context.Background(), account.ID, false)
			if err != nil || zero.Value != 0 {
				t.Fatalf("lost zero: %+v %v", zero, err)
			}
			stored, _ := store.GetAccount(context.Background(), account.ID)
			if stored.BalanceUpdatedAt == nil || stored.Balance != 0 || platform != model.SitePlatformSub2API && stored.TodayIncome != value.Income {
				t.Fatal("missing field overwrote prior data")
			}
			phase.Store(2)
			if _, err := engine.balances.Query(context.Background(), account.ID, false); err == nil {
				t.Fatal("accepted invalid balance")
			}
			last, _ := store.GetAccount(context.Background(), account.ID)
			if !last.BalanceUpdatedAt.Equal(*stored.BalanceUpdatedAt) {
				t.Fatal("failed query overwrote valid balance")
			}
		})
	}
}

func TestBalanceDisabledPrimaryAndAPIKey(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer key-secret" {
			t.Error("API key missing")
		}
		fmt.Fprint(w, `{"balance":0}`)
	}))
	defer server.Close()
	engine, store, conn, _, account := httpWorkflow(t, func(http.ResponseWriter, *http.Request) {}, false)
	if _, err := engine.balances.Query(context.Background(), account.ID, true); !errors.Is(err, op.ErrCheckinBalanceDisabled) {
		t.Fatal(err)
	}
	customBalance(t, store, account.ID, model.SitePlatformSub2API, server.URL, model.CheckinCredentialAPIKey)
	if _, err := engine.balances.Query(context.Background(), account.ID, true); err != nil {
		t.Fatal(err)
	}
	// Primary derives authentication address and network from the main credential.
	credential, err := store.CreateCredential(context.Background(), model.CheckinCredential{Name: "primary", Type: model.CheckinCredentialAccessToken, Platform: model.SitePlatformSub2API, AuthBaseURL: server.URL, AccessToken: "key-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(account).Updates(map[string]any{"credential_id": credential.ID, "auth_mode": model.CheckinAuthBearer, "balance_config": `{"mode":"primary"}`}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := engine.balances.Query(context.Background(), account.ID, true); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatal("disabled balance sent requests")
	}
}

func TestBalanceReadRecoveryAndAgentRouterNoLogin(t *testing.T) {
	for _, platform := range []model.SitePlatform{model.SitePlatformNewAPI, model.SitePlatformAgentRouter, model.SitePlatformSub2API} {
		t.Run(string(platform), func(t *testing.T) {
			var requests, logins atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer renewed" {
					w.WriteHeader(401)
					fmt.Fprint(w, `{"success":false}`)
					return
				}
				fmt.Fprint(w, `{"success":true,"code":0,"data":{"quota":500000,"balance":1}}`)
			}))
			defer server.Close()
			engine, store, _, _, account := httpWorkflow(t, func(http.ResponseWriter, *http.Request) {}, false)
			kind := model.CheckinCredentialPassword
			if platform == model.SitePlatformSub2API {
				kind = model.CheckinCredentialAccessToken
			}
			credential := customBalance(t, store, account.ID, platform, server.URL, kind)
			if platform == model.SitePlatformSub2API {
				candidate := *credential
				candidate.RefreshToken = "refresh"
				if _, err := store.EditCredential(context.Background(), candidate, candidate.CredentialVersion); err != nil {
					t.Fatal(err)
				}
			}
			auth := fakeAuth{login: func(context.Context, model.CheckinCredential) (op.CheckinLoginSession, error) {
				logins.Add(1)
				return op.CheckinLoginSession{AccessToken: "renewed"}, nil
			}, refresh: func(context.Context, model.CheckinCredential) (op.CheckinTokenPair, error) {
				logins.Add(1)
				return op.CheckinTokenPair{AccessToken: "renewed", RefreshToken: "rotated", TokenExpiresAt: time.Now().Add(time.Hour).UnixMilli()}, nil
			}}
			engine.balances.sessions = testSessions(store, auth)
			value, err := engine.balances.Query(context.Background(), account.ID, false)
			if platform == model.SitePlatformAgentRouter {
				if err == nil || logins.Load() != 0 || requests.Load() != 1 {
					t.Fatal("AgentRouter attempted implicit login")
				}
				return
			}
			if err != nil || value.Value != 1 || logins.Load() != 1 || requests.Load() != 2 {
				t.Fatalf("read recovery failed: %+v %v logins=%d requests=%d", value, err, logins.Load(), requests.Load())
			}
		})
	}
}

func TestBalanceRewardFreshnessUsageAndReplay(t *testing.T) {
	for _, scenario := range []string{"delta", "zero", "failure", "resumed", "shared"} {
		t.Run(scenario, func(t *testing.T) {
			var submits, queries atomic.Int32
			var confirmed atomic.Bool
			engine, store, conn, _, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/submit" {
					submits.Add(1)
					fmt.Fprint(w, `{"success":true}`)
					return
				}
				checked := submits.Load() > 0 && (scenario != "resumed" || confirmed.Load())
				fmt.Fprintf(w, `{"success":true,"data":{"checked":%t}}`, checked)
			}, true)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				queries.Add(1)
				if scenario == "failure" {
					fmt.Fprint(w, `{"success":true,"data":{}}`)
					return
				}
				quota, used := 500000, 100000
				if submits.Load() > 0 && scenario != "zero" {
					quota, used = 600000, 150000
				}
				fmt.Fprintf(w, `{"success":true,"data":{"quota":%d,"used_quota":%d,"today_income":200000}}`, quota, used)
			}))
			defer server.Close()
			credential := customBalance(t, store, account.ID, model.SitePlatformNewAPI, server.URL, model.CheckinCredentialAccessToken)
			if scenario == "shared" {
				copy := *account
				copy.Name = "shared"
				copy.BalanceConfig = model.CheckinBalanceConfig{Mode: model.CheckinBalanceCustom, Platform: credential.Platform, BaseURL: server.URL, CredentialID: &credential.ID}
				if _, err := store.CreateAccount(context.Background(), copy, nil); err != nil {
					t.Fatal(err)
				}
			}
			result, err := engine.Execute(context.Background(), account.ID, model.CheckinTriggerManual)
			if scenario == "resumed" {
				confirmed.Store(true)
				result, err = NewEngine(store, engine.sessions, store).Confirm(context.Background(), account.ID)
			}
			want := ""
			if scenario == "delta" {
				want = "0.3"
			}
			if scenario == "zero" {
				want = "0"
			}
			if err != nil || result.Status != model.SiteExecutionStatusSuccess || result.Reward != want {
				t.Fatalf("result=%+v err=%v want=%s", result, err, want)
			}
			count := queries.Load()
			replay, err := engine.Execute(context.Background(), account.ID, model.CheckinTriggerManual)
			if err != nil || replay.Reward != want || queries.Load() != count || submits.Load() != 1 {
				t.Fatal("replay queried or credited again")
			}
			var completion model.CheckinCompletion
			if err := conn.Where("log_id = ? AND status = ?", result.LogID, model.SiteExecutionStatusSuccess).First(&completion).Error; err != nil || completion.Entry.Reward != want {
				t.Fatal("completion lost reward", err)
			}
		})
	}
}

func TestBalanceIncomePagination(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.FixedZone("fixture", 8*3600))
	for _, complete := range []bool{true, false} {
		calls := 0
		value, ok := balanceIncome(func(path string) (map[string]any, error) {
			calls++
			if complete {
				return map[string]any{"data": map[string]any{"total": float64(1), "items": []any{map[string]any{"quota": float64(250000)}}}}, nil
			}
			return map[string]any{"items": []any{map[string]any{"content": "income 1,234.50 USD"}}}, nil
		}, now, finance.QuotaPerUSD)
		if complete && (!ok || value != 1 || calls != 2) || !complete && (ok || calls != 6) {
			t.Fatalf("pagination value=%v ok=%v calls=%d", value, ok, calls)
		}
	}
}

func TestBalanceCookieRevocationAndOrigin(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Cookie") != "session=balance-secret" {
			t.Error("missing scoped cookie")
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "", Path: "/", MaxAge: -1})
		w.WriteHeader(401)
		fmt.Fprint(w, `{"success":false}`)
	}))
	defer server.Close()
	engine, store, _, _, account := httpWorkflow(t, func(http.ResponseWriter, *http.Request) {}, false)
	c, err := store.CreateCredential(context.Background(), model.CheckinCredential{Name: "cookie balance", Platform: model.SitePlatformAnyRouter, Type: model.CheckinCredentialCookie, AuthBaseURL: server.URL, Cookie: "session=balance-secret"})
	if err != nil {
		t.Fatal(err)
	}
	var patch model.CheckinAccountPatch
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"balance_config":{"mode":"custom","platform":"anyrouter","base_url":%q,"credential_id":%d}}`, server.URL, c.ID)), &patch); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchAccount(context.Background(), account.ID, patch); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := engine.balances.Query(context.Background(), account.ID, false); err == nil {
			t.Fatal("revoked cookie accepted")
		}
	}
	if requests.Load() != 1 {
		t.Fatal("revoked cookie reused after restart")
	}
	stored, err := store.GetCredential(context.Background(), c.ID)
	if err != nil || len(stored.SessionCookies) == 0 || stored.SessionCookies[0].Cookie.MaxAge != -1 || stored.IdentityRevision != c.IdentityRevision {
		t.Fatal("cookie revocation not saved", err)
	}
	// Even explicit custom balance addresses cannot forward a session cookie to
	// a foreign origin. Tokens, when explicitly configured, remain separate.
	snapshot := &op.CheckinBalanceSnapshot{Config: model.CheckinBalanceConfig{BaseURL: "https://other.example"}}
	p := balanceClient(store, snapshot, *c)
	if p.cookies.Header("https://other.example/api/user/self") != "" {
		t.Fatal("foreign origin received cookie")
	}
}

func TestBalanceOptionalFailureDoesNotCancelSubmission(t *testing.T) {
	var submits atomic.Int32
	engine, _, _, _, account := httpWorkflow(t, func(w http.ResponseWriter, r *http.Request) { submits.Add(1); fmt.Fprint(w, `{"success":true}`) }, false)
	// The optional balance service may time out its child context. The check-in
	// owns a separate budget and must still submit and commit its result.
	engine.balances = &Balances{store: failedBalanceStore{BalanceStore: engine.balances.store}, sessions: engine.sessions}
	result, err := engine.Execute(context.Background(), account.ID, model.CheckinTriggerManual)
	if err != nil || result.Status != model.SiteExecutionStatusSuccess || submits.Load() != 1 {
		t.Fatalf("balance failure changed checkin: %+v %v", result, err)
	}
}

type failedBalanceStore struct{ BalanceStore }

func (s failedBalanceStore) BeginBalance(ctx context.Context, id int) (*op.CheckinBalanceSnapshot, error) {
	return nil, context.DeadlineExceeded
}

func TestBalanceBuiltInPrimarySessionAndAgentRouter(t *testing.T) {
	for _, platform := range []model.SitePlatform{model.SitePlatformNewAPI, model.SitePlatformAgentRouter} {
		t.Run(string(platform), func(t *testing.T) {
			var submits, balances atomic.Int32
			credential := model.CheckinCredential{Type: model.CheckinCredentialPassword, Username: "user", Password: "password", AccessToken: "original"}
			f := builtinFixture(t, platform, credential, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/user/self":
					balances.Add(1)
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "rotated", Path: "/"})
					quota := 500000
					if submits.Load() > 0 {
						quota = 750000
					}
					fmt.Fprintf(w, `{"success":true,"data":{"id":42,"quota":%d,"used_quota":0,"today_income":250000}}`, quota)
				case "/api/user/checkin":
					if r.Header.Get("Cookie") != "session=rotated" || r.Header.Get("New-API-User") != "42" {
						t.Error("balance read session was not used for submission")
					}
					submits.Add(1)
					fmt.Fprint(w, `{"success":true}`)
				case "/api/user/login":
					submits.Add(1)
					fmt.Fprint(w, `{"success":true,"data":{"checked_in":true,"token":"issued","id":42}}`)
				case "/api/status":
					fmt.Fprint(w, `{"success":true,"data":{"quota_per_unit":500000}}`)
				case "/api/log/self/":
					fmt.Fprint(w, `{"success":true,"data":{"items":[]}}`)
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
				}
			})
			var patch model.CheckinAccountPatch
			if err := json.Unmarshal([]byte(`{"balance_config":{"mode":"primary"}}`), &patch); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.PatchAccount(t.Context(), f.account.ID, patch); err != nil {
				t.Fatal(err)
			}
			result, err := f.engine.Execute(t.Context(), f.account.ID, model.CheckinTriggerManual)
			wantReward, wantReads := "0.5", int32(2)
			if platform == model.SitePlatformAgentRouter {
				wantReward, wantReads = "", 1
			}
			if err != nil || result.Status != model.SiteExecutionStatusSuccess || result.Reward != wantReward || submits.Load() != 1 || balances.Load() != wantReads {
				t.Fatalf("result=%+v err=%v submits=%d balances=%d", result, err, submits.Load(), balances.Load())
			}
		})
	}
}
