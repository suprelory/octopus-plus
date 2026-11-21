package backup

import (
	"errors"
	"fmt"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestDBImportRejectsInvalidDataWithoutPartialWrites(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Document)
		want   error
	}{
		{"missing version", func(d *Document) { d.Version = 0 }, ErrFormat},
		{"future version", func(d *Document) { d.Version = Version + 1 }, ErrFormat},
		{"unsupported channel", func(d *Document) {
			d.Channels = append(d.Channels, model.Channel{ID: 2, Name: "invalid-channel", Type: outbound.OutboundType(4)})
		}, ErrStorage},
		{"unset group mode", func(d *Document) { d.Groups[0].Mode = 0 }, ErrStorage},
		{"unsupported group mode", func(d *Document) { d.Groups[0].Mode = 2 }, ErrStorage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			service := testService(t)
			dump := backupFixture()
			test.change(dump)
			result, err := service.Import(ctx, dump)
			if err == nil || !errors.Is(err, test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
			if result != nil {
				t.Fatalf("failed import returned success: %+v", result)
			}
			for _, row := range []any{&model.ProxyConfiguration{}, &model.Channel{}, &model.ChannelKey{}, &model.CheckinSite{}, &model.CheckinAccount{}, &model.Group{}, &model.GroupItem{}} {
				var count int64
				if err := service.conn.WithContext(ctx).Model(row).Count(&count).Error; err != nil {
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
			ctx := t.Context()
			service := testService(t)
			conn := service.conn.WithContext(ctx)
			preserved := model.Setting{Key: "backup_rollback_test", Value: "original"}
			if err := conn.Create(&preserved).Error; err != nil {
				t.Fatalf("create existing setting: %v", err)
			}

			models := []any{
				&model.Channel{}, &model.ChannelKey{}, &model.CheckinSite{}, &model.CheckinAccount{},
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
			dump := backupFixture()
			dump.Settings = []model.Setting{{Key: preserved.Key, Value: "replaced"}}
			dump.IncludeStats = true
			dump.StatsTotal = []model.StatsTotal{{ID: 123, StatsMetrics: model.StatsMetrics{RequestSuccess: 7}}}
			dump.StatsModel = []model.StatsModel{{ID: 123, ChannelID: dump.Channels[0].ID, Name: "legacy-model", StatsMetrics: model.StatsMetrics{RequestSuccess: 7}}}
			dump.IncludeLogs = true

			result, err := service.Import(ctx, dump)
			if err == nil || !errors.Is(err, ErrStorage) {
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
			result, err = service.Import(ctx, dump)
			if err != nil {
				t.Fatalf("retry import after rollback: %v", err)
			}
			for table, want := range map[string]int64{"checkin_sites": 1, "checkin_accounts": 2, "stats_total": 1, "stats_model": 1, "relay_logs": 1} {
				if got := result.RowsAffected[table]; got != want {
					t.Errorf("retry imported %d %s rows, want %d", got, table, want)
				}
			}
		})
	}
}
