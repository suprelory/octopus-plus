package checkin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"gorm.io/gorm"
)

type platformFixture struct {
	engine  *Engine
	store   *op.CheckinStore
	conn    *gorm.DB
	site    *model.CheckinSite
	account *model.CheckinAccount
}

func builtinFixture(t *testing.T, platform model.SitePlatform, credential model.CheckinCredential, handler http.HandlerFunc) platformFixture {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store, conn := sessionStore(t)
	site, err := store.CreateSite(t.Context(), model.CheckinSite{Name: "site", Platform: platform, Enabled: true, BaseURL: server.URL, CheckinTimezone: "UTC",
		CustomHeader: []model.CustomHeader{{HeaderKey: "Authorization", HeaderValue: "unused site token"}, {HeaderKey: "Cookie", HeaderValue: "session=unused"}, {HeaderKey: "New-API-User", HeaderValue: "999"}}})
	if err != nil {
		t.Fatal(err)
	}
	credential.Name, credential.Platform, credential.AuthBaseURL = "credential", platform, server.URL
	account, err := store.CreateAccount(t.Context(), model.CheckinAccount{SiteID: site.ID, Name: "account", Enabled: true, AuthMode: model.CheckinAuthPlatform}, &credential)
	if err != nil {
		t.Fatal(err)
	}
	return platformFixture{NewEngine(store, testSessions(store, NewHTTPAuthenticator(store)), store), store, conn, site, account}
}

func (f platformFixture) restart() *Engine {
	store := op.NewCheckinStore(f.conn)
	return NewEngine(store, testSessions(store, NewHTTPAuthenticator(store)), store)
}

func TestPlatformAgentRouterOnlyReservedLoginAndDatedLogConfirm(t *testing.T) {
	var logins, reads atomic.Int32
	var confirmed atomic.Bool
	var stamp atomic.Int64
	var store *op.CheckinStore
	var accountID int
	f := builtinFixture(t, model.SitePlatformAgentRouter, model.CheckinCredential{Type: model.CheckinCredentialPassword, Username: "user", Password: "fixture-pass",
		AuthHeaders: []model.CustomHeader{{HeaderKey: "X-Auth-Only", HeaderValue: "login"}}}, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/user/login":
			logins.Add(1)
			account, err := store.GetAccount(req.Context(), accountID)
			if err != nil || account.CheckinPending == nil || !account.CheckinPending.Submitted {
				t.Error("login escaped its durable reservation")
			}
			var credentials map[string]string
			if req.Method != http.MethodPost || req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" || req.Header.Get("X-Auth-Only") != "login" || json.NewDecoder(req.Body).Decode(&credentials) != nil || credentials["password"] != "fixture-pass" {
				t.Error("invalid explicit login")
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "issued", Path: "/api"})
			fmt.Fprint(w, `{"success":true,"data":{"id":23,"quota":999,"checked_in":false}}`)
		case "/api/log/self/":
			reads.Add(1)
			if req.Method != http.MethodGet || req.Header.Get("Cookie") != "session=issued" || req.Header.Get("New-API-User") != "23" || req.Header.Get("X-Auth-Only") != "" || req.Header.Get("Authorization") != "" {
				t.Error("log read used wrong credential or headers")
			}
			since, _ := strconv.ParseInt(req.URL.Query().Get("start_timestamp"), 10, 64)
			until, _ := strconv.ParseInt(req.URL.Query().Get("end_timestamp"), 10, 64)
			items := []any{map[string]any{"type": 4, "content": "daily checkin success", "created_at": since - 1}, map[string]any{"type": 4, "content": "其他入账", "created_at": until}}
			if confirmed.Load() {
				if stamp.Load() < since || stamp.Load() > until {
					t.Error("confirmation selected the wrong calendar day")
				}
				items = append(items, map[string]any{"type": 4, "content": "每日签到成功", "created_at": stamp.Load()})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "items": items})
		default:
			t.Errorf("unexpected request %s", req.URL.Path)
		}
	})
	store, accountID = f.store, f.account.ID
	if _, err := f.engine.sessions.Ensure(t.Context(), f.account.CredentialID); !errors.Is(err, ErrLoginRequiresCheckin) {
		t.Fatalf("implicit login: %v", err)
	}
	pending, err := f.engine.Execute(t.Context(), accountID, model.CheckinTriggerManual)
	if err != nil || pending.Status != model.SiteExecutionStatusPending || !pending.Submitted || pending.Reward != "" {
		t.Fatalf("login alone was accepted as a check-in: %+v %v", pending, err)
	}
	account, err := store.GetAccount(t.Context(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate recovery after midnight, with an older check-in on the original day.
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	noon := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 12, 0, 0, 0, time.UTC)
	account.CheckinPending.Date = noon.Format(time.DateOnly)
	account.CheckinPending.StartedAt = noon.Add(time.Hour)
	account.CheckinPending.Deadline = noon.Add(2 * time.Hour)
	if err := f.conn.Select("checkin_pending").Updates(account).Error; err != nil {
		t.Fatal(err)
	}
	stamp.Store(noon.Unix())
	confirmed.Store(true)
	result, err := f.restart().Confirm(t.Context(), accountID)
	if err != nil || result.Status != model.SiteExecutionStatusSuccess || result.Reason != model.SiteCheckinReasonAlreadyCheckedIn || result.Reward != "" || result.LogID != pending.LogID || logins.Load() != 1 || reads.Load() != 2 {
		t.Fatalf("restart confirmation: %+v %v login=%d reads=%d", result, err, logins.Load(), reads.Load())
	}
}

