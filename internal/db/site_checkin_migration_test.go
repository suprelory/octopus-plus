package db

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func setupCheckinMigrationDB(t *testing.T) (*gorm.DB, *sqlCaptureLogger) {
	t.Helper()
	capture := &sqlCaptureLogger{}
	conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: capture})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := initializeSchema(conn); err != nil {
		t.Fatal(err)
	}
	return conn, capture
}

func TestSeparateLegacyCheckinsPreservesStateAndHistory(t *testing.T) {
	for _, archived := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "archived"}[archived], func(t *testing.T) {
			conn, capture := setupCheckinMigrationDB(t)
			now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
			next := now.Add(time.Hour)
			external := "https://separate-checkin.example/daily"
			source := model.Site{
				Name: "Legacy", Platform: model.SitePlatformOneAPI, BaseURL: "https://relay.example/prefix",
				Enabled: true, Archived: archived, CheckinMode: model.SiteCheckinModeEnabled,
				ExternalCheckinURL: &external, CheckinHTTPEnabled: true, CheckinHTTPMethod: "POST",
				CheckinHTTPPath: "/daily", CheckinHTTPBody: `{"claim":true}`,
				CheckinHTTPHeaders: []model.CustomHeader{{HeaderKey: "X-Test", HeaderValue: "test-value"}},
				CheckinTimezone:    "UTC", CheckinWindowStart: "10:00", CheckinWindowEnd: "14:00",
				CheckinVerificationStatus:      model.SiteCheckinSupportSupported,
				CheckinVerificationFingerprint: "fingerprint", CheckinVerifiedAt: &now,
				Tags: []string{"daily"}, IsPinned: true,
			}
			if err := conn.Create(&source).Error; err != nil {
				t.Fatal(err)
			}
			if archived {
				if err := conn.Model(&source).Updates(map[string]any{"enabled": false, "archived_at": now}).Error; err != nil {
					t.Fatal(err)
				}
			}
			accounts := []model.SiteAccount{
				{SiteID: source.ID, Name: "automatic", CredentialType: model.SiteCredentialTypeAccessToken,
					AccessToken: "migration-test-token", Enabled: true, AutoSync: true, AutoCheckin: true,
					RandomCheckin: true, CheckinRandomWindowMinutes: 30,
					LastCheckinAt: &now, LastCheckinSuccessAt: &now, NextAutoCheckinAt: &next,
					LastCheckinStatus: model.SiteExecutionStatusSuccess, LastCheckinMessage: "earned 2",
					LastSyncAt: &now, LastSyncStatus: model.SiteExecutionStatusSuccess, Balance: 12, TodayIncome: 2},
				{SiteID: source.ID, Name: "manual", CredentialType: model.SiteCredentialTypeAPIKey, APIKey: "manual-test-key"},
			}
			if err := conn.Create(&accounts).Error; err != nil {
				t.Fatal(err)
			}
			if err := conn.Model(&accounts[1]).Updates(map[string]any{
				"enabled": false, "auto_checkin": false, "checkin_random_window_minutes": 0,
			}).Error; err != nil {
				t.Fatal(err)
			}
			token := model.SiteToken{SiteAccountID: accounts[0].ID, Token: "relay-only-key"}
			if err := conn.Create(&token).Error; err != nil {
				t.Fatal(err)
			}
			logs := []model.SiteCheckinLog{
				{ID: 701, SiteID: source.ID, AccountID: accounts[0].ID, SiteName: source.Name,
					AccountName: accounts[0].Name, Status: model.SiteExecutionStatusSuccess, Reward: "2", BatchJobID: 700,
					StartedAt: now, FinishedAt: now},
				{ID: 702, SiteID: source.ID, AccountID: 0, SiteName: source.Name, AccountName: "deleted account"},
			}
			if err := conn.Create(&logs).Error; err != nil {
				t.Fatal(err)
			}
			if err := initializeSchema(conn); err != nil {
				t.Fatal(err)
			}
			var target model.Site
			if err := conn.Preload("Accounts", func(tx *gorm.DB) *gorm.DB { return tx.Order("id ASC") }).
				Where("kind = ? AND linked_site_id = ?", model.SiteKindCheckin, source.ID).First(&target).Error; err != nil {
				t.Fatal(err)
			}
			if target.Name != source.Name || target.BaseURL != source.BaseURL || target.ExternalCheckinURL == nil || *target.ExternalCheckinURL != external ||
				target.Enabled == archived || target.Archived != archived || (archived && target.ArchivedAt == nil) ||
				!target.CheckinHTTPEnabled || target.CheckinHTTPPath != "/daily" || target.CheckinHTTPBody != source.CheckinHTTPBody ||
				len(target.CheckinHTTPHeaders) != 1 || target.CheckinTimezone != "UTC" || target.CheckinWindowStart != "10:00" ||
				target.CheckinWindowEnd != "14:00" || target.CheckinVerificationFingerprint != "fingerprint" ||
				target.CheckinVerifiedAt == nil || !target.CheckinVerifiedAt.Equal(now) || !target.IsPinned || len(target.Tags) != 1 {
				t.Fatalf("migration lost site configuration: %+v", target)
			}
			if len(target.Accounts) != 2 {
				t.Fatalf("expected both accounts, got %d", len(target.Accounts))
			}
			automatic, manual := target.Accounts[0], target.Accounts[1]
			if automatic.ID == accounts[0].ID || automatic.AutoSync || !automatic.AutoCheckin || !automatic.Enabled ||
				automatic.AccessToken != accounts[0].AccessToken || automatic.NextAutoCheckinAt == nil || !automatic.NextAutoCheckinAt.Equal(next) ||
				automatic.LastCheckinSuccessAt == nil || !automatic.LastCheckinSuccessAt.Equal(now) || automatic.LastSyncAt != nil ||
				automatic.CheckinRandomWindowMinutes != 30 || !automatic.RandomCheckin ||
				automatic.Balance != 12 || automatic.TodayIncome != 2 || manual.Enabled || manual.AutoCheckin || manual.AutoSync ||
				manual.CheckinRandomWindowMinutes != 0 || manual.APIKey != accounts[1].APIKey {
				t.Fatalf("migration lost account settings: %+v", target.Accounts)
			}
			var savedSource model.Site
			if err := conn.Preload("Accounts.Tokens").First(&savedSource, source.ID).Error; err != nil {
				t.Fatal(err)
			}
			if savedSource.CheckinMode != model.SiteCheckinModeDisabled || savedSource.CheckinHTTPEnabled || savedSource.ExternalCheckinURL != nil ||
				savedSource.Accounts[0].AutoCheckin || savedSource.Accounts[0].NextAutoCheckinAt != nil ||
				!savedSource.Accounts[0].AutoSync || savedSource.Accounts[0].LastSyncAt == nil || len(savedSource.Accounts[0].Tokens) != 1 {
				t.Fatalf("source still checks in or lost relay data: %+v", savedSource)
			}
			var savedLog model.SiteCheckinLog
			if err := conn.First(&savedLog, logs[0].ID).Error; err != nil {
				t.Fatal(err)
			}
			if savedLog.SiteID != target.ID || savedLog.AccountID != automatic.ID || savedLog.SiteName != source.Name ||
				savedLog.Reward != "2" || savedLog.BatchJobID != 700 || !savedLog.StartedAt.Equal(now) {
				t.Fatalf("history lost identity or metadata: %+v", savedLog)
			}
			var deletedLog model.SiteCheckinLog
			if err := conn.First(&deletedLog, logs[1].ID).Error; err != nil || deletedLog.SiteID != target.ID || deletedLog.AccountID != 0 {
				t.Fatalf("deleted account history was not preserved: %+v, %v", deletedLog, err)
			}
			capture.reset()
			if err := initializeSchema(conn); err != nil {
				t.Fatal(err)
			}
			for _, sql := range capture.snapshot() {
				upper := strings.ToUpper(strings.TrimSpace(sql))
				for _, prefix := range []string{"INSERT ", "UPDATE ", "DELETE ", "ALTER ", "CREATE "} {
					if strings.HasPrefix(upper, prefix) {
						t.Errorf("second startup changed migrated data: %s", sql)
					}
				}
			}
		})
	}
}

