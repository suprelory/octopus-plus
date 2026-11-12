package sitesync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
)

func TestSub2APIRefreshRejectionKeepsOnlyUsableAccess(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reason  string
		expiry  time.Duration
		wantUse bool
	}{
		{"invalid_refresh", "REFRESH_TOKEN_INVALID", time.Minute, true},
		{"expired_refresh", "REFRESH_TOKEN_EXPIRED", time.Minute, true},
		{"reused_refresh", "REFRESH_TOKEN_REUSED", time.Minute, true},
		{"revoked_session", "TOKEN_REVOKED", time.Minute, false},
		{"binding_mismatch", "SESSION_BINDING_MISMATCH", time.Minute, false},
		{"inactive_user", "USER_INACTIVE", time.Minute, false},
		{"unknown_rejection", "", time.Minute, false},
		{"expired_access", "REFRESH_TOKEN_INVALID", -time.Minute, false},
		{"access_too_close_to_expiry", "REFRESH_TOKEN_INVALID", time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			var refreshes, profiles atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/auth/refresh" {
					refreshes.Add(1)
					w.WriteHeader(http.StatusUnauthorized)
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 401, "reason": tc.reason, "message": "rejected"})
					return
				}
				profiles.Add(1)
				if r.Header.Get("Authorization") != "Bearer old-access-token" {
					t.Error("profile did not use the existing access token")
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{"balance":5}}`))
			}))
			defer server.Close()
			site, account := sub2APITestAccount(t, ctx, server.URL, time.Now().Add(tc.expiry).UnixMilli())
			for range 2 {
				result := fetchSub2APIBalanceDetails(ctx, site, account, account.AccessToken)
				if result.ok != tc.wantUse {
					t.Fatalf("balance usable=%t, want %t: %v", result.ok, tc.wantUse, result.err)
				}
			}
			wantProfiles := int32(0)
			if tc.wantUse {
				wantProfiles = 2
			}
			if refreshes.Load() != 1 || profiles.Load() != wantProfiles {
				t.Fatalf("unexpected requests: refresh=%d profile=%d", refreshes.Load(), profiles.Load())
			}
		})
	}
}

func TestSub2APIRefreshFallbackStopsOnBusiness401(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var refreshes, profiles atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		if r.URL.Path == "/api/v1/auth/refresh" {
			refreshes.Add(1)
			_, _ = w.Write([]byte(`{"reason":"REFRESH_TOKEN_INVALID","message":"consumed"}`))
			return
		}
		profiles.Add(1)
		_, _ = w.Write([]byte(`{"reason":"TOKEN_EXPIRED","message":"expired"}`))
	}))
	defer server.Close()
	site, account := sub2APITestAccount(t, ctx, server.URL, time.Now().Add(time.Minute).UnixMilli())
	result := fetchSub2APIBalanceDetails(ctx, site, account, account.AccessToken)
	if result.ok || apperror.Code(result.err) != sub2APISessionInvalid || profiles.Load() != 1 || refreshes.Load() != 1 {
		t.Fatalf("401 incorrectly fell back or repeated refresh: profiles=%d refreshes=%d err=%v", profiles.Load(), refreshes.Load(), result.err)
	}
}
