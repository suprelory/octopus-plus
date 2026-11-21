package op

import (
	"context"
	"fmt"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	dbDumpVersion = 1

	// Keep import batches small enough for SQLite builds with low SQL variable limits.
	// Some exported tables (for example relay_logs) have many columns, so a conservative
	// row count avoids "too many SQL variables" during bulk insert/upsert.
	dbImportBatchSize    = 20
	dbExportLogBatchSize = 1000
)

func DBImportIncremental(ctx context.Context, dump *model.DBDump) (*model.DBImportResult, error) {
	if dump == nil {
		return nil, fmt.Errorf("empty dump")
	}

	if dump.Version != dbDumpVersion {
		return nil, fmt.Errorf("unsupported dump version: %d", dump.Version)
	}
	conn := db.GetDB().WithContext(ctx)
	prepare := func(conn *gorm.DB) error {
		// MySQL DDL implicitly commits transactions.
		if dump.IncludeStats && len(dump.StatsModel) > 0 && !conn.Migrator().HasTable(&model.StatsModel{}) {
			if err := conn.Migrator().CreateTable(&model.StatsModel{}); err != nil {
				return fmt.Errorf("prepare legacy stats_model import: %w", err)
			}
		}
		return nil
	}
	return RestoreBackupTransaction(ctx, conn, dump.IncludeStats, prepare, func(tx *gorm.DB) (*model.DBImportResult, error) {
		res := &model.DBImportResult{RowsAffected: map[string]int64{}}
		state := newDBImportState(tx, dump, res)
		stages := []func() error{
			state.importProxies, state.importChannels, state.importChannelKeys,
			state.importSites, state.importAccounts, state.importTokens, state.importUserGroups,
			state.importModels, state.importBindings, state.importGroups, state.importGroupItems,
			state.importGroupPresets, state.importModelPrices, state.importAPIKeys,
			state.importSettings, state.importStats, state.importLogs,
		}
		for _, stage := range stages {
			if err := stage(); err != nil {
				return nil, err
			}
		}
		return res, db.SeparateLegacySiteCheckins(tx)
	})
}

// RestoreBackupTransaction shares the existing cache publication and writer
// locks with the v2 importer. Schema preparation runs under the same locks before
// the transaction, while the restore callback performs DML only in the transaction.
// An injected database publishes caches only when it is the running application DB.
func RestoreBackupTransaction(ctx context.Context, conn *gorm.DB, includeStats bool, prepare func(*gorm.DB) error, restore func(*gorm.DB) (*model.DBImportResult, error)) (*model.DBImportResult, error) {
	// Freeze lifecycle writers before opening the transaction. Publishing the
	// committed snapshots under these same locks prevents stale flushes and late
	// settlements from replacing restored data or recreating removed keys.
	channelWriteLock.Lock()
	defer channelWriteLock.Unlock()
	apiKeyWriteLock.Lock()
	defer apiKeyWriteLock.Unlock()
	statsLifecycleLock.Lock()
	defer statsLifecycleLock.Unlock()
	statsPersistenceLock.Lock()
	defer statsPersistenceLock.Unlock()
	if err := prepare(conn.WithContext(ctx)); err != nil {
		return nil, err
	}

	var res *model.DBImportResult
	var restoredStats *statsCacheSnapshot
	var channels []model.Channel
	var apiKeys []model.APIKey
	pool, err := conn.DB()
	if err != nil {
		return nil, err
	}
	publish := false
	if running := db.GetDB(); running != nil {
		live, err := running.DB()
		publish = err == nil && live == pool
	}

	err = conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		res, err = restore(tx)
		if err != nil {
			return err
		}
		if !publish || res.AlreadyImported {
			return nil
		}
		if err := tx.Preload("Keys").Find(&channels).Error; err != nil {
			return fmt.Errorf("load imported channels: %w", err)
		}
		if err := tx.Find(&apiKeys).Error; err != nil {
			return fmt.Errorf("load imported API keys: %w", err)
		}
		if includeStats {
			var err error
			restoredStats, err = loadStatsCache(tx)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !publish || res.AlreadyImported {
		return res, nil
	}
	publishChannelsLocked(channels)
	publishAPIKeysLocked(apiKeys)
	if restoredStats != nil {
		restoredStats.publish()
	}
	// The import transaction has already committed; cache refresh failures are non-fatal
	// and can be recovered by a later InitCache/refresh cycle.
	if err := proxyConfigurationRefreshCache(ctx); err != nil {
		log.Warnw("refresh proxy configuration cache after import failed",
			"operation", "db_import_incremental",
			"error", err,
		)
	}
	return res, nil
}

// dbImportState belongs to one import transaction. Stages run in dependency order
// so all foreign-key remapping and writes share the same rollback boundary.
type dbImportState struct {
	tx               *gorm.DB
	dump             *model.DBDump
	result           *model.DBImportResult
	channelIDs       map[int]int
	channelKeyIDs    map[int]int
	resolvedChannels map[int]resolvedImportChannel
	proxyIDs         map[int]int
	siteIDs          map[int]int
	accountIDs       map[int]int
	userGroupIDs     map[int]int
	groupIDs         map[int]int
	newGroupIDs      map[int]bool
	apiKeyIDs        map[int]int
	strict           bool
}

func newDBImportState(tx *gorm.DB, dump *model.DBDump, result *model.DBImportResult) *dbImportState {
	return &dbImportState{
		tx:               tx,
		dump:             dump,
		result:           result,
		channelIDs:       make(map[int]int),
		channelKeyIDs:    make(map[int]int),
		resolvedChannels: make(map[int]resolvedImportChannel),
		proxyIDs:         make(map[int]int),
		siteIDs:          make(map[int]int),
		accountIDs:       make(map[int]int),
		userGroupIDs:     make(map[int]int),
		groupIDs:         make(map[int]int),
		newGroupIDs:      make(map[int]bool),
		apiKeyIDs:        make(map[int]int),
	}
}

func createDoNothing[T any](tx *gorm.DB, rows []T) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, dbImportBatchSize)
	return result.RowsAffected, result.Error
}

func createUpsertAll[T any](tx *gorm.DB, rows []T, columns []clause.Column) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	result := tx.Clauses(clause.OnConflict{
		Columns:   columns,
		UpdateAll: true,
	}).CreateInBatches(&rows, dbImportBatchSize)
	return result.RowsAffected, result.Error
}

func createUpsertSettings(tx *gorm.DB, rows []model.Setting) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	result := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).CreateInBatches(&rows, dbImportBatchSize)
	return result.RowsAffected, result.Error
}
