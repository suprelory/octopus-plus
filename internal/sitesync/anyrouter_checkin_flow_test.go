package sitesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestAnyRouterLinkedCheckinReusesAndSavesSession(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var profiles, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("New-API-User") != "24680" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected authentication: user=%q bearer=%v", r.Header.Get("New-API-User"), r.Header.Get("Authorization") != "")
		}
		if r.Header.Get("Origin") == "" || r.Header.Get("Referer") == "" || r.Header.Get("Accept") == "" {
			t.Error("missing browser request headers")
		}
		wantSession := "profile-session"
		switch r.URL.Path {
		case "/api/user/self":
			n := profiles.Add(1)
			quota, used := 50_000_000, 5_000_000
			nextSession := "profile-session"
			if n == 1 {
				wantSession = "old-session"
			} else if posts.Load() > 0 {
				wantSession, nextSession = "signed-session", "final-session"
				quota, used = 62_000_000, 5_500_000
			}
			w.Header().Add("Set-Cookie", "session="+nextSession+"; Path=/; HttpOnly")
			w.Header().Add("Set-Cookie", "acw_tc=fresh-waf; Path=/")
			_, _ = fmt.Fprintf(w, `{"success":true,"data":{"id":24680,"quota":%d,"used_quota":%d,"today_income":12500000}}`, quota, used)
		case "/api/user/sign_in":
			posts.Add(1)
			if cookie, err := r.Cookie("acw_tc"); err != nil || cookie.Value != "fresh-waf" {
				t.Error("WAF cookie was not reused")
			}
			w.Header().Add("Set-Cookie", "session=signed-session; Path=/; HttpOnly")
			_, _ = w.Write([]byte(`{"ret":1,"msg":"ok"}`))
		default:
			t.Errorf("unnecessary request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
		if cookie, err := r.Cookie("session"); err != nil || cookie.Value != wantSession {
			t.Errorf("%s did not use the current session", r.URL.Path)
		}
	}))
	defer server.Close()
	site, account, source := createLinkedCheckinFixture(t, ctx, server.URL)
	if err := db.GetDB().Model(&model.Site{}).Where("id IN ?", []int{site.ID, source.SiteID}).Update("platform", model.SitePlatformAnyRouter).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Model(source).Updates(map[string]any{"access_token": "session=old-session", "platform_user_id": 24680}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := CheckinAccount(ctx, account.ID)
	if err != nil || result == nil || result.Status != model.SiteExecutionStatusSuccess || result.Reward != "25" {
		t.Fatalf("checkin result=%+v err=%v", result, err)
	}
	if posts.Load() != 1 || profiles.Load() != 3 {
		t.Fatalf("unexpected request count: posts=%d profiles=%d", posts.Load(), profiles.Load())
	}
	savedSource, err := op.SiteAccountGet(source.ID, ctx)
	if err != nil || !strings.Contains(savedSource.AccessToken, "session=final-session") || !strings.Contains(savedSource.AccessToken, "acw_tc=fresh-waf") {
		t.Fatalf("latest session was not saved: %v", err)
	}
	savedAccount, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || savedAccount.AccessToken != "" || savedAccount.Balance != 124 || savedAccount.LastCheckinSuccessAt == nil {
		t.Fatalf("linked account state: %+v, %v", savedAccount, err)
	}
}

func TestAnyRouterDiscoverUsesKnownAndActualUserID(t *testing.T) {
	for _, actualID := range []int{24680, 13579} {
		t.Run(fmt.Sprint(actualID), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("New-API-User") != "24680" {
					t.Error("configured ID was not tried first")
				}
				_, _ = fmt.Fprintf(w, `{"success":true,"data":{"id":%d}}`, actualID)
			}))
			defer server.Close()
			knownID := 24680
			account := &model.SiteAccount{PlatformUserID: &knownID}
			id, err := anyRouterDiscoverUserID(context.Background(), &model.Site{BaseURL: server.URL}, account, "session=opaque")
			if err != nil || id != actualID || firstManagedPlatformUserID(account) != actualID || calls.Load() != 1 {
				t.Fatalf("id=%d err=%v requests=%d", id, err, calls.Load())
			}
		})
	}
}

