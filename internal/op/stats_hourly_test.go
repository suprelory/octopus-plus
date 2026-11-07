package op

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

const hourlyStatsTestDate = "20261029"

type legacyHourlyStats struct {
	Hour int    `gorm:"primaryKey"`
	Date string `gorm:"not null"`
	model.StatsMetrics
}

func (legacyHourlyStats) TableName() string { return "stats_hourlies" }

func TestDBImportPreservesMidnightHourlyStats(t *testing.T) {
	testHourlyStatsPersistence(t, func(ctx context.Context, rows []model.StatsHourly) error {
		_, err := DBImportIncremental(ctx, &model.DBDump{
			Version: dbDumpVersion, IncludeStats: true, StatsHourly: slices.Clone(rows),
		})
		if err != nil {
			return err
		}
		statsHourlyCacheLock.RLock()
		cached := statsHourlyCache
		statsHourlyCacheLock.RUnlock()
		for _, row := range rows {
			if cached[row.Hour] != row {
				return fmt.Errorf("import did not publish hour %d before returning: got %+v, want %+v", row.Hour, cached[row.Hour], row)
			}
		}
		return nil
	})
}

func TestStatsHourlyFlushPreservesMidnight(t *testing.T) {
	testHourlyStatsPersistence(t, func(ctx context.Context, rows []model.StatsHourly) error {
		var hourly [24]model.StatsHourly
		for _, row := range rows {
			hourly[row.Hour] = row
		}
		if len(rows) == 1 {
			// A stale cache entry must not be included in today's flush.
			hourly[1] = model.StatsHourly{Hour: 1, Date: "20261028", StatsMetrics: model.StatsMetrics{InputCost: 500}}
		}
		return persistStatsHourlySnapshot(db.GetDB().WithContext(ctx), hourly, hourlyStatsTestDate)
	})
}

func testHourlyStatsPersistence(t *testing.T, write func(context.Context, []model.StatsHourly) error) {
	t.Helper()
	for _, schema := range []string{"current", "legacy_autoincrement"} {
		t.Run(schema, func(t *testing.T) {
			for _, tc := range []struct {
				name         string
				hours        int
				seedExisting bool
			}{
				{name: "midnight_empty_table", hours: 1},
				{name: "midnight_existing_hours", hours: 1, seedExisting: true},
				{name: "full_day", hours: 24, seedExisting: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx := setupBackupTestDB(t)
					conn := db.GetDB().WithContext(ctx)
					if schema == "legacy_autoincrement" {
						if err := conn.Migrator().DropTable(&model.StatsHourly{}); err != nil {
							t.Fatal(err)
						}
						if err := conn.Migrator().CreateTable(&legacyHourlyStats{}); err != nil {
							t.Fatal(err)
						}
					}
					if err := InitCache(); err != nil {
						t.Fatal(err)
					}
					existing := model.StatsHourly{Hour: 23, Date: hourlyStatsTestDate, StatsMetrics: model.StatsMetrics{InputCost: 50}}
					if tc.seedExisting {
						if err := conn.Create(&existing).Error; err != nil {
							t.Fatal(err)
						}
					}

					// Explicit hours and a fixed date exercise midnight at any wall-clock
					// time; 24 rows also cross the backup import's batch boundary.
					for pass := 0; pass < 2; pass++ {
						rows := make([]model.StatsHourly, tc.hours)
						for hour := range rows {
							rows[hour] = model.StatsHourly{Hour: hour, Date: hourlyStatsTestDate,
								StatsMetrics: model.StatsMetrics{InputCost: float64(100 + pass + hour)}}
						}
						if err := write(ctx, slices.Clone(rows)); err != nil {
							t.Fatalf("write pass %d: %v", pass, err)
						}
						want := slices.Clone(rows)
						if tc.hours == 1 && tc.seedExisting {
							want = append(want, existing)
						}
						var saved []model.StatsHourly
						if err := conn.Order("hour").Find(&saved).Error; err != nil {
							t.Fatal(err)
						}
						if !slices.Equal(saved, want) {
							t.Fatalf("write pass %d changed hourly keys or costs: got %+v, want %+v", pass, saved, want)
						}
						if err := statsRefreshCache(ctx); err != nil {
							t.Fatal(err)
						}
						statsHourlyCacheLock.RLock()
						cached := statsHourlyCache
						statsHourlyCacheLock.RUnlock()
						for _, row := range want {
							if cached[row.Hour] != row {
								t.Fatalf("reload pass %d lost hour %d: got %+v, want %+v", pass, row.Hour, cached[row.Hour], row)
							}
						}
					}
				})
			}
		})
	}
}
