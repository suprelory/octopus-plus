package sitesync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"gorm.io/gorm"
)

func sub2APITestAccount(t *testing.T, ctx context.Context, baseURL string, expiresAt int64) (*model.Site, *model.SiteAccount) {
	t.Helper()
	site := &model.Site{Name: "Sub2API session", Platform: model.SitePlatformSub2API, BaseURL: baseURL,
		CustomHeader: []model.CustomHeader{{HeaderKey: "User-Agent", HeaderValue: "subscription-agent"}, {HeaderKey: "Cookie", HeaderValue: "legacy-cookie"}, {HeaderKey: "Authorization", HeaderValue: "Bearer stale-shared-token"}}}
	if err := op.SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "Session", CredentialType: model.SiteCredentialTypeAccessToken,
		AccessToken: "old-access-token", RefreshToken: "rt_old", TokenExpiresAt: expiresAt}
	if err := op.SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	key := sub2APISessionKey{conn: db.GetDB(), accountID: account.ID}
	t.Cleanup(func() { clearSub2APIFailure(key) })
	return site, account
}

func updateSub2APITestAccount(req *model.SiteAccountUpdateRequest, ctx context.Context) (*model.SiteAccount, error) {
	account, err := op.SiteAccountGet(req.ID, ctx)
	if err != nil {
		return nil, err
	}
	req.CredentialVersion = &account.CredentialVersion
	return op.SiteAccountUpdate(req, ctx)
}

func writeSub2APITestRefresh(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["refresh_token"] != "rt_old" {
		t.Errorf("unexpected refresh request body: %v", err)
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("User-Agent") != "subscription-agent" {
		t.Error("refresh did not retain only the source session's network headers")
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"new-access-token","refresh_token":"rt_new","expires_in":3600}}`))
}

func TestSub2APIConcurrentRefreshConsumesTokenOnce(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/refresh" || refreshes.Add(1) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"reason":"REFRESH_TOKEN_INVALID","message":"already consumed"}`))
			return
		}
		writeSub2APITestRefresh(t, w, r)
	}))
	defer server.Close()
	site, account := sub2APITestAccount(t, ctx, server.URL, time.Now().Add(-time.Minute).UnixMilli())
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 12 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			copy := *account
			<-start
			token, err := ensureFreshSub2APIAccessToken(ctx, site, &copy, true)
			if err != nil || token != "new-access-token" || copy.RefreshToken != "rt_new" || copy.CredentialVersion != 1 {
				t.Errorf("concurrent refresh failed: token=%q version=%d err=%v", token, copy.CredentialVersion, err)
			}
		}()
	}
	close(start)
	workers.Wait()
	saved, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || refreshes.Load() != 1 || saved.RefreshToken != "rt_new" || saved.CredentialVersion != 1 {
		t.Fatalf("refresh was not committed exactly once: count=%d err=%v", refreshes.Load(), err)
	}
}

func TestSub2APILeaseCoordinatesIndependentWorkers(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		writeSub2APITestRefresh(t, w, r)
	}))
	defer server.Close()
	site, account := sub2APITestAccount(t, ctx, server.URL, time.Now().Add(-time.Minute).UnixMilli())
	key := sub2APISessionKey{conn: db.GetDB(), accountID: account.ID}
	start := make(chan struct{})
	var workers sync.WaitGroup
	// Bypass in-process singleflight to exercise the shared database lease.
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			result, err := loadOrRefreshSub2APISession(ctx, key, site, account, true)
			if err != nil || result.account == nil || result.account.AccessToken != "new-access-token" {
				t.Errorf("independent worker failed: %v", err)
			}
		}()
	}
	close(start)
	workers.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("database lease allowed %d refreshes", refreshes.Load())
	}
}

func TestSub2APICanceledWaiterDoesNotLoseRotatedCredentials(t *testing.T) {
	ctx := setupProjectTestDB(t)
	started, reply := make(chan struct{}), make(chan struct{})
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		close(started)
		<-reply
		if r.Context().Err() != nil {
			t.Error("caller cancellation canceled an in-flight token exchange")
		}
		writeSub2APITestRefresh(t, w, r)
	}))
	defer server.Close()
	site, account := sub2APITestAccount(t, ctx, server.URL, time.Now().Add(-time.Minute).UnixMilli())
	callerCtx, cancel := context.WithCancel(ctx)
	finished := make(chan error, 1)
	go func() {
		copy := *account
		_, err := ensureFreshSub2APIAccessToken(callerCtx, site, &copy, false)
		finished <- err
	}()
	<-started
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Errorf("waiter did not cancel: %v", err)
	}
	close(reply)
	copy := *account
	token, err := ensureFreshSub2APIAccessToken(ctx, site, &copy, false)
	if err != nil || token != "new-access-token" || refreshes.Load() != 1 {
		t.Fatalf("remaining waiter lost refresh: count=%d err=%v", refreshes.Load(), err)
	}
	saved, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || saved.RefreshToken != "rt_new" || saved.TokenRefreshLease != "" {
		t.Fatalf("canceled caller lost stored session: %v", err)
	}
}