func TestPlatformConcurrentConfirmationPreservesDirectAgentRouterEvidence(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var dispatched atomic.Int32
	f := builtinFixture(t, model.SitePlatformAgentRouter, model.CheckinCredential{Type: model.CheckinCredentialPassword, Username: "user", Password: "pass"}, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/user/login" {
			t.Error("unexpected request")
			return
		}
		if dispatched.Add(1) == 1 {
			close(arrived)
		}
		<-release
		fmt.Fprint(w, `{"success":true,"data":{"checked_in":true}}`)
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	done := make(chan error, 1)
	go func() {
		res, err := f.engine.Execute(t.Context(), f.account.ID, model.CheckinTriggerManual)
		if err == nil && (res.Status != model.SiteExecutionStatusSuccess || res.Reward != "") {
			err = errors.New("direct confirmation lost")
		}
		done <- err
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("login never started")
	}
	pending, err := f.restart().Confirm(t.Context(), f.account.ID)
	if err != nil || pending.Status != model.SiteExecutionStatusPending || !pending.Submitted {
		t.Fatalf("competing confirm: %+v %v", pending, err)
	}
	once.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	result, err := f.restart().Execute(t.Context(), f.account.ID, model.CheckinTriggerManual)
	if err != nil || !result.Replayed || result.Status != model.SiteExecutionStatusSuccess || result.LogID != pending.LogID || dispatched.Load() != 1 {
		t.Fatalf("one login must own the final result: %+v %v", result, err)
	}
}

