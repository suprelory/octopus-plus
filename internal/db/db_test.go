package db

import (
	"context"
	"sync"
	"time"

	"gorm.io/gorm/logger"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
)

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

func TestRuntimeSchemaCreatesCurrentTablesAndPreservesExistingData(t *testing.T) {
	gormDB := runtimeTestDB(t)
	if err := PrepareRuntimeSchema(gormDB); err != nil {
		t.Fatalf("initialize schema: %v", err)
	}
	for _, table := range []string{"channels", "channel_keys", "proxy_configurations", "checkin_sites", "checkin_accounts", "checkin_credentials", "site_checkin_logs", "site_checkin_batch_jobs", "groups", "group_items", "settings", "relay_logs", "ws_response_affinities"} {
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
		{"site_checkin_logs", "idx_site_checkin_site_id"},
		{"site_checkin_logs", "idx_site_checkin_account_id"},
		{"site_checkin_logs", "idx_site_checkin_batch_job_id"},
	} {
		if !gormDB.Migrator().HasIndex(index.table, index.name) {
			t.Errorf("missing current index %s", index.name)
		}
	}
	entry := model.RelayLog{ID: 1, Success: true, RequestModelName: "current-model"}
	if err := gormDB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	setting := model.Setting{Key: model.SettingKeyStatsSaveInterval, Value: "2"}
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
	if err := PrepareRuntimeSchema(gormDB); err != nil {
		t.Fatalf("reinitialize schema: %v", err)
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
