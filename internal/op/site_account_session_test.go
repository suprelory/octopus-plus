package op

import (
	"testing"

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
		AccessToken: "old-access", RefreshToken: "rt_old", CredentialVersion: 99, TokenRefreshLease: "client-lease", TokenRefreshLeaseUntil: 999}
	if err := SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	if account.CredentialVersion != 0 || account.TokenRefreshLease != "" || account.TokenRefreshLeaseUntil != 0 {
		t.Fatal("account creation accepted client coordination state")
	}
	if err := db.GetDB().Model(account).Updates(map[string]any{"access_token": "fresh-access", "refresh_token": "rt_fresh", "credential_version": 1}).Error; err != nil {
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
	if err != nil || renamed.AccessToken != "fresh-access" || renamed.RefreshToken != "rt_fresh" || renamed.CredentialVersion != 1 {
		t.Fatalf("name edit changed refreshed credentials: %v", err)
	}
	version, nextAccess := int64(1), "manual-access"
	updated, err := SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: account.ID, AccessToken: &nextAccess, CredentialVersion: &version}, ctx)
	if err != nil || updated.CredentialVersion != 2 || updated.AccessToken != nextAccess || updated.RefreshToken != "rt_fresh" {
		t.Fatalf("versioned edit failed: %v", err)
	}
}
