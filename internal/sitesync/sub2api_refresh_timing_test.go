package sitesync

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestSub2APIShortLivedTokenDoesNotRefreshAgainAfterReload(t *testing.T) {
	ctx := setupProjectTestDB(t)
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := refreshes.Add(1)
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		want := "rt_old"
		if count > 1 {
			want = fmt.Sprintf("rt_short_%d", count-1)
		}
		if body["refresh_token"] != want {
			t.Error("refresh did not use the latest token")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"access_token": fmt.Sprintf("short-access-%d", count), "refresh_token": fmt.Sprintf("rt_short_%d", count), "expires_in": 180,
		}})
	}))
	defer server.Close()
	site, account := sub2APITestAccount(t, ctx, server.URL, time.Now().Add(-time.Minute).UnixMilli())
	if _, err := ensureFreshSub2APIAccessToken(ctx, site, account, false); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		// A separate worker, or a restarted process, loads the timing from storage.
		loaded, err := op.SiteAccountGet(account.ID, ctx)
		if err != nil {
			t.Fatal(err)
		}
		token, err := ensureFreshSub2APIAccessToken(ctx, site, loaded, false)
		if err != nil || token != "short-access-1" || loaded.TokenRefreshedAt == 0 || refreshes.Load() != 1 {
			t.Fatalf("fresh 180-second token was rotated again: refreshes=%d err=%v", refreshes.Load(), err)
		}
	}
	// An actual 401 must still be able to force rotation during this interval.
	token, err := ensureFreshSub2APIAccessToken(ctx, site, account, true)
	if err != nil || token != "short-access-2" || refreshes.Load() != 2 {
		t.Fatalf("refresh scheduling blocked forced recovery: refreshes=%d err=%v", refreshes.Load(), err)
	}
}

func TestSub2APIProactiveRefreshUsesTokenLifetime(t *testing.T) {
	now := time.Now()
	jwt := func(issuedAt, expiresAt time.Time) string {
		payload := fmt.Sprintf(`{"iat":%d,"exp":%d}`, issuedAt.Unix(), expiresAt.Unix())
		return "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
	}
	for _, tc := range []struct {
		name      string
		access    string
		expiresAt int64
		refreshed int64
		want      bool
	}{
		{"imported_short_jwt", jwt(now, now.Add(3*time.Minute)), 0, 0, false},
		{"short_jwt_near_expiry", jwt(now.Add(-150*time.Second), now.Add(30*time.Second)), 0, 0, true},
		{"long_jwt_before_window", jwt(now.Add(-23*time.Hour), now.Add(time.Hour)), 0, 0, false},
		{"long_jwt_inside_window", jwt(now.Add(-24*time.Hour), now.Add(4*time.Minute)), 0, 0, true},
		{"expired_jwt_overrides_stored_expiry", jwt(now.Add(-time.Hour), now.Add(-time.Minute)), now.Add(time.Hour).UnixMilli(), 0, true},
		{"fresh_opaque_token", "opaque", now.Add(3 * time.Minute).UnixMilli(), now.UnixMilli(), false},
		{"opaque_token_near_expiry", "opaque", now.Add(30 * time.Second).UnixMilli(), now.Add(-150 * time.Second).UnixMilli(), true},
		{"legacy_expiry_without_issuance", "opaque", now.Add(4 * time.Minute).UnixMilli(), 0, true},
		{"unknown_expiry", "opaque", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &model.SiteAccount{AccessToken: tc.access, RefreshToken: "rt_test", TokenExpiresAt: tc.expiresAt, TokenRefreshedAt: tc.refreshed}
			if got := shouldProactivelyRefreshSub2API(account); got != tc.want {
				t.Fatalf("refresh=%t, want %t", got, tc.want)
			}
		})
	}
}
