package db

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// sqlCaptureLogger records SQL to verify that restarting does not mutate an
// existing database.
type sqlCaptureLogger struct {
	mu         sync.Mutex
	statements []string
}

func (l *sqlCaptureLogger) LogMode(logger.LogLevel) logger.Interface      { return l }
func (l *sqlCaptureLogger) Info(context.Context, string, ...interface{})  {}
func (l *sqlCaptureLogger) Warn(context.Context, string, ...interface{})  {}
func (l *sqlCaptureLogger) Error(context.Context, string, ...interface{}) {}
func (l *sqlCaptureLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statements = append(l.statements, sql)
}
func (l *sqlCaptureLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.statements))
	copy(out, l.statements)
	return out
}
func (l *sqlCaptureLogger) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statements = l.statements[:0]
}

func TestInitializeSchemaCreatesCurrentTablesAndPreservesExistingData(t *testing.T) {
	capture := &sqlCaptureLogger{}
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: capture})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := initializeSchema(gormDB); err != nil {
		t.Fatalf("initialize schema: %v", err)
	}
	for _, table := range []string{"channels", "channel_keys", "proxy_configurations", "sites", "site_accounts", "site_checkin_logs", "site_checkin_batch_jobs", "site_models", "groups", "group_items", "settings", "relay_logs", "stats_site_model_hourlies", "ws_response_affinities"} {
		if !gormDB.Migrator().HasTable(table) {
			t.Errorf("missing current table %s", table)
		}
	}
	for _, table := range []string{"migration_records", "site_prices", "site_disabled_models", "stats_models"} {
		if gormDB.Migrator().HasTable(table) {
			t.Errorf("obsolete table %s was created", table)
		}
	}
	for _, index := range []struct{ table, name string }{
		{"site_models", "idx_site_account_group_model"},
		{"site_checkin_logs", "idx_site_checkin_site_id"},
		{"site_checkin_logs", "idx_site_checkin_account_id"},
		{"site_checkin_logs", "idx_site_checkin_batch_job_id"},
		{"stats_site_model_hourlies", "idx_stats_site_model_account_hour"},
	} {
		if !gormDB.Migrator().HasIndex(index.table, index.name) {
			t.Errorf("missing current index %s", index.name)
		}
	}
	entry := model.RelayLog{ID: 1, Success: true, RequestModelName: "current-model"}
	if err := gormDB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	setting := model.Setting{Key: model.SettingKeyProjectedChannelAutoGroupEnabled, Value: "2"}
	if err := gormDB.Create(&setting).Error; err != nil {
		t.Fatal(err)
	}
	// Upgrades keep historical statistics available to the backup compatibility
	// path, even though fresh installations no longer create this table.
	if err := gormDB.Migrator().CreateTable(&model.StatsModel{}); err != nil {
		t.Fatal(err)
	}
	legacyStats := model.StatsModel{ID: 1, Name: "legacy-model", ChannelID: 7, StatsMetrics: model.StatsMetrics{RequestSuccess: 42}}
	if err := gormDB.Create(&legacyStats).Error; err != nil {
		t.Fatal(err)
	}
	capture.reset()
	if err := initializeSchema(gormDB); err != nil {
		t.Fatalf("reinitialize schema: %v", err)
	}
	for _, sql := range capture.snapshot() {
		upper := strings.ToUpper(strings.TrimSpace(sql))
		for _, mutation := range []string{"CREATE ", "ALTER ", "DROP ", "UPDATE ", "INSERT ", "DELETE "} {
			if strings.HasPrefix(upper, mutation) {
				t.Errorf("startup modified an existing database: %s", sql)
			}
		}
	}
	var saved model.RelayLog
	if err := gormDB.First(&saved, entry.ID).Error; err != nil || !saved.Success || saved.RequestModelName != entry.RequestModelName {
		t.Fatalf("existing log changed: %+v, error=%v", saved, err)
	}
	var savedSetting model.Setting
	if err := gormDB.First(&savedSetting, "key = ?", setting.Key).Error; err != nil || savedSetting.Value != setting.Value {
		t.Fatalf("existing setting changed: %+v, error=%v", savedSetting, err)
	}
	var savedLegacyStats model.StatsModel
	if err := gormDB.First(&savedLegacyStats, legacyStats.ID).Error; err != nil || savedLegacyStats != legacyStats {
		t.Fatalf("legacy statistics changed: %+v, error=%v", savedLegacyStats, err)
	}
}

func TestInitializeSchemaAddsBatchJobLinkToExistingCheckinLogs(t *testing.T) {
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := gormDB.Exec("CREATE TABLE site_checkin_logs (id integer PRIMARY KEY, site_id integer, account_id integer)").Error; err != nil {
		t.Fatal(err)
	}
	if err := initializeSchema(gormDB); err != nil {
		t.Fatalf("upgrade existing schema: %v", err)
	}
	if !gormDB.Migrator().HasColumn(&model.SiteCheckinLog{}, "BatchJobID") {
		t.Fatal("batch job link column was not added")
	}
	if !gormDB.Migrator().HasIndex(&model.SiteCheckinLog{}, "idx_site_checkin_batch_job_id") {
		t.Fatal("batch job link index was not added")
	}
}

