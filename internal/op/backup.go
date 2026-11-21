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

// Keep batches below SQLite's variable limit for wider tables.
const dbImportBatchSize = 20

// RestoreBackupTransaction prepares the schema, commits the restore and publishes
// cache updates under the same writer locks.
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
			"operation", "backup_restore",
			"error", err,
		)
	}
	return res, nil
}

// dbImportState belongs to one import transaction. Stages run in dependency order
// so all foreign-key remapping and writes share the same rollback boundary.
type dbImportState struct {
	tx               *gorm.DB
	dump             *model.LegacyDBDump
	result           *model.DBImportResult
	channelIDs       map[int]int
	channelKeyIDs    map[int]int
	resolvedChannels map[int]resolvedImportChannel
	proxyIDs         map[int]int
	groupIDs         map[int]int
	apiKeyIDs        map[int]int
}

func newDBImportState(tx *gorm.DB, dump *model.LegacyDBDump, result *model.DBImportResult) *dbImportState {
	return &dbImportState{
		tx:               tx,
		dump:             dump,
		result:           result,
		channelIDs:       make(map[int]int),
		channelKeyIDs:    make(map[int]int),
		resolvedChannels: make(map[int]resolvedImportChannel),
		proxyIDs:         make(map[int]int),
		groupIDs:         make(map[int]int),
		apiKeyIDs:        make(map[int]int),
	}
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
