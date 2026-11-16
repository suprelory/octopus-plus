package db

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The previous explicit P2 schema did not track dispatched refreshes or backoff.
type credentialBeforeDispatch struct {
	ID                     int `gorm:"primaryKey"`
	AccessToken            string
	RefreshToken           string
	TokenRefreshLease      string
	TokenRefreshLeaseUntil int64
}

func (credentialBeforeDispatch) TableName() string { return "checkin_credentials" }

func TestPrepareCheckinSchemaAddsSessionStateWithoutChangingCredentials(t *testing.T) {
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "old-p2.db")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := conn.Migrator().CreateTable(&credentialBeforeDispatch{}); err != nil {
		t.Fatal(err)
	}
	before := credentialBeforeDispatch{ID: 5, AccessToken: "access", RefreshToken: "refresh", TokenRefreshLease: "owner", TokenRefreshLeaseUntil: 42}
	if err := conn.Create(&before).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := PrepareCheckinSchema(conn); err != nil {
			t.Fatal(err)
		}
	}
	var stored model.CheckinCredential
	if err := conn.First(&stored, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != before.AccessToken || stored.RefreshToken != before.RefreshToken || stored.TokenRefreshLease != before.TokenRefreshLease || stored.TokenRefreshLeaseUntil != before.TokenRefreshLeaseUntil || stored.TokenRefreshStarted || stored.TokenRefreshRetryAt != 0 {
		t.Fatal("schema preparation modified existing credential or ownership state")
	}
	for _, field := range []string{"TokenRefreshStarted", "TokenRefreshRetryAt"} {
		if !conn.Migrator().HasColumn(&model.CheckinCredential{}, field) {
			t.Fatalf("missing new session field %s", field)
		}
	}
	for _, name := range []string{"sites", "site_accounts", "site_channel_bindings"} {
		if conn.Migrator().HasTable(name) {
			t.Fatalf("checkin schema created legacy table %s", name)
		}
	}
}
