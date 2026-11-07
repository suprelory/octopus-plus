package db

import (
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
)

type legacyUniqueSiteName struct {
	Name string `gorm:"unique;not null"`
}

func (legacyUniqueSiteName) TableName() string { return "sites" }

func TestSiteNameMigrationPreservesLegacyDatabase(t *testing.T) {
	conn, capture := setupCheckinMigrationDB(t)
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	// Recreate the v0.10.7 global name constraint before adding any records.
	if err := conn.Migrator().CreateConstraint(&legacyUniqueSiteName{}, "Name"); err != nil {
		t.Fatal(err)
	}
	if !conn.Migrator().HasConstraint(&legacyUniqueSiteName{}, "Name") {
		t.Fatal("legacy global name constraint is missing")
	}
	if err := conn.Migrator().CreateIndex(&model.Site{}, "Archived"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"CREATE INDEX idx_sites_base_url_upgrade ON sites (base_url)",
		"CREATE TABLE site_name_audit (site_id integer, name text)",
		"CREATE TRIGGER site_name_updated AFTER UPDATE OF name ON sites BEGIN INSERT INTO site_name_audit (site_id, name) VALUES (NEW.id, NEW.name); END",
	} {
		if err := conn.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	source := model.Site{Name: "方舟", Platform: model.SitePlatformOneAPI, BaseURL: "https://relay.example", CheckinMode: model.SiteCheckinModeDisabled}
	if err := conn.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	target := model.Site{Name: source.Name + " · 签到", Kind: model.SiteKindCheckin, LinkedSiteID: &source.ID,
		Platform: source.Platform, BaseURL: "https://rewards.example", Archived: true}
	if err := conn.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&target).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	sourceAccount := model.SiteAccount{SiteID: source.ID, Name: "Original", AccessToken: "relay-test-token"}
	if err := conn.Create(&sourceAccount).Error; err != nil {
		t.Fatal(err)
	}
	account := model.SiteAccount{SiteID: target.ID, CheckinSourceAccountID: &sourceAccount.ID, Name: "Independent",
		AccessToken: "checkin-test-token", AutoCheckin: true, Balance: 12.5}
	if err := conn.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	entry := model.SiteCheckinLog{ID: 71, SiteID: target.ID, AccountID: account.ID, SiteName: target.Name, Reward: "2.5"}
	if err := conn.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	removed := model.Site{ID: 9000, Name: "Removed", Platform: source.Platform, BaseURL: "https://removed.example"}
	if err := conn.Create(&removed).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Delete(&removed).Error; err != nil {
		t.Fatal(err)
	}
	if err := initializeSchema(conn); err != nil {
		t.Fatalf("upgrade legacy schema: %v", err)
	}
	if conn.Migrator().HasConstraint(&legacyUniqueSiteName{}, "Name") {
		t.Fatal("global name constraint was not removed")
	}
	for _, index := range []string{"idx_sites_kind_name", "idx_sites_archived", "idx_sites_base_url_upgrade"} {
		if !conn.Migrator().HasIndex(&model.Site{}, index) {
			t.Errorf("migration lost index %s", index)
		}
	}
	var saved model.Site
	if err := conn.Preload("Accounts").First(&saved, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Name != source.Name || !saved.Archived || saved.Enabled || saved.BaseURL != target.BaseURL ||
		saved.LinkedSiteID == nil || *saved.LinkedSiteID != source.ID || len(saved.Accounts) != 1 ||
		saved.Accounts[0].ID != account.ID || saved.Accounts[0].AccessToken != account.AccessToken ||
		!saved.Accounts[0].AutoCheckin || saved.Accounts[0].Balance != account.Balance {
		t.Fatalf("upgrade did not preserve the independent check-in site: %+v", saved)
	}
	var savedLog model.SiteCheckinLog
	if err := conn.First(&savedLog, entry.ID).Error; err != nil || savedLog.SiteID != target.ID ||
		savedLog.AccountID != account.ID || savedLog.SiteName != entry.SiteName || savedLog.Reward != entry.Reward {
		t.Fatalf("upgrade changed check-in history: %+v, %v", savedLog, err)
	}
	var auditedName string
	if err := conn.Table("site_name_audit").Where("site_id = ?", target.ID).Pluck("name", &auditedName).Error; err != nil || auditedName != source.Name {
		t.Fatalf("site trigger was not preserved: name=%q, err=%v", auditedName, err)
	}
	var foreignKeys, violations int
	if err := conn.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error; err != nil || foreignKeys != 1 {
		t.Fatalf("foreign key enforcement was not restored: %d, %v", foreignKeys, err)
	}
	if err := conn.Raw("SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations).Error; err != nil || violations != 0 {
		t.Fatalf("migration broke foreign keys: %d, %v", violations, err)
	}
	for _, kind := range []model.SiteKind{model.SiteKindRelay, model.SiteKindCheckin} {
		duplicate := model.Site{Name: source.Name, Kind: kind, Platform: source.Platform, BaseURL: "https://duplicate.example"}
		if err := conn.Create(&duplicate).Error; err == nil {
			t.Fatalf("duplicate %s name should still be rejected", kind)
		}
	}
	capture.reset()
	if err := initializeSchema(conn); err != nil {
		t.Fatal(err)
	}
	for _, statement := range capture.snapshot() {
		upper := strings.ToUpper(strings.TrimSpace(statement))
		for _, prefix := range []string{"CREATE ", "ALTER ", "DROP ", "UPDATE ", "INSERT ", "DELETE "} {
			if strings.HasPrefix(upper, prefix) {
				t.Errorf("second startup mutated the database: %s", statement)
			}
		}
	}
	created := model.Site{Name: "After upgrade", Platform: source.Platform, BaseURL: "https://new.example"}
	if err := conn.Create(&created).Error; err != nil || created.ID <= removed.ID {
		t.Fatalf("migration reused an old site ID: id=%d, err=%v", created.ID, err)
	}
}

func TestLegacyCheckinNamesRespectCustomNamesAndCollisions(t *testing.T) {
	for _, tt := range []struct {
		name      string
		oldName   string
		wantName  string
		unlinked  bool
		collision bool
	}{
		{name: "generated", oldName: "方舟 · 签到", wantName: "方舟"},
		{name: "old numeric suffix", oldName: "方舟 · 签到 (2)", wantName: "方舟"},
		{name: "custom name", oldName: "独立福利站", wantName: "独立福利站"},
		{name: "custom suffix", oldName: "方舟 · 签到 (备用)", wantName: "方舟 · 签到 (备用)"},
		{name: "unlinked", oldName: "方舟 · 签到", wantName: "方舟 · 签到", unlinked: true},
		{name: "same kind collision", oldName: "方舟 · 签到", wantName: "方舟 (2)", collision: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conn, _ := setupCheckinMigrationDB(t)
			source := model.Site{Name: "方舟", Platform: model.SitePlatformOneAPI, BaseURL: "https://source.example", CheckinMode: model.SiteCheckinModeDisabled}
			if err := conn.Create(&source).Error; err != nil {
				t.Fatal(err)
			}
			target := model.Site{Name: tt.oldName, Kind: model.SiteKindCheckin, Platform: source.Platform, BaseURL: "https://checkin.example"}
			if !tt.unlinked {
				target.LinkedSiteID = &source.ID
			}
			if err := conn.Create(&target).Error; err != nil {
				t.Fatal(err)
			}
			if tt.collision {
				existing := model.Site{Name: source.Name, Kind: model.SiteKindCheckin, Platform: source.Platform, BaseURL: "https://other.example"}
				if err := conn.Create(&existing).Error; err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				if err := SeparateLegacySiteCheckins(conn); err != nil {
					t.Fatal(err)
				}
				var saved model.Site
				if err := conn.First(&saved, target.ID).Error; err != nil || saved.Name != tt.wantName {
					t.Fatalf("expected name %q, got %q: %v", tt.wantName, saved.Name, err)
				}
			}
		})
	}
}