func TestPlatformAnyRouterCookieRotationAcrossAccounts(t *testing.T) {
	var submits, logins, profiles atomic.Int32
	f := builtinFixture(t, model.SitePlatformAnyRouter, model.CheckinCredential{Type: model.CheckinCredentialPassword, Username: "user", Password: "pass"}, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/user/login":
			logins.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "first", Path: "/api", HttpOnly: true})
			http.SetCookie(w, &http.Cookie{Name: "log_only", Value: "scoped", Path: "/api/log/"})
			fmt.Fprint(w, `{"success":true,"data":{}}`)
		case "/api/user/self":
			visit := profiles.Add(1)
			expected := "session=first"
			expectedID := ""
			if visit == 2 {
				expected = "session=after-submit"
				expectedID = "32"
			}
			if req.Header.Get("Cookie") != expected || req.Header.Get("New-API-User") != expectedID || req.Header.Get("Authorization") != "" {
				t.Error("profile did not use the scoped session saved by its predecessor")
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "verified", Path: "/api", HttpOnly: true})
			fmt.Fprint(w, `{"success":true,"data":{"id":"32"}}`)
		case "/api/user/sign_in":
			submits.Add(1)
			if req.Method != "POST" || req.Header.Get("Cookie") != "session=verified" || req.Header.Get("New-API-User") != "32" || req.Header.Get("Authorization") != "" {
				t.Error("submission used stale cookie or identity")
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "after-submit", Path: "/api", HttpOnly: true})
			fmt.Fprint(w, `{"ret":1,"code":0,"data":{"reward":0}}`)
		default:
			t.Errorf("unexpected route %s", req.URL.Path)
		}
	})
	second, err := f.store.CreateAccount(t.Context(), model.CheckinAccount{SiteID: f.site.ID, Name: "second", CredentialID: f.account.CredentialID, AuthMode: model.CheckinAuthPlatform, Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{f.account.ID, second.ID} {
		result, err := f.restart().Execute(t.Context(), id, model.CheckinTriggerManual)
		if err != nil || result.Status != model.SiteExecutionStatusSuccess || result.Reward != "0" {
			t.Fatalf("cookie workflow: %+v %v", result, err)
		}
	}
	c, err := f.store.GetCredential(t.Context(), f.account.CredentialID)
	if err != nil || c.IdentityRevision != 0 || c.Cookie != "session=after-submit" || len(c.SessionCookies) != 2 || c.PlatformUserID == nil || *c.PlatformUserID != 32 || submits.Load() != 2 || logins.Load() != 1 || profiles.Load() != 2 {
		t.Fatal("shared session state or counts incorrect", err)
	}
}

func TestPlatformManualVerificationRecordsSubmissionBoundary(t *testing.T) {
	for _, stage := range []string{"login", "profile", "submit"} {
		t.Run(stage, func(t *testing.T) {
			var posts, attempts atomic.Int32
			c := model.CheckinCredential{Type: model.CheckinCredentialAccessToken, AccessToken: "access"}
			if stage == "login" {
				c = model.CheckinCredential{Type: model.CheckinCredentialPassword, Username: "user", Password: "pass"}
			}
			f := builtinFixture(t, model.SitePlatformAnyRouter, c, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/api/user/checkin" {
					posts.Add(1)
				}
				if stage == "submit" && req.URL.Path == "/api/user/self" {
					fmt.Fprint(w, `{"success":true,"data":{"id":32}}`)
					return
				}
				attempts.Add(1)
				w.Header().Set("Cf-Mitigated", "challenge")
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, "private-verification-body")
			})
			result, err := f.engine.Execute(t.Context(), f.account.ID, model.CheckinTriggerManual)
			sent := stage == "submit"
			if err != nil || result.Submitted != sent || result.Status != model.SiteExecutionStatusFailed || result.Reason != "verification_required" || strings.Contains(result.Message, "private-") || attempts.Load() != 1 || (posts.Load() > 0) != sent {
				t.Fatalf("wrong challenge state: %+v %v", result, err)
			}
			if sent {
				replay, err := f.restart().Execute(t.Context(), f.account.ID, model.CheckinTriggerManual)
				if err != nil || !replay.Replayed || posts.Load() != 1 {
					t.Fatal("submitted challenge was replayed", err)
				}
			}
		})
	}
}

