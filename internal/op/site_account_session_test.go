package op

import (
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestSub2APIAccountEditsRequireCurrentCredentialVersion(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	site := &model.Site{Name: "Versioned Sub2API", Platform: model.SitePlatformSub2API, BaseURL: "https://example.com"}
	if err := SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "Before", CredentialType: model.SiteCredentialTypeAccessToken,
		AccessToken: "old-access", RefreshToken: "rt_old", CredentialVersion: 99, TokenRefreshLease: "client-lease", TokenRefreshLeaseUntil: 999, TokenRefreshedAt: 12345}
	if err := SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	if account.CredentialVersion != 0 || account.TokenRefreshLease != "" || account.TokenRefreshLeaseUntil != 0 || account.TokenRefreshedAt != 0 {
		t.Fatal("account creation accepted client coordination state")
	}
	if err := db.GetDB().Model(account).Updates(map[string]any{"access_token": "fresh-access", "refresh_token": "rt_fresh", "credential_version": 1, "token_refreshed_at": 12345}).Error; err != nil {
		t.Fatal(err)
	}
	staleVersion := int64(0)
	staleToken := "old-access"
	for _, version := range []*int64{nil, &staleVersion} {
		_, err := SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: account.ID, AccessToken: &staleToken, CredentialVersion: version}, ctx)
		if apperror.Code(err) != CodeSiteCredentialConflict {
			t.Fatalf("stale or missing credential version accepted: %v", err)
		}
	}
	name := "Renamed without rewriting tokens"
	renamed, err := SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: account.ID, Name: &name, CredentialVersion: &staleVersion}, ctx)
	if err != nil || renamed.AccessToken != "fresh-access" || renamed.RefreshToken != "rt_fresh" || renamed.CredentialVersion != 1 || renamed.TokenRefreshedAt != 12345 {
		t.Fatalf("name edit changed refreshed credentials: %v", err)
	}
	version, nextAccess := int64(1), "manual-access"
	updated, err := SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: account.ID, AccessToken: &nextAccess, CredentialVersion: &version}, ctx)
	if err != nil || updated.CredentialVersion != 2 || updated.AccessToken != nextAccess || updated.RefreshToken != "rt_fresh" || updated.TokenRefreshedAt != 0 {
		t.Fatalf("versioned edit failed: %v", err)
	}
}

func TestSub2APIImportPreservesRefreshStateForSameSession(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	site := &model.Site{Name: "Imported Sub2API", Platform: model.SitePlatformSub2API, BaseURL: "https://example.com"}
	if err := SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "Session", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "access", RefreshToken: "rt_old"}
	if err := SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	leaseUntil, refreshedAt := time.Now().Add(time.Minute).UnixMilli(), time.Now().UnixMilli()
	if err := db.GetDB().Model(account).Updates(map[string]any{"token_refresh_lease": "active-lease", "token_refresh_lease_until": leaseUntil, "token_refreshed_at": refreshedAt}).Error; err != nil {
		t.Fatal(err)
	}
	input := importedAccountInput{Name: account.Name, CredentialType: account.CredentialType, AccessToken: account.AccessToken, RefreshToken: account.RefreshToken, TokenExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	imported, created, updated, err := upsertImportedAccount(db.GetDB(), site, input)
	if err != nil || created || !updated {
		t.Fatalf("update imported account: created=%t updated=%t err=%v", created, updated, err)
	}
	saved, err := SiteAccountGet(account.ID, ctx)
	if err != nil || saved.TokenRefreshLease != "active-lease" || saved.TokenRefreshLeaseUntil != leaseUntil || saved.TokenRefreshedAt != refreshedAt || imported.TokenRefreshLease != saved.TokenRefreshLease {
		t.Fatalf("import of same session abandoned its refresh state: %v", err)
	}
	input.AccessToken, input.RefreshToken = "replacement-access", "rt_replacement"
	if _, _, _, err := upsertImportedAccount(db.GetDB(), site, input); err != nil {
		t.Fatal(err)
	}
	saved, err = SiteAccountGet(account.ID, ctx)
	if err != nil || saved.TokenRefreshLease != "" || saved.TokenRefreshLeaseUntil != 0 || saved.TokenRefreshedAt != 0 {
		t.Fatalf("replacement session retained previous refresh state: %v", err)
	}
}
