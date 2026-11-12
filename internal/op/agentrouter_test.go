package op

import (
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestAgentRouterLinkedCheckinRequiresPasswordAccount(t *testing.T) {
	ctx := setupBackupTestDB(t)
	site := &model.Site{Name: "AgentRouter", Platform: model.SitePlatformAgentRouter, BaseURL: "https://agentrouter.example"}
	if err := SiteCreate(site, ctx); err != nil {
		t.Fatal(err)
	}
	source := &model.SiteAccount{SiteID: site.ID, Name: "Source", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "session=token"}
	if err := SiteAccountCreate(source, ctx); err != nil {
		t.Fatal(err)
	}
	checkin := &model.Site{Name: "Rewards", Kind: model.SiteKindCheckin, Platform: site.Platform, BaseURL: site.BaseURL, LinkedSiteID: &site.ID}
	if err := SiteCreate(checkin, ctx); err != nil {
		t.Fatal(err)
	}
	account := &model.SiteAccount{SiteID: checkin.ID, Name: "Linked", CredentialType: model.SiteCredentialTypeLinkedAccount, LinkedAccountID: &source.ID}
	if err := SiteAccountCreate(account, ctx); err == nil {
		t.Fatal("token-only source accepted for login-triggered checkin")
	}
	kind, user, password := model.SiteCredentialTypeUsernamePassword, "user@example.com", "password"
	if _, err := SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: source.ID, CredentialType: &kind, Username: &user, Password: &password}, ctx); err != nil {
		t.Fatal(err)
	}
	if err := SiteAccountCreate(account, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := SiteCheckinLinkedAccount(checkin, account, ctx); err != nil {
		t.Fatal(err)
	}
	kind = model.SiteCredentialTypeAccessToken
	if _, err := SiteAccountUpdate(&model.SiteAccountUpdateRequest{ID: source.ID, CredentialType: &kind}, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := SiteCheckinLinkedAccount(checkin, account, ctx); err == nil {
		t.Fatal("source credential edit did not invalidate login-triggered checkin")
	}
}

func TestAgentRouterImportRecognition(t *testing.T) {
	for _, value := range []string{"agentrouter", "agent-router", "https://agentrouter.org", "https://ps.air-outer.com"} {
		platform, ok := resolveImportedPlatform(value, value)
		if !ok || platform != model.SitePlatformAgentRouter {
			t.Fatalf("%s resolved as %s", value, platform)
		}
	}
}
