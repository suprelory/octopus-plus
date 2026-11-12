package db

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type legacySessionAccount struct {
	ID             int `gorm:"primaryKey"`
	AccessToken    string
	RefreshToken   string
	TokenExpiresAt int64
}

func (legacySessionAccount) TableName() string { return "site_accounts" }

func TestSiteAccountSessionMigrationPreservesCredentials(t *testing.T) {
	conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := conn.Migrator().CreateTable(&legacySessionAccount{}); err != nil {
		t.Fatal(err)
	}
	old := legacySessionAccount{ID: 1, AccessToken: "existing-access", RefreshToken: "existing-refresh", TokenExpiresAt: 1_900_000_000_000}
	if err := conn.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := ensureSiteAccountSessionColumns(conn); err != nil {
			t.Fatal(err)
		}
	}
	var current model.SiteAccount
	if err := conn.First(&current, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.AccessToken != old.AccessToken || current.RefreshToken != old.RefreshToken || current.TokenExpiresAt != old.TokenExpiresAt ||
		current.CredentialVersion != 0 || current.TokenRefreshLease != "" || current.TokenRefreshLeaseUntil != 0 {
		t.Fatal("session migration changed credentials or introduced an active lease")
	}
	current.TokenRefreshLease, current.TokenRefreshLeaseUntil = "runtime-only", 12345
	encoded, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "runtime-only") || strings.Contains(string(encoded), "token_refresh_lease") {
		t.Fatal("runtime refresh lease was included in an API response or backup")
	}
}