func TestSeparateLegacyCheckinsRollsBackFailure(t *testing.T) {
	conn, _ := setupCheckinMigrationDB(t)
	source := model.Site{Name: "Rollback", Platform: model.SitePlatformOneAPI, BaseURL: "https://rollback.example", CheckinMode: model.SiteCheckinModeEnabled}
	if err := conn.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	account := model.SiteAccount{SiteID: source.ID, Name: "account", AutoCheckin: true}
	if err := conn.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	entry := model.SiteCheckinLog{ID: 42, SiteID: source.ID, AccountID: account.ID}
	if err := conn.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("CREATE TRIGGER fail_checkin_migration BEFORE UPDATE ON site_checkin_logs BEGIN SELECT RAISE(ABORT, 'migration failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := SeparateLegacySiteCheckins(conn); err == nil || !strings.Contains(err.Error(), "migration failure") {
		t.Fatalf("expected injected failure, got %v", err)
	}
	var sites, accounts int64
	conn.Model(&model.Site{}).Count(&sites)
	conn.Model(&model.SiteAccount{}).Count(&accounts)
	var saved model.SiteAccount
	if err := conn.First(&saved, account.ID).Error; err != nil || !saved.AutoCheckin || sites != 1 || accounts != 1 {
		t.Fatalf("failed migration left partial writes: sites=%d accounts=%d account=%+v err=%v", sites, accounts, saved, err)
	}
	if err := conn.Exec("DROP TRIGGER fail_checkin_migration").Error; err != nil {
		t.Fatal(err)
	}
	if err := SeparateLegacySiteCheckins(conn); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
}
