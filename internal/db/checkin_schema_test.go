package db

import (
	"path/filepath"
	"testing"
	"time"

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
	for _, field := range []string{"TokenRefreshStarted", "TokenRefreshRetryAt", "SessionCookies", "BalanceObservation"} {
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

type accountBeforeWorkflowVersion struct {
	ID             int                       `gorm:"primaryKey"`
	CheckinPending *model.SiteCheckinPending `gorm:"type:text;serializer:json"`
}

func (accountBeforeWorkflowVersion) TableName() string { return "checkin_accounts" }

func TestPrepareCheckinSchemaPreservesSubmittedWorkflowAndHistory(t *testing.T) {
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "workflow-upgrade.db")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	for _, table := range []any{&accountBeforeWorkflowVersion{}, &model.SiteCheckinLog{}} {
		if err := conn.Migrator().CreateTable(table); err != nil {
			t.Fatal(err)
		}
	}
	before := accountBeforeWorkflowVersion{ID: 5, CheckinPending: &model.SiteCheckinPending{
		LogID: 42, Date: "2026-11-17", Fingerprint: "original", Submitted: true, BeforeUnchecked: true,
		StartedAt: time.Now().UTC(), Deadline: time.Now().UTC().Add(time.Hour), Status: model.SiteExecutionStatusPending,
	}}
	entry := model.SiteCheckinLog{ID: 42, SiteID: 3, AccountID: before.ID, SiteName: "historical site", AccountName: "historical account",
		Platform: model.SitePlatformAPI, Source: "manual", Status: model.SiteExecutionStatusPending, Reason: "pending_confirmation"}
	if err := conn.Create(&before).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := PrepareCheckinSchema(conn); err != nil {
			t.Fatal(err)
		}
	}
	if !conn.Migrator().HasColumn(&model.CheckinAccount{}, "CheckinWorkflowVersion") {
		t.Fatal("workflow version column was not added")
	}
	for _, field := range []string{"BalanceVersion", "BalanceConfigVersion", "BalanceUpdatedAt"} {
		if !conn.Migrator().HasColumn(&model.CheckinAccount{}, field) {
			t.Fatalf("missing balance column %s", field)
		}
	}
	for _, table := range []any{&model.CheckinCompletion{}, &model.CheckinNotificationDelivery{}, &model.CheckinNotificationCooldown{}, &model.CheckinNotificationGate{}} {
		if !conn.Migrator().HasTable(table) {
			t.Fatalf("missing notification table %T", table)
		}
	}
	var account model.CheckinAccount
	if err := conn.First(&account, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if account.CheckinWorkflowVersion != 0 || account.CheckinPending == nil || *account.CheckinPending != *before.CheckinPending {
		t.Fatal("schema upgrade changed the submitted workflow or its authorization")
	}
	var stored model.CheckinLog
	if err := conn.First(&stored, entry.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored != entry || conn.Migrator().HasTable("checkin_logs") {
		t.Fatal("new history model did not preserve the existing log table and snapshots")
	}
}

func TestPrepareCheckinSchemaPreservesBatchHistoryAndActiveLease(t *testing.T) {
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "batch-upgrade.db")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := conn.Migrator().CreateTable(&model.SiteCheckinBatchJob{}); err != nil {
		t.Fatal(err)
	}
	history := model.SiteCheckinBatchJob{ID: 42, Status: model.SiteCheckinBatchJobStatusCompleted,
		Trigger: "manual", SiteIDs: []int{3, 4}, Total: 2, Attempted: 2, Success: 1, Pending: 1,
		StartedAt: time.Now().UTC()}
	if err := conn.Create(&history).Error; err != nil {
		t.Fatal(err)
	}
	if err := PrepareCheckinSchema(conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&model.CheckinBatchLease{}).Where("id = 1").Updates(map[string]any{"job_id": 99, "until_milli": 12345}).Error; err != nil {
		t.Fatal(err)
	}
	if err := PrepareCheckinSchema(conn); err != nil {
		t.Fatal(err)
	}
	var lease model.CheckinBatchLease
	if err := conn.First(&lease, 1).Error; err != nil || lease.JobID != 99 || lease.UntilMilli != 12345 {
		t.Fatalf("schema preparation reset worker ownership: %+v, %v", lease, err)
	}
	var stored model.CheckinBatchJob
	if err := conn.First(&stored, history.ID).Error; err != nil || stored.Success != 1 || stored.Pending != 1 || len(stored.SiteIDs) != 2 || stored.SiteIDs[1] != 4 {
		t.Fatalf("batch history or selection lost in upgrade: %+v, %v", stored, err)
	}
	if conn.Migrator().HasTable("checkin_batch_jobs") {
		t.Fatal("batch model created a second history table")
	}
}
