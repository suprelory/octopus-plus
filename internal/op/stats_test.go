package op

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func resetStatsTestState(t *testing.T) {
	t.Helper()
	statsChannelCache.Clear()
	statsAPIKeyCache.Clear()
	statsChannelCacheNeedUpdateLock.Lock()
	statsChannelCacheNeedUpdate = make(map[int]struct{})
	statsChannelCacheNeedUpdateLock.Unlock()
	statsAPIKeyCacheNeedUpdateLock.Lock()
	statsAPIKeyCacheNeedUpdate = make(map[int]struct{})
	statsAPIKeyCacheNeedUpdateLock.Unlock()
	t.Cleanup(func() {
		statsChannelCache.Clear()
		statsAPIKeyCache.Clear()
	})
}

func TestStatsChannelUpdateIsAtomic(t *testing.T) {
	resetStatsTestState(t)

	const (
		workers = 32
		updates = 100
	)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < updates; j++ {
				if err := StatsChannelUpdate(7, model.StatsMetrics{RequestSuccess: 1}); err != nil {
					t.Errorf("StatsChannelUpdate failed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got := StatsChannelGet(7)
	if got.RequestSuccess != workers*updates {
		t.Fatalf("request success count = %d, want %d", got.RequestSuccess, workers*updates)
	}
}

func TestChannelKeyUpdateWithDeltaIsAtomic(t *testing.T) {
	resetStatsTestState(t)
	channelCache.Clear()
	channelKeyCache.Clear()
	channel := model.Channel{
		ID:   1,
		Keys: []model.ChannelKey{{ID: 11, ChannelID: 1, Enabled: true, ChannelKey: "key"}},
	}
	setChannelRuntimeCache(channel.ID, channel)
	channelKeyCache.Set(11, channel.Keys[0])
	t.Cleanup(func() {
		channelCache.Clear()
		channelKeyCache.Clear()
	})

	const (
		workers = 32
		updates = 100
		delta   = 0.25
	)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < updates; j++ {
				if err := ChannelKeyUpdateWithDelta(channel.Keys[0], delta); err != nil {
					t.Errorf("ChannelKeyUpdateWithDelta failed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got, ok := channelKeyCache.Get(11)
	if !ok {
		t.Fatal("channel key missing from cache")
	}
	want := float64(workers*updates) * delta
	if got.TotalCost != want {
		t.Fatalf("total cost = %f, want %f", got.TotalCost, want)
	}
	gotChannel, ok := channelCache.Get(1)
	if !ok || len(gotChannel.Keys) != 1 || gotChannel.Keys[0].TotalCost != want {
		t.Fatalf("channel cache key cost is not synchronized: %+v", gotChannel.Keys)
	}
}

func TestChannelKeyUpdateWithDeltaPreservesNewerConfiguration(t *testing.T) {
	channelCache.Clear()
	channelKeyCache.Clear()
	t.Cleanup(func() {
		channelCache.Clear()
		channelKeyCache.Clear()
	})

	current := model.ChannelKey{
		ID: 11, ChannelID: 1, Enabled: false, ChannelKey: "new-key", Remark: "new", TotalCost: 10,
		StatusCode: 201, LastUseTimeStamp: 200,
	}
	staleRequestSnapshot := model.ChannelKey{
		ID: 11, ChannelID: 1, Enabled: true, ChannelKey: "old-key", Remark: "old", TotalCost: 3,
		StatusCode: 500, LastUseTimeStamp: 100,
	}
	setChannelRuntimeCache(1, model.Channel{ID: 1, Keys: []model.ChannelKey{current}})
	channelKeyCache.Set(11, current)

	if err := ChannelKeyUpdateWithDelta(staleRequestSnapshot, 2); err != nil {
		t.Fatalf("ChannelKeyUpdateWithDelta failed: %v", err)
	}
	got, _ := channelKeyCache.Get(11)
	if got.Enabled || got.ChannelKey != "new-key" || got.Remark != "new" {
		t.Fatalf("stale relay snapshot overwrote key configuration: %+v", got)
	}
	if got.TotalCost != 12 {
		t.Fatalf("total cost = %f, want 12", got.TotalCost)
	}
	if got.StatusCode != 201 || got.LastUseTimeStamp != 200 {
		t.Fatalf("older runtime status overwrote newer status: %+v", got)
	}
}

func TestStatsSiteModelHourlySaveDBRestoresRowsOnFailure(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	siteModelHourlyCacheLock.Lock()
	siteModelHourlyCache = map[siteModelHourlyKey]*model.StatsSiteModelHourly{
		{Hour: 1, SiteAccountID: 2, GroupKey: "default", ModelName: "model"}: {
			Hour: 1, SiteAccountID: 2, GroupKey: "default", ModelName: "model", Date: "20260101",
			StatsMetrics: model.StatsMetrics{RequestSuccess: 1},
		},
	}
	siteModelHourlyCacheLock.Unlock()
	t.Cleanup(func() {
		siteModelHourlyCacheLock.Lock()
		siteModelHourlyCache = make(map[siteModelHourlyKey]*model.StatsSiteModelHourly)
		siteModelHourlyCacheLock.Unlock()
	})

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := StatsSiteModelHourlySaveDB(canceled); err == nil {
		t.Fatal("expected canceled database write to fail")
	}

	siteModelHourlyCacheLock.Lock()
	defer siteModelHourlyCacheLock.Unlock()
	entry, ok := siteModelHourlyCache[siteModelHourlyKey{Hour: 1, SiteAccountID: 2, GroupKey: "default", ModelName: "model"}]
	if !ok || entry.RequestSuccess != 1 {
		t.Fatalf("failed snapshot was not restored: %+v", siteModelHourlyCache)
	}
}

func TestStatsSiteModelHourlySaveDBUpsertsSQLite(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	siteModelHourlyCacheLock.Lock()
	siteModelHourlyCache = map[siteModelHourlyKey]*model.StatsSiteModelHourly{
		{Hour: 1, SiteAccountID: 2, GroupKey: "default", ModelName: "model"}: {
			Hour: 1, SiteAccountID: 2, GroupKey: "default", ModelName: "model", Date: "20260101",
			StatsMetrics: model.StatsMetrics{InputToken: 3, RequestSuccess: 1},
		},
	}
	siteModelHourlyCacheLock.Unlock()

	if err := StatsSiteModelHourlySaveDB(ctx); err != nil {
		t.Fatalf("first hourly save failed: %v", err)
	}
	siteModelHourlyCacheLock.Lock()
	siteModelHourlyCache = map[siteModelHourlyKey]*model.StatsSiteModelHourly{
		{Hour: 1, SiteAccountID: 2, GroupKey: "default", ModelName: "model"}: {
			Hour: 1, SiteAccountID: 2, GroupKey: "default", ModelName: "model", Date: "20260101",
			StatsMetrics: model.StatsMetrics{InputToken: 4, RequestSuccess: 2},
		},
	}
	siteModelHourlyCacheLock.Unlock()
	if err := StatsSiteModelHourlySaveDB(ctx); err != nil {
		t.Fatalf("second hourly save failed: %v", err)
	}

	var row model.StatsSiteModelHourly
	if err := dbpkg.GetDB().WithContext(ctx).First(&row, "hour = ? AND site_account_id = ? AND group_key = ? AND model_name = ?", 1, 2, "default", "model").Error; err != nil {
		t.Fatalf("load hourly row failed: %v", err)
	}
	if row.InputToken != 7 || row.RequestSuccess != 3 {
		t.Fatalf("hourly upsert did not accumulate values: %+v", row)
	}
}

func TestStatsSiteModelHourlyUpsertSQLByDialect(t *testing.T) {
	tests := []struct {
		name       string
		dialector  gorm.Dialector
		contains   []string
		notContain []string
	}{
		{
			name:      "sqlite",
			dialector: sqlite.Open(":memory:"),
			contains:  []string{"EXCLUDED.input_token", "MAX(stats_site_model_hourlies.last_request_at, EXCLUDED.last_request_at)"},
		},
		{
			name: "mysql",
			dialector: mysql.New(mysql.Config{
				DSN:                       "user:pass@tcp(localhost:3306)/octopus",
				SkipInitializeWithVersion: true,
			}),
			contains:   []string{"VALUES(input_token)", "GREATEST(stats_site_model_hourlies.last_request_at, VALUES(last_request_at))"},
			notContain: []string{"EXCLUDED"},
		},
		{
			name: "postgres",
			dialector: postgres.New(postgres.Config{
				DSN:                  "host=localhost user=postgres dbname=octopus sslmode=disable",
				PreferSimpleProtocol: true,
			}),
			contains:   []string{"EXCLUDED.input_token", "GREATEST(stats_site_model_hourlies.last_request_at, EXCLUDED.last_request_at)"},
			notContain: []string{"MAX(stats_site_model_hourlies.last_request_at"},
		},
	}

	row := model.StatsSiteModelHourly{
		Hour: 1, SiteAccountID: 2, GroupKey: "default", ModelName: "model", Date: "20260101",
		StatsMetrics: model.StatsMetrics{InputToken: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dbConn, err := gorm.Open(test.dialector, &gorm.Config{
				DryRun:                 true,
				DisableAutomaticPing:   true,
				SkipDefaultTransaction: true,
			})
			if err != nil {
				t.Fatalf("open dry-run database: %v", err)
			}
			statement := dbConn.Clauses(statsSiteModelHourlyUpsertClause(dbConn)).Create(&[]model.StatsSiteModelHourly{row}).Statement
			if statement.Error != nil {
				t.Fatalf("build dry-run upsert: %v", statement.Error)
			}
			sql := statement.SQL.String()
			for _, fragment := range test.contains {
				if !strings.Contains(sql, fragment) {
					t.Fatalf("generated SQL does not contain %q: %s", fragment, sql)
				}
			}
			for _, fragment := range test.notContain {
				if strings.Contains(sql, fragment) {
					t.Fatalf("generated SQL unexpectedly contains %q: %s", fragment, sql)
				}
			}
		})
	}
}
