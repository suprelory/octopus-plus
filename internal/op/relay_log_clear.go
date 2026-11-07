package op

import (
	"context"
	"fmt"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/utils/snowflake"
	"gorm.io/gorm"
)

func RelayLogSaveDBTask(ctx context.Context) error {
	log.Debugf("relay log save db task started")
	startTime := time.Now()
	defer func() {
		log.Debugf("relay log save db task finished, save time: %s", time.Since(startTime))
	}()
	enabled, err := SettingGetBool(model.SettingKeyRelayLogKeepEnabled)
	if err != nil {
		return err
	}

	if enabled {
		if err := RelayLogFlushPending(ctx); err != nil {
			return err
		}
		return relayLogCleanup(ctx)
	}

	trimRelayLogRecent()
	return nil
}

func trimRelayLogRecent() {
	relayLogBuffer.recentLock.Lock()
	if len(relayLogBuffer.recent) > relayLogRecentMaxSize {
		keepSize := relayLogRecentMaxSize / 2
		relayLogBuffer.recent = append([]model.RelayLog(nil), relayLogBuffer.recent[len(relayLogBuffer.recent)-keepSize:]...)
	}
	relayLogBuffer.recentLock.Unlock()
}

func relayLogCleanup(ctx context.Context) error {
	if err := relayLogContentCleanup(ctx); err != nil {
		return err
	}
	keepPeriod, err := SettingGetInt(model.SettingKeyRelayLogKeepPeriod)
	if err != nil {
		return err
	}

	if keepPeriod <= 0 {
		return nil
	}

	cutoffTime := time.Now().Add(-time.Duration(keepPeriod) * 24 * time.Hour).Unix()
	start := time.Now()
	deletedRows := int64(0)
	batchCount := 0
	dbConn := db.GetDB().WithContext(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var ids []int64
		if err := dbConn.Model(&model.RelayLog{}).
			Where("time < ?", cutoffTime).
			Order("time ASC").
			Order("id ASC").
			Limit(relayLogCleanupBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		count, err := deleteRelayLogRows(dbConn, ids)
		if err != nil {
			return err
		}
		deletedRows += count
		batchCount++
		if len(ids) < relayLogCleanupBatchSize {
			break
		}
		if relayLogCleanupBatchWait > 0 {
			timer := time.NewTimer(relayLogCleanupBatchWait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	if deletedRows > 0 {
		log.Debugw("relay_log.cleanup", "deleted_rows", deletedRows, "batch_count", batchCount, "duration", time.Since(start).String())
	}
	return nil
}

func RelayLogClear(ctx context.Context) error {
	// Claim the purge under the flush lock so a flush already in flight finishes
	// first and concurrent clears are rejected. Keep it only while establishing
	// the cutoff and pruning the in-memory buffers; the batched DB purge must not
	// stall the background flusher.
	relayLogBuffer.flushLock.Lock()
	if relayLogBuffer.clearing {
		relayLogBuffer.flushLock.Unlock()
		return fmt.Errorf("relay log clear already in progress")
	}
	relayLogBuffer.clearing = true

	// Exclude RelayLogAdd while minting the boundary. Include the largest
	// persisted ID because imports and wall-clock rollback can leave database IDs
	// ahead of this process's generator. Advancing the generator past that value
	// keeps every later local log outside the purge.
	relayLogBuffer.snapshotLock.Lock()
	clearThroughID, err := relayLogClearThroughID(ctx)
	if err != nil {
		relayLogBuffer.snapshotLock.Unlock()
		relayLogBuffer.clearing = false
		relayLogBuffer.flushLock.Unlock()
		return err
	}
	discardRelayLogBuffersThrough(clearThroughID)
	relayLogBuffer.snapshotLock.Unlock()
	relayLogBuffer.flushLock.Unlock()

	defer func() {
		relayLogBuffer.flushLock.Lock()
		relayLogBuffer.clearing = false
		relayLogBuffer.flushLock.Unlock()
	}()

	// 缓存的 total 在删除后立刻失效，否则清空日志后列表还会显示旧总数。
	defer RelayLogTotalCacheInvalidate()

	start := time.Now()
	deletedRows := int64(0)
	batchCount := 0
	dbConn := db.GetDB().WithContext(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var ids []int64
		if err := dbConn.Model(&model.RelayLog{}).
			Where("id <= ?", clearThroughID).
			Order("time ASC").
			Order("id ASC").
			Limit(relayLogCleanupBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		count, err := deleteRelayLogRows(dbConn, ids)
		if err != nil {
			return err
		}
		deletedRows += count
		batchCount++
		if len(ids) < relayLogCleanupBatchSize {
			break
		}
		if relayLogCleanupBatchWait > 0 {
			timer := time.NewTimer(relayLogCleanupBatchWait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	if deletedRows > 0 {
		log.Debugw("relay_log.clear", "deleted_rows", deletedRows, "batch_count", batchCount, "duration", time.Since(start).String())
	}
	return nil
}

func relayLogClearThroughID(ctx context.Context) (int64, error) {
	var maxPersistedID int64
	if err := db.GetDB().WithContext(ctx).
		Model(&model.RelayLog{}).
		Select("COALESCE(MAX(id), 0)").
		Scan(&maxPersistedID).Error; err != nil {
		return 0, fmt.Errorf("failed to snapshot relay log boundary: %w", err)
	}

	clearThroughID, err := snowflake.GenerateIDAfter(maxPersistedID)
	if err != nil {
		return 0, fmt.Errorf("failed to reserve relay log boundary: %w", err)
	}
	return clearThroughID, nil
}

// discardRelayLogBuffersThrough removes the in-memory portion of a clear
// snapshot. The caller must hold relayLogBuffer.snapshotLock for writing.
func discardRelayLogBuffersThrough(clearThroughID int64) {
	relayLogBuffer.pendingLock.Lock()
	keptPending := make([]model.RelayLog, 0, relayLogBatchSize)
	keptPendingBytes := int64(0)
	for _, entry := range relayLogBuffer.pending {
		if entry.ID <= clearThroughID {
			continue
		}
		keptPending = append(keptPending, entry)
		keptPendingBytes += relayLogApproxBytes(entry)
	}
	relayLogBuffer.pending = keptPending
	relayLogBuffer.pendingBytes = keptPendingBytes
	relayLogBuffer.pendingContentBytes = 0
	for _, entry := range keptPending {
		relayLogBuffer.pendingContentBytes += relayTraceBytes(entry.Trace)
	}
	relayLogBuffer.pendingLock.Unlock()

	relayLogBuffer.recentLock.Lock()
	keptRecent := make([]model.RelayLog, 0, relayLogRecentMaxSize)
	for _, entry := range relayLogBuffer.recent {
		if entry.ID > clearThroughID {
			keptRecent = append(keptRecent, entry)
		}
	}
	relayLogBuffer.recent = keptRecent
	relayLogBuffer.recentLock.Unlock()
}

func relayLogContentCleanup(ctx context.Context) error {
	days, err := SettingGetInt(model.SettingKeyRelayLogContentKeepPeriod)
	if err != nil {
		return err
	}
	if days <= 0 {
		return nil
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	updates := map[string]any{"request_body": nil, "response_body": nil, "bodies_expired": true}
	for _, table := range []any{&model.RelayRequestContent{}, &model.RelayAttemptContent{}} {
		query := db.GetDB().WithContext(ctx).Model(table).Where("created_at < ? AND bodies_expired = ?", cutoff, false)
		if err := expireRelayContentBatches(query, "log_id", updates); err != nil {
			return err
		}
	}
	query := db.GetDB().WithContext(ctx).Model(&model.RelayLog{}).Where("time < ? AND (request_content <> '' OR response_content <> '')", cutoff)
	return expireRelayContentBatches(query, "id", map[string]any{"request_content": "", "response_content": ""})
}

func expireRelayContentBatches(query *gorm.DB, idColumn string, updates map[string]any) error {
	for {
		var ids []int64
		if err := query.Session(&gorm.Session{}).Limit(200).Pluck(idColumn, &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := query.Session(&gorm.Session{}).Where(idColumn+" IN ?", ids).Updates(updates).Error; err != nil {
			return err
		}
	}
}
