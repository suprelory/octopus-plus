package db

import (
	"github.com/suprelory/octopus-plus/internal/model"
	"testing"
)

func TestCheckinCredentialMigrationBindsSourcesAndPreservesExplicitEdits(t *testing.T) {
	conn, _ := setupCheckinMigrationDB(t)
	sourceSite := model.Site{Name: "Subscription", Kind: model.SiteKindRelay, Platform: model.SitePlatformOneAPI, BaseURL: "https://platform.example"}
	if err := conn.Create(&sourceSite).Error; err != nil {
		t.Fatal(err)
	}
	source := model.SiteAccount{SiteID: sourceSite.ID, Name: "Source", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "source-token"}
	if err := conn.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	checkin := model.Site{Name: "Rewards", Kind: model.SiteKindCheckin, Platform: sourceSite.Platform, BaseURL: sourceSite.BaseURL, LinkedSiteID: &sourceSite.ID}
	if err := conn.Create(&checkin).Error; err != nil {
		t.Fatal(err)
	}
	account := model.SiteAccount{SiteID: checkin.ID, Name: "Migrated", CredentialType: source.CredentialType, AccessToken: "old-copy", CheckinSourceAccountID: &source.ID}
	if err := conn.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := SeparateLegacySiteCheckins(conn); err != nil {
		t.Fatal(err)
	}
	var saved model.SiteAccount
	if err := conn.First(&saved, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.CredentialType != model.SiteCredentialTypeLinkedAccount || saved.LinkedAccountID == nil || *saved.LinkedAccountID != source.ID {
		t.Fatalf("source was not bound: %+v", saved)
	}
	if err := conn.Model(&saved).Update("linked_account_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := SeparateLegacySiteCheckins(conn); err != nil {
		t.Fatal(err)
	}
	saved = model.SiteAccount{}
	if err := conn.First(&saved, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.LinkedAccountID != nil {
		t.Fatal("startup restored a removed account binding")
	}
}

func TestCheckinCookieMigrationDoesNotTreatBearerAsCookie(t *testing.T) {
	conn, _ := setupCheckinMigrationDB(t)
	site := model.Site{Name: "External", Kind: model.SiteKindCheckin, Platform: model.SitePlatformAPI, BaseURL: "https://external.example", CheckinHTTPEnabled: true, CheckinHTTPPath: "/daily"}
	if err := conn.Create(&site).Error; err != nil {
		t.Fatal(err)
	}
	accounts := []model.SiteAccount{
		{SiteID: site.ID, Name: "Cookie", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "session=legacy-cookie"},
		{SiteID: site.ID, Name: "Bearer", CredentialType: model.SiteCredentialTypeAccessToken, AccessToken: "bearer-token"},
	}
	if err := conn.Create(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	if err := SeparateLegacySiteCheckins(conn); err != nil {
		t.Fatal(err)
	}
	var saved []model.SiteAccount
	if err := conn.Order("id").Find(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if saved[0].CredentialType != model.SiteCredentialTypeCookie || saved[0].Cookie != "session=legacy-cookie" || saved[1].Cookie != "" {
		t.Fatalf("incorrect cookie migration: %+v", saved)
	}
	if err := conn.Model(&saved[0]).Update("cookie", "session=edited-cookie").Error; err != nil {
		t.Fatal(err)
	}
	if err := SeparateLegacySiteCheckins(conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.First(&saved[0], saved[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved[0].Cookie != "session=edited-cookie" {
		t.Fatal("startup overwrote the edited cookie")
	}
}