func TestSub2APISaveFailureRetriesOnlyPersistence(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		writeSub2APITestRefresh(t, w, r)
	}))
	defer server.Close()
	site, account := sub2APITestAccount(t, ctx, server.URL, time.Now().Add(-time.Minute).UnixMilli())
	conn := db.GetDB()
	var unavailable atomic.Bool
	unavailable.Store(true)
	if err := conn.Callback().Update().Before("gorm:update").Register("test:sub2api_save_failure", func(tx *gorm.DB) {
		if fields, ok := tx.Statement.Dest.(map[string]any); ok && fields["refresh_token"] != nil && unavailable.Load() {
			tx.AddError(errors.New("simulated unavailable storage"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Callback().Update().Remove("test:sub2api_save_failure") })
	copy := *account
	if token, err := ensureFreshSub2APIAccessToken(ctx, site, &copy, false); apperror.Code(err) != sub2APISessionSaveFailed || token != "" {
		t.Fatalf("save failure was hidden: token=%q err=%v", token, err)
	}
	unavailable.Store(false)
	// Expire the save-only cooldown without delaying the regression suite.
	key := sub2APISessionKey{conn: conn, accountID: account.ID}
	sub2APISessions.Lock()
	failure := sub2APISessions.failures[key]
	failure.retryAt = time.Now().Add(-time.Second)
	sub2APISessions.failures[key] = failure
	sub2APISessions.Unlock()
	token, err := ensureFreshSub2APIAccessToken(ctx, site, &copy, false)
	if err != nil || token != "new-access-token" || refreshes.Load() != 1 {
		t.Fatalf("save retry exchanged the consumed token again: count=%d err=%v", refreshes.Load(), err)
	}
	saved, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || saved.RefreshToken != "rt_new" || saved.CredentialVersion != 1 {
		t.Fatalf("save-only recovery did not persist the pair: %v", err)
	}
}

func TestSub2APIRefreshCannotOverwriteManualCredentialEdit(t *testing.T) {
	ctx := setupProjectTestDB(t)
	started, reply := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-reply
		writeSub2APITestRefresh(t, w, r)
	}))
	defer server.Close()
	site, account := sub2APITestAccount(t, ctx, server.URL, time.Now().Add(-time.Minute).UnixMilli())
	finished := make(chan error, 1)
	go func() {
		copy := *account
		token, err := ensureFreshSub2APIAccessToken(ctx, site, &copy, true)
		if err == nil && token != "manual-access" {
			err = fmt.Errorf("did not adopt manually replaced credentials")
		}
		finished <- err
	}()
	<-started
	access, refresh, expiry := "manual-access", "rt_manual", time.Now().Add(time.Hour).UnixMilli()
	_, editErr := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: account.ID, AccessToken: &access,
		RefreshToken: &refresh, TokenExpiresAt: &expiry, CredentialVersion: &account.CredentialVersion}, ctx)
	close(reply)
	if err := <-finished; err != nil || editErr != nil {
		t.Fatalf("concurrent edit: update=%v refresh=%v", editErr, err)
	}
	saved, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || saved.AccessToken != access || saved.RefreshToken != refresh || saved.CredentialVersion != 1 {
		t.Fatalf("refresh overwrote a manual edit: %v", err)
	}
	// A stale sync result must not undo either credential update.
	if err := persistSyncSnapshot(ctx, account.ID, &syncSnapshot{accessToken: "old-access-token", status: model.SiteExecutionStatusSuccess}); err != nil {
		t.Fatal(err)
	}
	if err := updateAccountSyncState(ctx, account.ID, model.SiteExecutionStatusFailed, "late result", "old-access-token"); err != nil {
		t.Fatal(err)
	}
	saved, err = op.SiteAccountGet(account.ID, ctx)
	if err != nil || saved.AccessToken != access || saved.RefreshToken != refresh {
		t.Fatalf("stale snapshot restored an old token: %v", err)
	}
}