func TestInitializeSchemaMarksUnfinishedCheckinBatchJobsInterrupted(t *testing.T) {
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := initializeSchema(gormDB); err != nil {
		t.Fatal(err)
	}
	job := model.SiteCheckinBatchJob{ID: 101, Status: model.SiteCheckinBatchJobStatusRunning, Trigger: "manual", StartedAt: time.Now().Add(-time.Minute)}
	if err := gormDB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := initializeSchema(gormDB); err != nil {
		t.Fatalf("recover unfinished task: %v", err)
	}
	var saved model.SiteCheckinBatchJob
	if err := gormDB.First(&saved, "id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != model.SiteCheckinBatchJobStatusInterrupted || saved.FinishedAt == nil || saved.ErrorMessage == "" {
		t.Fatalf("unfinished task was not marked interrupted: %+v", saved)
	}
}

func TestInitializeSchemaAddsCustomHTTPCheckinColumnsToExistingSites(t *testing.T) {
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := gormDB.Exec("CREATE TABLE sites (id integer PRIMARY KEY, name text NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := gormDB.Exec("INSERT INTO sites (id, name) VALUES (7, 'legacy')").Error; err != nil {
		t.Fatal(err)
	}
	if err := initializeSchema(gormDB); err != nil {
		t.Fatalf("upgrade existing schema: %v", err)
	}
	for _, column := range []string{"checkin_http_enabled", "checkin_http_method", "checkin_http_path", "checkin_http_body", "checkin_http_headers", "checkin_reward_extractor"} {
		if !gormDB.Migrator().HasColumn(&model.Site{}, column) {
			t.Errorf("missing migrated site column %s", column)
		}
	}
	var name string
	if err := gormDB.Raw("SELECT name FROM sites WHERE id = 7").Scan(&name).Error; err != nil || name != "legacy" {
		t.Fatalf("legacy site data changed: name=%q error=%v", name, err)
	}
}

func TestInitializeSchemaAddsCheckinCapabilityColumnsToExistingSites(t *testing.T) {
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := gormDB.Exec("CREATE TABLE sites (id integer PRIMARY KEY, name text NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := gormDB.Exec("INSERT INTO sites (id, name) VALUES (7, 'legacy')").Error; err != nil {
		t.Fatal(err)
	}
	if err := initializeSchema(gormDB); err != nil {
		t.Fatalf("upgrade existing schema: %v", err)
	}
	for _, column := range []string{"checkin_mode", "checkin_verification_status", "checkin_verification_fingerprint", "checkin_verified_at"} {
		if !gormDB.Migrator().HasColumn(&model.Site{}, column) {
			t.Errorf("missing migrated site column %s", column)
		}
	}
	var values struct {
		CheckinMode              string
		CheckinVerificationState string
	}
	if err := gormDB.Raw("SELECT checkin_mode, checkin_verification_status AS checkin_verification_state FROM sites WHERE id = 7").Scan(&values).Error; err != nil {
		t.Fatalf("read migrated checkin defaults: %v", err)
	}
	if values.CheckinMode != string(model.SiteCheckinModeAuto) {
		t.Fatalf("expected checkin mode default %q, got %q", model.SiteCheckinModeAuto, values.CheckinMode)
	}
	if values.CheckinVerificationState != string(model.SiteCheckinSupportUnknown) {
		t.Fatalf("expected checkin verification default %q, got %q", model.SiteCheckinSupportUnknown, values.CheckinVerificationState)
	}
	if err := gormDB.Model(&model.Site{}).Where("id = ?", 7).Update("checkin_mode", model.SiteCheckinModeDisabled).Error; err != nil {
		t.Fatalf("update migrated checkin mode: %v", err)
	}
	var mode string
	if err := gormDB.Raw("SELECT checkin_mode FROM sites WHERE id = 7").Scan(&mode).Error; err != nil || mode != string(model.SiteCheckinModeDisabled) {
		t.Fatalf("migrated checkin mode was not writable: value=%q error=%v", mode, err)
	}
}

func TestInitializeSchemaMarksExistingSitesAsRelay(t *testing.T) {
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := gormDB.Exec("CREATE TABLE sites (id integer PRIMARY KEY, name text NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := gormDB.Exec("INSERT INTO sites (id, name) VALUES (7, 'legacy')").Error; err != nil {
		t.Fatal(err)
	}
	if err := initializeSchema(gormDB); err != nil {
		t.Fatalf("upgrade existing schema: %v", err)
	}
	var values struct {
		Kind         string
		LinkedSiteID *int
	}
	if err := gormDB.Raw("SELECT kind, linked_site_id FROM sites WHERE id = 7").Scan(&values).Error; err != nil {
		t.Fatalf("read migrated site kind: %v", err)
	}
	if values.Kind != string(model.SiteKindRelay) || values.LinkedSiteID != nil {
		t.Fatalf("expected legacy site to be an unlinked relay site, got kind=%q linked=%v", values.Kind, values.LinkedSiteID)
	}
}
