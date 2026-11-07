package op

import (
	"fmt"
	"strings"
	"testing"

	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestDBImportRejectsInvalidDataWithoutPartialWrites(t *testing.T) {
	tests := []struct {
		name   string
		change func(*model.DBDump)
		want   string
	}{
		{"missing version", func(d *model.DBDump) { d.Version = 0 }, "unsupported dump version"},
		{"future version", func(d *model.DBDump) { d.Version = dbDumpVersion + 1 }, "unsupported dump version"},
		{"unsupported channel", func(d *model.DBDump) {
			d.Channels = append(d.Channels, model.Channel{ID: 2, Name: "invalid-channel", Type: outbound.OutboundType(4)})
		}, "unsupported channel type"},
		{"unset group mode", func(d *model.DBDump) { d.Groups[0].Mode = 0 }, "invalid group mode"},
		{"unsupported group mode", func(d *model.DBDump) { d.Groups[0].Mode = 2 }, "invalid group mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := setupBackupTestDB(t)
			dump := buildTestDump()
			dump.ProxyConfigurations = []model.ProxyConfiguration{{ID: 1, Name: "imported-proxy", URL: "http://127.0.0.1:8080", Enabled: true}}
			test.change(dump)
			result, err := DBImportIncremental(ctx, dump)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
			if result != nil {
				t.Fatalf("failed import returned success: %+v", result)
			}
			for _, row := range []any{&model.ProxyConfiguration{}, &model.Channel{}, &model.ChannelKey{}, &model.Site{}, &model.SiteAccount{}, &model.Group{}, &model.GroupItem{}} {
				var count int64
				if err := dbpkg.GetDB().WithContext(ctx).Model(row).Count(&count).Error; err != nil {
					t.Fatalf("count %T: %v", row, err)
				}
				if count != 0 {
					t.Errorf("failed import left %d rows in %T", count, row)
				}
			}
		})
	}
}

func TestDBImportRollsBackEarlierStagesOnFailure(t *testing.T) {
	for _, table := range []string{"stats_totals", "relay_logs"} {
		t.Run(table, func(t *testing.T) {
			ctx := setupBackupTestDB(t)
			conn := dbpkg.GetDB().WithContext(ctx)
			preserved := model.Setting{Key: "backup_rollback_test", Value: "original"}
			if err := conn.Create(&preserved).Error; err != nil {
				t.Fatalf("create existing setting: %v", err)
			}

			models := []any{
				&model.Channel{}, &model.ChannelKey{}, &model.Site{}, &model.SiteAccount{},
				&model.Group{}, &model.GroupItem{}, &model.StatsTotal{}, &model.RelayLog{},
			}
			before := make([]int64, len(models))
			for i, row := range models {
				if err := conn.Model(row).Count(&before[i]).Error; err != nil {
					t.Fatalf("count %T before import: %v", row, err)
				}
			}

			// Fail a late database write after channels, sites, groups and settings
			// have been processed, without depending on the import helper layout.
			trigger := fmt.Sprintf("CREATE TRIGGER fail_backup_import BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT, 'injected backup failure'); END", table)
			if err := conn.Exec(trigger).Error; err != nil {
				t.Fatalf("create failure trigger: %v", err)
			}
			dump := buildTestDump()
			dump.Settings = []model.Setting{{Key: preserved.Key, Value: "replaced"}}
			dump.IncludeStats = true
			dump.StatsTotal = []model.StatsTotal{{ID: 123, StatsMetrics: model.StatsMetrics{RequestSuccess: 7}}}
			dump.StatsModel = []model.StatsModel{{ID: 123, ChannelID: 1, Name: "legacy-model", StatsMetrics: model.StatsMetrics{RequestSuccess: 7}}}
			dump.IncludeLogs = true
			dump.RelayLogs = []model.RelayLog{{ID: 123, Time: 123, Success: true}}

			result, err := DBImportIncremental(ctx, dump)
			if err == nil || !strings.Contains(err.Error(), "injected backup failure") {
				t.Fatalf("expected injected import failure, got %v", err)
			}
			if result != nil {
				t.Fatalf("failed import returned a success result: %+v", result)
			}
			var legacyCount int64
			if err := conn.Model(&model.StatsModel{}).Count(&legacyCount).Error; err != nil || legacyCount != 0 {
				t.Fatalf("failed import left legacy statistics: count=%d error=%v", legacyCount, err)
			}
			for i, row := range models {
				var after int64
				if err := conn.Model(row).Count(&after).Error; err != nil {
					t.Fatalf("count %T after import: %v", row, err)
				}
				if after != before[i] {
					t.Errorf("%T leaked rows from a failed import: before=%d after=%d", row, before[i], after)
				}
			}
			var restored model.Setting
			if err := conn.Where("key = ?", preserved.Key).First(&restored).Error; err != nil {
				t.Fatalf("read existing setting: %v", err)
			}
			if restored.Value != preserved.Value {
				t.Errorf("failed import overwrote existing setting: %q", restored.Value)
			}

			if err := conn.Exec("DROP TRIGGER fail_backup_import").Error; err != nil {
				t.Fatalf("remove failure trigger: %v", err)
			}
			result, err = DBImportIncremental(ctx, dump)
			if err != nil {
				t.Fatalf("retry import after rollback: %v", err)
			}
			for table, want := range map[string]int64{"sites": 1, "site_accounts": 3, "stats_total": 1, "stats_model": 1, "relay_logs": 1} {
				if got := result.RowsAffected[table]; got != want {
					t.Errorf("retry imported %d %s rows, want %d", got, table, want)
				}
			}
		})
	}
}