func TestSub2APIJWTExpiryRefreshesWithoutStoredExpiry(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		writeSub2APITestRefresh(t, w, r)
	}))
	defer server.Close()
	site, account := sub2APITestAccount(t, ctx, server.URL, 0)
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(-time.Minute).Unix())))
	account.AccessToken = "header." + payload + ".signature"
	if err := db.GetDB().Model(account).Update("access_token", account.AccessToken).Error; err != nil {
		t.Fatal(err)
	}
	token, err := ensureFreshSub2APIAccessToken(ctx, site, account, false)
	if err != nil || token != "new-access-token" || refreshes.Load() != 1 {
		t.Fatalf("JWT expiration was not used for scheduling: %v", err)
	}
}

func TestSub2APIAuthRecoveryUsesStructuredReasons(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		reason      string
		wantRefresh bool
	}{
		{"expired", 401, "TOKEN_EXPIRED", true},
		{"legacy_401", 401, "", true},
		{"forbidden", 403, "FORBIDDEN", false},
		{"revoked", 401, "TOKEN_REVOKED", false},
		{"binding", 401, "SESSION_BINDING_MISMATCH", false},
		{"inactive", 401, "USER_INACTIVE", false},
		{"api_key", 401, "INVALID_API_KEY", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			var refreshes, queries atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/refresh" {
					refreshes.Add(1)
					writeSub2APITestRefresh(t, w, r)
					return
				}
				queries.Add(1)
				if r.Header.Get("Authorization") == "Bearer new-access-token" {
					_, _ = w.Write([]byte(`{"code":0,"data":{"balance":5}}`))
					return
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"reason": tc.reason, "message": "authentication rejected"})
			}))
			defer server.Close()
			site, account := sub2APITestAccount(t, ctx, server.URL, 0)
			_, err := requestSub2APIJSON(withSub2APIRecoveryBudget(ctx), site, http.MethodGet, "/api/v1/auth/me", nil, account, account.AccessToken)
			if tc.wantRefresh {
				if err != nil || refreshes.Load() != 1 || queries.Load() != 2 {
					t.Fatalf("401 recovery: refreshes=%d queries=%d err=%v", refreshes.Load(), queries.Load(), err)
				}
			} else if err == nil || refreshes.Load() != 0 || queries.Load() != 1 {
				t.Fatalf("non-refreshable failure retried: refreshes=%d queries=%d err=%v", refreshes.Load(), queries.Load(), err)
			}
		})
	}
}

func TestSub2APITransientRefreshFailureKeepsOnlyUnexpiredAccess(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"rate limited"}`))
	}))
	defer server.Close()
	site, account := sub2APITestAccount(t, ctx, server.URL, time.Now().Add(time.Minute).UnixMilli())
	for range 2 {
		copy := *account
		token, err := ensureFreshSub2APIAccessToken(ctx, site, &copy, false)
		if err != nil || token != "old-access-token" {
			t.Fatalf("transient refresh discarded a valid token: %v", err)
		}
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refresh failure was not cooled down: %d", refreshes.Load())
	}
	copy := *account
	if token, err := ensureFreshSub2APIAccessToken(ctx, site, &copy, true); token != "" || !isSub2APITransientRefreshError(err) {
		t.Fatalf("forced refresh hid failure: token=%q err=%v", token, err)
	}
}

func TestSub2APIKeyCreationRetriesOnlyExplicitAuthRejections(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		name := "jwt_rejection"
		if uncertain {
			name = "response_lost"
		}
		t.Run(name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			var requests, created, refreshes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/refresh" {
					refreshes.Add(1)
					writeSub2APITestRefresh(t, w, r)
					return
				}
				requests.Add(1)
				if r.URL.Path != "/api/v1/keys" {
					t.Error("key creation fell through to a legacy endpoint")
				}
				if uncertain {
					created.Add(1)
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				if r.Header.Get("Authorization") != "Bearer new-access-token" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"reason":"TOKEN_EXPIRED","message":"expired"}`))
					return
				}
				created.Add(1)
				_, _ = w.Write([]byte(`{"code":0,"data":{"id":10,"key":"created-key"}}`))
			}))
			defer server.Close()
			site, account := sub2APITestAccount(t, ctx, server.URL, 0)
			err := createSub2APIToken(ctx, site, account, "default", "Test key")
			if uncertain {
				if err == nil || requests.Load() != 1 || created.Load() != 1 || refreshes.Load() != 0 {
					t.Fatalf("uncertain key creation was repeated: requests=%d created=%d refreshes=%d err=%v", requests.Load(), created.Load(), refreshes.Load(), err)
				}
			} else if err != nil || requests.Load() != 2 || created.Load() != 1 || refreshes.Load() != 1 {
				t.Fatalf("explicit JWT rejection did not recover once: requests=%d created=%d refreshes=%d err=%v", requests.Load(), created.Load(), refreshes.Load(), err)
			}
		})
	}
}