func TestAnyRouterLoginUsesReturnedUserID(t *testing.T) {
	var profiles, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/login" {
			w.Header().Add("Set-Cookie", "session=login-session; Path=/")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":24680}}`))
			return
		}
		if r.Header.Get("New-API-User") != "24680" {
			t.Error("login profile ID was ignored")
		}
		if cookie, err := r.Cookie("session"); err != nil || cookie.Value != "login-session" {
			t.Error("login cookie was not used")
		}
		switch r.URL.Path {
		case "/api/user/self":
			profiles.Add(1)
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":24680}}`))
		case "/api/user/sign_in":
			posts.Add(1)
			_, _ = w.Write([]byte(`{"code":0,"message":"ok"}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	result, _, err := checkinAnyRouter(context.Background(), &model.Site{Platform: model.SitePlatformAnyRouter, BaseURL: server.URL},
		&model.SiteAccount{CredentialType: model.SiteCredentialTypeUsernamePassword, Username: "user", Password: "password"}, nil)
	if err != nil || result == nil || result.Status != model.SiteExecutionStatusSuccess || profiles.Load() != 1 || posts.Load() != 1 {
		t.Fatalf("result=%+v err=%v profiles=%d posts=%d", result, err, profiles.Load(), posts.Load())
	}
}

func TestAnyRouterStopsUncertainOrTransientCheckin(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"rate limit", `{"message":"rate limited"}`, 429},
		{"server error", `{"message":"unavailable"}`, 503},
		{"unknown JSON", `{"data":{}}`, 200},
		{"HTML", `<html>success</html>`, 200},
		{"challenge", `<html><script src="/cdn-cgi/challenge-platform/test"></script></html>`, 403},
		{"conflicting status", `{"success":false,"code":0}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/user/self" {
					_, _ = w.Write([]byte(`{"success":true,"data":{"id":24680}}`))
					return
				}
				posts.Add(1)
				if strings.HasPrefix(tc.body, "<html>") {
					w.Header().Set("Content-Type", "text/html")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			result, _, err := checkinAnyRouter(context.Background(), &model.Site{Platform: model.SitePlatformAnyRouter, BaseURL: server.URL},
				&model.SiteAccount{CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "session=test"}, nil)
			if err == nil || result != nil || posts.Load() != 1 {
				t.Fatalf("uncertain action was repeated: result=%+v err=%v posts=%d", result, err, posts.Load())
			}
		})
	}
}

func TestAnyRouterProfileRateLimitStopsBeforeCheckin(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"rate limited"}`))
	}))
	defer server.Close()
	_, _, err := checkinAnyRouter(context.Background(), &model.Site{Platform: model.SitePlatformAnyRouter, BaseURL: server.URL},
		&model.SiteAccount{CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "opaque"}, nil)
	if siteErrorStatusCode(err) != 429 || calls.Load() != 1 {
		t.Fatalf("unexpected requests after profile rate limit: requests=%d err=%v", calls.Load(), err)
	}
}

func TestAnyRouterCheckinSuccessFormats(t *testing.T) {
	for _, tc := range []struct {
		body    string
		success bool
	}{
		{`{"ret":1}`, true}, {`{"code":0}`, true}, {`{"success":true}`, true},
		{`{"success":true,"ret":1,"code":0}`, true},
		{`{"success":false,"message":"already checked in today"}`, true},
		{`{}`, false}, {`{"code":null}`, false}, {`{"code":"0"}`, false},
		{`{"ret":1.5}`, false}, {`{"code":false}`, false}, {`{"ret":true}`, false},
		{`{"success":false,"code":0}`, false}, {`{"success":true,"code":403}`, false},
		{`{"message":"success"}`, false}, {`{"message":"not checked in today"}`, false},
	} {
		var payload map[string]any
		if err := json.Unmarshal([]byte(tc.body), &payload); err != nil {
			t.Fatal(err)
		}
		_, success := anyRouterBuildCheckinResult(payload)
		if success != tc.success {
			t.Errorf("body=%s accepted=%v", tc.body, success)
		}
	}
}

func TestAnyRouterCheckinTimeoutDoesNotResubmit(t *testing.T) {
	var posts atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/self" {
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":24680}}`))
			return
		}
		posts.Add(1)
		<-release
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err := checkinAnyRouter(ctx, &model.Site{Platform: model.SitePlatformAnyRouter, BaseURL: server.URL},
		&model.SiteAccount{CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "session=test"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || posts.Load() != 1 {
		t.Fatalf("timeout was masked or submission repeated: posts=%d err=%v", posts.Load(), err)
	}
}

func TestAnyRouterRotatedCookiesAreRedacted(t *testing.T) {
	const secret = "new-secret-from-response"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/self" {
			w.Header().Add("Set-Cookie", "session="+secret+"; Path=/")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":24680}}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"message":"could not process %s"}`, secret)
	}))
	defer server.Close()
	_, token, err := checkinAnyRouter(context.Background(), &model.Site{Platform: model.SitePlatformAnyRouter, BaseURL: server.URL},
		&model.SiteAccount{CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "session=old"}, nil)
	if err == nil || strings.Contains(err.Error()+fmt.Sprint(apperror.Params(err)), secret) || !strings.Contains(token, secret) {
		t.Fatal("rotated cookie was leaked in the error or lost from the session")
	}
}

func TestAnyRouterUnknownUserIDDoesNotEnumerateUnrelatedAccounts(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("New-API-User") != "" {
			t.Error("guessed an unrelated user ID")
		}
		_, _ = w.Write([]byte(`{"success":false,"message":"missing New-API-User"}`))
	}))
	defer server.Close()
	id, err := anyRouterDiscoverUserID(context.Background(), &model.Site{BaseURL: server.URL}, &model.SiteAccount{}, "session=opaque")
	if err != nil || id != 0 || calls.Load() != 1 {
		t.Fatalf("id=%d err=%v requests=%d", id, err, calls.Load())
	}
}
