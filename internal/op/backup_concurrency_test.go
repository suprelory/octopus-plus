package op

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func setupRestoreConcurrency(t *testing.T) (context.Context, *model.Channel, model.APIKey, *model.DBDump) {
	t.Helper()
	ctx := setupChannelSupportTestDB(t)
	if err := InitCache(); err != nil {
		t.Fatal(err)
	}
	channel := createLifecycleChannel(t, ctx)
	key := model.APIKey{Name: "restore-key", APIKey: "test-only-restore-key", Enabled: true}
	if err := APIKeyCreate(&key, ctx); err != nil {
		t.Fatal(err)
	}
	if err := StatsRecordRequest(ctx, key.ID, channel.ID, model.StatsMetrics{InputCost: 4}, true, nil, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	metrics := model.StatsMetrics{InputCost: 100}
	dump := &model.DBDump{Version: dbDumpVersion, IncludeStats: true,
		Channels: []model.Channel{*channel}, APIKeys: []model.APIKey{key},
		StatsTotal:   []model.StatsTotal{{ID: 1, StatsMetrics: metrics}},
		StatsDaily:   []model.StatsDaily{{Date: now.Format("20060102"), StatsMetrics: metrics}},
		StatsHourly:  []model.StatsHourly{{Hour: now.Hour(), Date: now.Format("20060102"), StatsMetrics: metrics}},
		StatsChannel: []model.StatsChannel{{ChannelID: channel.ID, StatsMetrics: metrics}},
		StatsAPIKey:  []model.StatsAPIKey{{APIKeyID: key.ID, StatsMetrics: metrics}},
	}
	return ctx, channel, key, dump
}

func assertRestoredCost(t *testing.T, channelID, keyID int, want float64) {
	t.Helper()
	for name, got := range map[string]float64{
		"total": StatsTotalGet().InputCost, "daily": StatsTodayGet().InputCost,
		"channel": StatsChannelGet(channelID).InputCost, "API key": StatsAPIKeyGet(keyID).InputCost,
	} {
		if got != want {
			t.Errorf("%s cost=%v, want %v", name, got, want)
		}
	}
	hourly := StatsHourlyGet()
	if got := hourly[len(hourly)-1].InputCost; got != want {
		t.Errorf("hourly cost=%v, want %v", got, want)
	}
}

func TestRestorePublishesStatsBeforeReturning(t *testing.T) {
	ctx, channel, key, dump := setupRestoreConcurrency(t)
	// An old rollover retry must not overwrite the imported day afterward.
	enqueuePendingDailyOverride(model.StatsDaily{Date: time.Now().Format("20060102"), StatsMetrics: model.StatsMetrics{InputCost: 4}})
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatal(err)
	}
	// This is the exact historical failure window: real periodic flush before
	// the HTTP/WebDAV caller's follow-up cache refresh.
	if err := StatsSaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	assertRestoredCost(t, channel.ID, key.ID, 100)
	if err := StatsRecordRequest(ctx, key.ID, channel.ID, model.StatsMetrics{InputCost: 1}, true, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := RefreshCacheAfterImport(true); err != nil {
		t.Fatal(err)
	}
	assertRestoredCost(t, channel.ID, key.ID, 101)
	if err := StatsSaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	var persisted model.StatsTotal
	if err := db.GetDB().First(&persisted, 1).Error; err != nil || persisted.InputCost != 101 {
		t.Fatalf("restored stats were overwritten: %+v, %v", persisted, err)
	}
}

func TestRestoreOrdersConcurrentSettlementAndFlush(t *testing.T) {
	ctx, channel, key, dump := setupRestoreConcurrency(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	callback := "test:pause_stats_import"
	if err := db.GetDB().Callback().Create().After("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "stats_totals" {
			once.Do(func() { close(entered); <-release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.GetDB().Callback().Create().Remove(callback) })
	imported := make(chan error, 1)
	go func() { _, err := DBImportIncremental(ctx, dump); imported <- err }()
	<-entered
	settled, flushed := make(chan error, 1), make(chan error, 1)
	go func() {
		settled <- StatsRecordRequest(ctx, key.ID, channel.ID, model.StatsMetrics{InputCost: 1}, true, nil, "")
	}()
	go func() { flushed <- StatsSaveDB(ctx) }()
	close(release)
	for _, done := range []chan error{imported, settled, flushed} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	assertRestoredCost(t, channel.ID, key.ID, 101)
	if err := StatsSaveDB(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreReadFailureRollsBackDatabaseAndCaches(t *testing.T) {
	ctx, channel, key, dump := setupRestoreConcurrency(t)
	if err := StatsSaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected snapshot read failure")
	callback := "test:fail_import_snapshot"
	if err := db.GetDB().Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "stats_api_keys" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.GetDB().Callback().Query().Remove(callback) })
	if _, err := DBImportIncremental(ctx, dump); !errors.Is(err, injected) {
		t.Fatalf("snapshot failure lost: %v", err)
	}
	assertRestoredCost(t, channel.ID, key.ID, 4)
	var persisted model.StatsTotal
	if err := db.GetDB().First(&persisted, 1).Error; err != nil || persisted.InputCost != 4 {
		t.Fatalf("failed restore changed persisted stats: %+v, %v", persisted, err)
	}
}

func TestConfigurationOnlyRestorePreservesPendingStatsAndKeyRuntime(t *testing.T) {
	ctx, channel, key, dump := setupRestoreConcurrency(t)
	dump.IncludeStats = false
	if err := ChannelKeyUpdateWithDelta(channel.Keys[0], 7.25); err != nil {
		t.Fatal(err)
	}
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatal(err)
	}
	if err := RefreshCacheAfterImport(false); err != nil {
		t.Fatal(err)
	}
	assertRestoredCost(t, channel.ID, key.ID, 4)
	if err := StatsSaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ChannelKeySaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	var persisted model.ChannelKey
	if err := db.GetDB().First(&persisted, channel.Keys[0].ID).Error; err != nil || persisted.TotalCost != 7.25 {
		t.Fatalf("configuration restore lost key runtime: %+v, %v", persisted, err)
	}
}
