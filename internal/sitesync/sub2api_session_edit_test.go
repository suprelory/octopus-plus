package sitesync

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"gorm.io/gorm"
)

func TestSub2APIConfigEditDuringRefreshPreservesTokenPair(t *testing.T) {
	for _, field := range []string{"expiry", "proxy", "bearer_prefix", "manual_sync"} {
		t.Run(field, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			started, reply := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-reply
				writeSub2APITestRefresh(t, w, r)
			}))
			defer server.Close()
			site, account := sub2APITestAccount(t, ctx, server.URL, time.Now().Add(time.Minute).UnixMilli())
			finished := make(chan error, 1)
			go func() {
				copy := *account
				token, err := ensureFreshSub2APIAccessToken(ctx, site, &copy, false)
				if err == nil && token != "new-access-token" {
					err = fmt.Errorf("refresh did not return the rotated access token")
				}
				finished <- err
			}()
			<-started
			name := "Edited session"
			req := &model.SiteAccountUpdateRequest{ID: account.ID, Name: &name, CredentialVersion: &account.CredentialVersion}
			switch field {
			case "expiry":
				expiry := time.Now().Add(2 * time.Minute).UnixMilli()
				req.TokenExpiresAt = &expiry
			case "proxy":
				mode := model.ProxyUsageModeDirect
				req.ProxyMode = &mode
			case "bearer_prefix":
				token := "Bearer " + account.AccessToken
				req.AccessToken = &token
			}
			var edited *model.SiteAccount
			var editErr error
			if field == "manual_sync" {
				name = account.Name
				editErr = persistSyncSnapshot(ctx, account.ID, &syncSnapshot{accessToken: account.AccessToken, credentialVersion: &account.CredentialVersion, status: model.SiteExecutionStatusSuccess})
				if editErr == nil {
					edited, editErr = op.SiteAccountGet(account.ID, ctx)
				}
			} else {
				edited, editErr = op.SiteAccountUpdate(req, ctx)
			}
			close(reply)
			refreshErr := <-finished
			if editErr != nil || refreshErr != nil {
				t.Fatalf("concurrent settings edit: edit=%v refresh=%v", editErr, refreshErr)
			}
			if edited.TokenRefreshLease == "" {
				t.Fatal("settings edit released a lease for a token still being exchanged")
			}
			saved, err := op.SiteAccountGet(account.ID, ctx)
			if err != nil {
				t.Fatal(err)
			}
			if saved.AccessToken != "new-access-token" || saved.RefreshToken != "rt_new" || saved.CredentialVersion != 2 || saved.TokenRefreshLease != "" {
				t.Fatal("settings edit lost the rotated session or left its lease active")
			}
			if saved.Name != name || saved.ProxyMode != edited.ProxyMode || saved.TokenExpiresAt <= time.Now().Add(30*time.Minute).UnixMilli() {
				t.Fatal("rotation lost the current settings or retained the old expiry hint")
			}
		})
	}
}

func TestSub2APIPendingSaveSurvivesConfigEdit(t *testing.T) {
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
	if err := conn.Callback().Update().Before("gorm:update").Register("test:pending_settings_edit", func(tx *gorm.DB) {
		if fields, ok := tx.Statement.Dest.(map[string]any); ok && fields["refresh_token"] != nil && unavailable.Load() {
			tx.AddError(errors.New("simulated unavailable storage"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Callback().Update().Remove("test:pending_settings_edit") })
	if _, err := ensureFreshSub2APIAccessToken(ctx, site, account, false); apperror.Code(err) != sub2APISessionSaveFailed {
		t.Fatalf("expected a failed save: %v", err)
	}
	unavailable.Store(false)
	expiry := time.Now().Add(-time.Second).UnixMilli()
	if _, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: account.ID, TokenExpiresAt: &expiry, CredentialVersion: &account.CredentialVersion}, ctx); err != nil {
		t.Fatal(err)
	}
	key := sub2APISessionKey{conn: conn, accountID: account.ID}
	sub2APISessions.Lock()
	failure := sub2APISessions.failures[key]
	failure.retryAt = time.Now().Add(-time.Second)
	sub2APISessions.failures[key] = failure
	sub2APISessions.Unlock()
	token, err := ensureFreshSub2APIAccessToken(ctx, site, account, false)
	if err != nil || token != "new-access-token" || refreshes.Load() != 1 || account.CredentialVersion != 2 {
		t.Fatalf("settings edit lost a pending pair: refreshes=%d version=%d err=%v", refreshes.Load(), account.CredentialVersion, err)
	}
}

func TestSub2APIConfigEditDoesNotSkipForcedRefresh(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		writeSub2APITestRefresh(t, w, r)
	}))
	defer server.Close()
	site, account := sub2APITestAccount(t, ctx, server.URL, time.Now().Add(time.Hour).UnixMilli())
	expiry := time.Now().Add(2 * time.Hour).UnixMilli()
	if _, err := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: account.ID, TokenExpiresAt: &expiry, CredentialVersion: &account.CredentialVersion}, ctx); err != nil {
		t.Fatal(err)
	}
	// The rejected request still holds the account snapshot from before the edit.
	token, err := ensureFreshSub2APIAccessToken(ctx, site, account, true)
	if err != nil || token != "new-access-token" || refreshes.Load() != 1 {
		t.Fatalf("settings version was mistaken for a newer token: refreshes=%d err=%v", refreshes.Load(), err)
	}
}

func TestSub2APIConfigEditDoesNotHideRefreshFailure(t *testing.T) {
	ctx := setupProjectTestDB(t)
	started, reply := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-reply
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	site, account := sub2APITestAccount(t, ctx, server.URL, time.Now().Add(time.Minute).UnixMilli())
	finished := make(chan error, 1)
	go func() {
		copy := *account
		_, err := ensureFreshSub2APIAccessToken(ctx, site, &copy, true)
		finished <- err
	}()
	<-started
	expiry := time.Now().Add(2 * time.Minute).UnixMilli()
	_, editErr := op.SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: account.ID, TokenExpiresAt: &expiry, CredentialVersion: &account.CredentialVersion}, ctx)
	close(reply)
	if err := <-finished; editErr != nil || !isSub2APITransientRefreshError(err) {
		t.Fatalf("settings edit hid refresh failure: edit=%v refresh=%v", editErr, err)
	}
	saved, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil || saved.TokenRefreshLease != "" {
		t.Fatalf("settings version prevented release of the failed refresh lease: %v", err)
	}
}
