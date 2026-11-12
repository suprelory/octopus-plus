package op

import (
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestSubscriptionTokenCheckinValidatesLinkedPlatformAndAccount(t *testing.T) {
	ctx := setupBackupTestDB(t)
	sourceSite := &model.Site{Name: "Sub2API", Platform: model.SitePlatformSub2API, BaseURL: "https://subscription.example"}
	otherSite := &model.Site{Name: "Other", Platform: model.SitePlatformNewAPI, BaseURL: "https://other.example"}
	for _, site := range []*model.Site{sourceSite, otherSite} {
		if err := SiteCreate(site, ctx); err != nil {
			t.Fatal(err)
		}
	}
	source := &model.SiteAccount{SiteID: sourceSite.ID, Name: "Token", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "source-token"}
	key := &model.SiteAccount{SiteID: sourceSite.ID, Name: "API key", CredentialType: model.SiteCredentialTypeAPIKey, APIKey: "source-api-key"}
	other := &model.SiteAccount{SiteID: otherSite.ID, Name: "Other account", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "other-token"}
	for _, account := range []*model.SiteAccount{source, key, other} {
		if err := SiteAccountCreate(account, ctx); err != nil {
			t.Fatal(err)
		}
	}
	site := &model.Site{Name: "Custom", Kind: model.SiteKindCheckin, Platform: model.SitePlatformSub2API, BaseURL: "https://checkin.example",
		CheckinHTTPEnabled: true, CheckinHTTPPath: "/daily", LinkedSiteID: &sourceSite.ID}
	if err := SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	enabled := true
	site, err := SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinUseSubscriptionToken: &enabled}, ctx)
	if err != nil || !site.CheckinUseSubscriptionToken {
		t.Fatalf("enable subscription token: %+v %v", site, err)
	}
	account := &model.SiteAccount{SiteID: site.ID, Name: "Token checkin", CredentialType: model.SiteCredentialTypeLinkedAccount, LinkedAccountID: &source.ID}
	if err := SiteAccountCreate(account, ctx); err != nil {
		t.Fatalf("linked token should not require a cookie: %v", err)
	}
	for _, id := range []int{key.ID, other.ID, 99999} {
		if _, err := SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: account.ID, LinkedAccountID: &id, LinkedAccountIDSet: true}, ctx); err == nil {
			t.Fatalf("accepted invalid subscription account %d", id)
		}
	}
	if _, err := SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, LinkedSiteID: &otherSite.ID, LinkedSiteIDSet: true}, ctx); err == nil {
		t.Fatal("accepted another subscription platform for token checkin")
	}
	for name, change := range map[string]func(*model.Site){
		"subscription site": func(s *model.Site) { s.Kind = model.SiteKindRelay },
		"platform":          func(s *model.Site) { s.Platform = model.SitePlatformNewAPI },
		"HTTP disabled":     func(s *model.Site) { s.CheckinHTTPEnabled = false },
		"unlinked":          func(s *model.Site) { s.LinkedSiteID = nil },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := *site
			change(&invalid)
			if err := invalid.Validate(); err == nil {
				t.Fatal("accepted invalid token checkin configuration")
			}
		})
	}
	enabled = false
	site, err = SiteUpdate(&model.SiteUpdateRequest{ID: site.ID, CheckinUseSubscriptionToken: &enabled}, ctx)
	if err != nil || site.CheckinUseSubscriptionToken {
		t.Fatalf("disable subscription token: %+v %v", site, err)
	}
	if err := validateSiteAccountCredentials(site, account, ctx); err == nil {
		t.Fatal("cookie mode accepted an account without a cookie")
	}
}

func TestSubscriptionTokenBackupRemapsPendingAccountBinding(t *testing.T) {
	ctx := setupBackupTestDB(t)
	sourceSiteID, sourceID := 1, 1
	sourceSite := model.Site{ID: sourceSiteID, Name: "Source", Kind: model.SiteKindRelay, Platform: model.SitePlatformSub2API, BaseURL: "https://subscription.example"}
	site := model.Site{ID: 2, Name: "Checkin", Kind: model.SiteKindCheckin, Platform: model.SitePlatformSub2API, BaseURL: "https://checkin.example",
		LinkedSiteID: &sourceSiteID, CheckinHTTPEnabled: true, CheckinUseSubscriptionToken: true, CheckinHTTPPath: "/daily",
		CheckinStatus: &model.SiteCheckinStatusConfig{Path: "/status", CheckedPath: "data.checked_in"}}
	if err := site.Validate(); err != nil {
		t.Fatal(err)
	}
	account := model.SiteAccount{ID: 2, SiteID: site.ID, Name: "Pending token checkin", CredentialType: model.SiteCredentialTypeLinkedAccount, LinkedAccountID: &sourceID}
	account.Normalize()
	account.CheckinPending = &model.SiteCheckinPending{LogID: 123, Date: "2026-11-12", Submitted: true,
		StartedAt: time.Now(), Deadline: time.Now().Add(time.Hour), Fingerprint: site.CheckinWorkflowFingerprint(&account)}
	dump := &model.DBDump{Version: 1, Sites: []model.Site{site, sourceSite}, SiteAccounts: []model.SiteAccount{
		account,
		{ID: sourceID, SiteID: sourceSiteID, Name: "Source token", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "source-token"},
	}}
	// Reverse ordering deliberately remaps the source account after its consumer.
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatal(err)
	}
	var restored model.SiteAccount
	if err := db.GetDB().Where("name = ?", account.Name).First(&restored).Error; err != nil {
		t.Fatal(err)
	}
	restoredSite, err := SiteGet(restored.SiteID, ctx)
	if err != nil || !restoredSite.CheckinUseSubscriptionToken || restored.LinkedAccountID == nil || *restored.LinkedAccountID == sourceID || restored.CheckinPending == nil ||
		restored.CheckinPending.Fingerprint != restoredSite.CheckinWorkflowFingerprint(&restored) {
		t.Fatalf("token workflow binding was not remapped: %+v %+v %v", restoredSite, restored, err)
	}
	_, linked, err := SiteCheckinSubscriptionTokenAccount(restoredSite, &restored, ctx)
	if err != nil || linked.Name != "Source token" || linked.AccessToken != "source-token" || restored.AccessToken != "" {
		t.Fatalf("invalid restored token source: %+v %v", linked, err)
	}
	before := restoredSite.CheckinWorkflowFingerprint(&restored)
	otherID := *restored.LinkedAccountID + 1
	restored.LinkedAccountID = &otherID
	if before == restoredSite.CheckinWorkflowFingerprint(&restored) {
		t.Fatal("changing the subscription account did not invalidate the pending workflow")
	}
}
