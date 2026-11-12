package sitesync

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestSub2APIReplacementAccessUsesItsOwnJWTExpiry(t *testing.T) {
	for _, withRefresh := range []bool{false, true} {
		name := "without_refresh"
		if withRefresh {
			name = "with_stale_refresh"
		}
		t.Run(name, func(t *testing.T) {
			ctx := setupProjectTestDB(t)
			now := time.Now()
			expiresAt := now.Add(time.Hour).Unix()
			payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"iat":%d,"exp":%d}`, now.Unix(), expiresAt)))
			access := "header." + payload + ".signature"
			var profiles, refreshes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/auth/refresh":
					refreshes.Add(1)
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"reason":"REFRESH_TOKEN_INVALID","message":"expired refresh"}`))
				case "/api/v1/auth/me":
					profiles.Add(1)
					if r.Header.Get("Authorization") != "Bearer "+access {
						t.Error("profile did not use the replacement access token")
					}
					_, _ = w.Write([]byte(`{"code":0,"data":{"balance":5}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			site, account := sub2APITestAccount(t, ctx, server.URL, now.Add(-time.Minute).UnixMilli())
			req := &model.SiteAccountUpdateRequest{ID: account.ID, AccessToken: &access, CredentialVersion: &account.CredentialVersion}
			if !withRefresh {
				refresh := ""
				req.RefreshToken = &refresh
			}
			if _, err := op.SiteAccountUpdate(req, ctx); err != nil {
				t.Fatal(err)
			}
			saved, err := op.SiteAccountGet(account.ID, ctx)
			if err != nil {
				t.Fatal(err)
			}
			result := fetchSub2APIBalanceDetails(ctx, site, saved, saved.AccessToken)
			if !result.ok || result.balance != 5 || profiles.Load() != 1 || refreshes.Load() != 0 {
				t.Fatalf("replacement access token blocked or needlessly refreshed: profile_requests=%d refresh_requests=%d err=%v", profiles.Load(), refreshes.Load(), result.err)
			}
			if saved.TokenExpiresAt != 0 || sub2APIAccessTokenExpiresAt(saved) != expiresAt*1000 {
				t.Fatal("replacement token still uses the previous token's expiry")
			}
		})
	}
}