func TestPlatformManagementDefaultsAndMissingProfile(t *testing.T) {
	for _, platform := range []model.SitePlatform{model.SitePlatformNewAPI, model.SitePlatformOneAPI, model.SitePlatformOneHub, model.SitePlatformDoneHub} {
		t.Run(string(platform), func(t *testing.T) {
			var calls atomic.Int32
			f := builtinFixture(t, platform, model.CheckinCredential{Type: model.CheckinCredentialAccessToken, AccessToken: "api-access"}, func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("Authorization") != "Bearer api-access" || req.Header.Get("Cookie") != "" || req.Header.Get("New-API-User") != "" {
					t.Error("site header replaced account identity")
				}
				switch req.URL.Path {
				case "/api/user/self":
					http.NotFound(w, req)
				case "/api/user/checkin":
					calls.Add(1)
					fmt.Fprint(w, `{"success":true,"data":{"reward":" 2.25 "}}`)
				default:
					t.Errorf("unexpected path %s", req.URL.Path)
				}
			})
			if platform == model.SitePlatformDoneHub {
				if _, err := f.engine.Execute(t.Context(), f.account.ID, model.CheckinTriggerBatch); !errors.Is(err, op.ErrCheckinUnavailable) {
					t.Fatal("unverified DoneHub auto-enabled", err)
				}
			}
			result, err := f.engine.Execute(t.Context(), f.account.ID, model.CheckinTriggerManual)
			if err != nil || result.Reward != "2.25" || calls.Load() != 1 {
				t.Fatalf("management check-in: %+v %v", result, err)
			}
			site, err := f.store.GetSite(t.Context(), f.site.ID)
			if err != nil || !site.ResolveCheckinCapability().Enabled || site.VerificationStatus != model.SiteCheckinSupportSupported {
				t.Fatal("successful verification not recorded", err)
			}
		})
	}
}

func TestPlatformAnyRouterUsesVerifiedUserAndNeverRetriesRejectedSubmission(t *testing.T) {
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"16"}`)) + ".fixture"
	var submits, profiles atomic.Int32
	f := builtinFixture(t, model.SitePlatformAnyRouter, model.CheckinCredential{Type: model.CheckinCredentialAccessToken, AccessToken: token}, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/user/self":
			profiles.Add(1)
			if req.Header.Get("New-API-User") != "16" {
				t.Error("profile did not receive the account's hint")
			}
			fmt.Fprint(w, `{"success":true,"data":{"id":24}}`)
		case "/api/user/checkin":
			submits.Add(1)
			if req.Header.Get("New-API-User") != "24" || req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "Bearer "+token {
				t.Error("submission did not use the verified identity")
			}
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"fixture-private-response"}`)
		default:
			t.Errorf("unexpected alternative submission/login %s", req.URL.Path)
		}
	})
	first, err := f.engine.Execute(t.Context(), f.account.ID, model.CheckinTriggerManual)
	if err != nil || first.Status != model.SiteExecutionStatusFailed || !first.Submitted || strings.Contains(first.Message, "private") {
		t.Fatalf("unconfirmed POST: %+v %v", first, err)
	}
	again, err := f.restart().Execute(t.Context(), f.account.ID, model.CheckinTriggerManual)
	if err != nil || !again.Replayed || again.LogID != first.LogID || submits.Load() != 1 || profiles.Load() != 1 {
		t.Fatal("rejected submission repeated", err)
	}
}

func TestPlatformAnyRouterExplicitCookieRevocationSurvivesRestart(t *testing.T) {
	var submits, profiles atomic.Int32
	f := builtinFixture(t, model.SitePlatformAnyRouter, model.CheckinCredential{Type: model.CheckinCredentialCookie, Cookie: "session=explicit"}, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/user/self":
			profiles.Add(1)
			if req.Header.Get("Cookie") != "session=explicit" {
				t.Error("lost explicit cookie")
			}
			fmt.Fprint(w, `{"success":true,"data":{"id":12}}`)
		case "/api/user/sign_in":
			submits.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "", Path: "/", MaxAge: -1})
			w.WriteHeader(http.StatusUnauthorized)
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
		}
	})
	result, err := f.engine.Execute(t.Context(), f.account.ID, model.CheckinTriggerManual)
	if err != nil || !result.Submitted || result.Status != model.SiteExecutionStatusFailed {
		t.Fatalf("revoked submission: %+v %v", result, err)
	}
	second, err := f.store.CreateAccount(t.Context(), model.CheckinAccount{SiteID: f.site.ID, Name: "second", AuthMode: model.CheckinAuthPlatform, CredentialID: f.account.CredentialID, Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err = f.restart().Execute(t.Context(), second.ID, model.CheckinTriggerManual)
	if err != nil || result.Submitted || result.Status != model.SiteExecutionStatusFailed || submits.Load() != 1 || profiles.Load() != 1 {
		t.Fatalf("revoked cookie was used after restart: %+v %v", result, err)
	}
}
