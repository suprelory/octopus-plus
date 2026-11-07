package op

import (
	"testing"
	"time"

	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestBackupKeepsCheckinSitesIndependentAndRemapsLinks(t *testing.T) {
	ctx := setupBackupTestDB(t)
	linkedID := 80
	sourceAccountID := 81
	dump := &model.DBDump{
		Version: 1, IncludeLogs: true,
		Sites: []model.Site{
			{ID: 90, Kind: model.SiteKindCheckin, LinkedSiteID: &linkedID, Name: "Subscription", Platform: model.SitePlatformOneAPI,
				BaseURL: "https://shared.example", CheckinMode: model.SiteCheckinModeEnabled},
			{ID: 80, Kind: model.SiteKindRelay, Name: "Subscription", Platform: model.SitePlatformOneAPI, BaseURL: "https://shared.example", Enabled: true},
		},
		SiteAccounts: []model.SiteAccount{
			{ID: 91, SiteID: 90, CheckinSourceAccountID: &sourceAccountID, Name: "Manual", CredentialType: model.SiteCredentialTypeAPIKey, APIKey: "test-key", AutoSync: true},
			{ID: 81, SiteID: 80, Name: "Relay account", CredentialType: model.SiteCredentialTypeAPIKey, APIKey: "relay-key", Enabled: true, AutoSync: true},
		},
		SiteCheckinLogs: []model.SiteCheckinLog{{ID: 901, SiteID: 90, AccountID: 91, SiteName: "Subscription", AccountName: "Manual"}},
	}
	for i := 0; i < 2; i++ {
		if _, err := DBImportIncremental(ctx, dump); err != nil {
			t.Fatal(err)
		}
	}
	var sites []model.Site
	if err := dbpkg.GetDB().Preload("Accounts").Order("kind ASC").Find(&sites).Error; err != nil {
		t.Fatal(err)
	}
	if len(sites) != 2 || sites[0].Kind != model.SiteKindCheckin || sites[1].Kind != model.SiteKindRelay ||
		sites[0].Name != "Subscription" || sites[1].Name != "Subscription" ||
		sites[0].LinkedSiteID == nil || *sites[0].LinkedSiteID != sites[1].ID || sites[0].Enabled || !sites[1].Enabled {
		t.Fatalf("backup merged kinds, lost state or failed to remap forward link: %+v", sites)
	}
	if len(sites[0].Accounts) != 1 || len(sites[1].Accounts) != 1 {
		t.Fatalf("duplicate or missing accounts: %+v", sites)
	}
	account := sites[0].Accounts[0]
	if account.Enabled || account.AutoSync || account.AutoCheckin || account.CheckinRandomWindowMinutes != 0 ||
		account.CheckinSourceAccountID == nil || *account.CheckinSourceAccountID != sites[1].Accounts[0].ID {
		t.Fatalf("restoring a backup enabled disabled automation: %+v", account)
	}
	var entry model.SiteCheckinLog
	if err := dbpkg.GetDB().First(&entry, 901).Error; err != nil || entry.SiteID != sites[0].ID || entry.AccountID != account.ID {
		t.Fatalf("history was not remapped: %+v, %v", entry, err)
	}
}

func TestLegacyBackupCheckinsMigrateAndIncrementalHistoryReusesTarget(t *testing.T) {
	ctx := setupBackupTestDB(t)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	dump := &model.DBDump{
		Version: 1, IncludeLogs: true,
		Sites: []model.Site{{ID: 80, Name: "Legacy", Platform: model.SitePlatformOneAPI, BaseURL: "https://legacy.example",
			Enabled: true, CheckinMode: model.SiteCheckinModeEnabled}},
		SiteAccounts: []model.SiteAccount{{ID: 81, SiteID: 80, Name: "Account", CredentialType: model.SiteCredentialTypeAccessToken,
			AccessToken: "old-test-token", Enabled: true, AutoSync: true, AutoCheckin: true, NextAutoCheckinAt: &now}},
		SiteCheckinLogs: []model.SiteCheckinLog{{ID: 801, SiteID: 80, AccountID: 81, SiteName: "Legacy", AccountName: "Account", Reward: "5"}},
	}
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatal(err)
	}
	var target, source model.Site
	if err := dbpkg.GetDB().Preload("Accounts").Where("kind = ?", model.SiteKindCheckin).First(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.GetDB().Preload("Accounts").Where("kind = ?", model.SiteKindRelay).First(&source).Error; err != nil {
		t.Fatal(err)
	}
	if target.Name != source.Name || len(target.Accounts) != 1 || len(source.Accounts) != 1 || target.LinkedSiteID == nil || *target.LinkedSiteID != source.ID ||
		!target.Accounts[0].AutoCheckin || target.Accounts[0].AutoSync || source.Accounts[0].AutoCheckin ||
		source.CheckinMode != model.SiteCheckinModeDisabled || source.Accounts[0].NextAutoCheckinAt != nil ||
		target.Accounts[0].NextAutoCheckinAt == nil || !target.Accounts[0].NextAutoCheckinAt.Equal(now) {
		t.Fatalf("legacy automation was not separated: source=%+v target=%+v", source, target)
	}
	// Edits to the new independent account must survive subsequent old backups.
	if err := dbpkg.GetDB().Model(&target).Updates(map[string]any{"name": "Independent rewards", "base_url": "https://independent-checkin.example"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.GetDB().Model(&target.Accounts[0]).Updates(map[string]any{
		"name": "independent name", "auto_checkin": false, "access_token": "independent-token", "next_auto_checkin_at": nil,
	}).Error; err != nil {
		t.Fatal(err)
	}
	dump.SiteCheckinLogs = append(dump.SiteCheckinLogs, model.SiteCheckinLog{
		ID: 802, SiteID: 80, AccountID: 81, SiteName: "Legacy", AccountName: "Account", Reward: "7",
	})
	for i := 0; i < 2; i++ {
		if _, err := DBImportIncremental(ctx, dump); err != nil {
			t.Fatal(err)
		}
	}
	var siteCount, accountCount int64
	dbpkg.GetDB().Model(&model.Site{}).Count(&siteCount)
	dbpkg.GetDB().Model(&model.SiteAccount{}).Count(&accountCount)
	if siteCount != 2 || accountCount != 2 {
		t.Fatalf("incremental import duplicated migrated data: %d sites, %d accounts", siteCount, accountCount)
	}
	var savedSite model.Site
	if err := dbpkg.GetDB().First(&savedSite, target.ID).Error; err != nil || savedSite.Name != "Independent rewards" {
		t.Fatalf("incremental import overwrote the custom site name: %+v, %v", savedSite, err)
	}
	saved, err := SiteAccountGet(target.Accounts[0].ID, ctx)
	if err != nil || saved.AutoCheckin || saved.AccessToken != "independent-token" || saved.NextAutoCheckinAt != nil {
		t.Fatalf("incremental import overwrote independent account: %+v, %v", saved, err)
	}
	var entries []model.SiteCheckinLog
	if err := dbpkg.GetDB().Order("id ASC").Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected both history entries, got %d", len(entries))
	}
	for _, entry := range entries {
		if entry.SiteID != target.ID || entry.AccountID != saved.ID {
			t.Fatalf("legacy history points at subscription account: %+v", entry)
		}
	}
}
