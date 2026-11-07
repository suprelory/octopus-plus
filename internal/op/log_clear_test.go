package op

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/snowflake"
)

// RelayLogClear must not hold relayLogBuffer.flushLock across its batched deletes: the
// background flusher takes the same lock, so a long purge would stall it until
// the pending queue overflows and starts dropping logs.
func TestRelayLogClearDoesNotBlockFlushLock(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatalf("settingRefreshCache failed: %v", err)
	}
	resetRelayLogStateForTest()

	// Enough batches that the inter-batch sleeps dominate: holding the lock for
	// the whole purge is then clearly distinguishable from not holding it.
	const batches = 20
	rows := make([]model.RelayLog, 0, relayLogCleanupBatchSize*batches)
	for i := 0; i < relayLogCleanupBatchSize*batches; i++ {
		rows = append(rows, model.RelayLog{ID: int64(i + 1), Time: time.Now().Unix(), RequestModelName: "gpt-4o", Success: true})
	}
	if err := dbpkg.GetDB().WithContext(ctx).CreateInBatches(&rows, 1000).Error; err != nil {
		t.Fatalf("seed relay logs failed: %v", err)
	}

	clearDone := make(chan error, 1)
	clearStart := time.Now()
	go func() { clearDone <- RelayLogClear(ctx) }()

	// Give the purge time to get past its first batch, then time how long the
	// flusher would wait for the lock.
	time.Sleep(relayLogCleanupBatchWait * 2)
	lockStart := time.Now()
	relayLogBuffer.flushLock.Lock()
	clearing := relayLogBuffer.clearing
	relayLogBuffer.flushLock.Unlock()
	lockWait := time.Since(lockStart)
	if !clearing {
		t.Fatal("purge finished before lock contention could be observed")
	}

	if err := <-clearDone; err != nil {
		t.Fatalf("RelayLogClear failed: %v", err)
	}
	purgeDuration := time.Since(clearStart)

	// The purge has to be slow enough for this assertion to mean anything.
	minPurge := relayLogCleanupBatchWait * batches / 2
	if purgeDuration < minPurge {
		t.Fatalf("purge finished too quickly (%s) to test lock contention", purgeDuration)
	}
	if lockWait > purgeDuration/4 {
		t.Fatalf("flusher waited %s of a %s purge; the lock is held across the batched deletes",
			lockWait, purgeDuration)
	}

	var remaining int64
	if err := dbpkg.GetDB().WithContext(ctx).Model(&model.RelayLog{}).Count(&remaining).Error; err != nil {
		t.Fatalf("count relay logs failed: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected every seeded row to be purged, %d remain", remaining)
	}
}

// The purge still resets the in-memory buffers.
func TestRelayLogClearResetsBuffers(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatalf("settingRefreshCache failed: %v", err)
	}
	resetRelayLogStateForTest()

	for i := 0; i < 5; i++ {
		if err := RelayLogAdd(model.RelayLog{Time: time.Now().Unix(), RequestModelName: "gpt-4o", Success: true}); err != nil {
			t.Fatalf("RelayLogAdd failed: %v", err)
		}
	}
	if RelayLogPendingLen() == 0 {
		t.Fatal("expected queued logs before the purge")
	}

	if err := RelayLogClear(ctx); err != nil {
		t.Fatalf("RelayLogClear failed: %v", err)
	}

	if got := RelayLogPendingLen(); got != 0 {
		t.Fatalf("expected the pending buffer to be reset, got %d", got)
	}
	relayLogBuffer.recentLock.Lock()
	recent := len(relayLogBuffer.recent)
	relayLogBuffer.recentLock.Unlock()
	if recent != 0 {
		t.Fatalf("expected the recent buffer to be reset, got %d", recent)
	}
	if relayLogBuffer.clearing {
		t.Fatal("expected the clearing flag to be released")
	}
}

// Imported logs can carry IDs ahead of the local clock. A clear snapshot must
// include those persisted IDs and advance the local generator past them.
func TestRelayLogClearRemovesPersistedIDsAheadOfLocalGenerator(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatalf("settingRefreshCache failed: %v", err)
	}
	resetRelayLogStateForTest()

	futureID := snowflake.GenerateID() + int64((24*time.Hour)/time.Millisecond)
	row := model.RelayLog{
		ID:               futureID,
		Time:             time.Now().Unix(),
		RequestModelName: "imported-future-id",
		Success:          true,
	}
	if err := dbpkg.GetDB().WithContext(ctx).Create(&row).Error; err != nil {
		t.Fatalf("seed future relay log failed: %v", err)
	}

	if err := RelayLogClear(ctx); err != nil {
		t.Fatalf("RelayLogClear failed: %v", err)
	}

	var remaining int64
	if err := dbpkg.GetDB().WithContext(ctx).Model(&model.RelayLog{}).Count(&remaining).Error; err != nil {
		t.Fatalf("count relay logs failed: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected the imported future-ID row to be purged, %d rows remain", remaining)
	}
	if nextID := snowflake.GenerateID(); nextID <= futureID {
		t.Fatalf("expected the local generator to advance past %d, got %d", futureID, nextID)
	}
}

// Overlapping purges are rejected rather than running concurrently.
func TestRelayLogClearRejectsConcurrentCall(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatalf("settingRefreshCache failed: %v", err)
	}
	resetRelayLogStateForTest()

	relayLogBuffer.flushLock.Lock()
	relayLogBuffer.clearing = true
	relayLogBuffer.flushLock.Unlock()
	t.Cleanup(func() {
		relayLogBuffer.flushLock.Lock()
		relayLogBuffer.clearing = false
		relayLogBuffer.flushLock.Unlock()
	})

	err := RelayLogClear(ctx)
	if err == nil {
		t.Fatal("expected an overlapping clear to be rejected")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A cancelled purge must still release the flag, or clearing breaks forever.
func TestRelayLogClearReleasesFlagOnCancel(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatalf("settingRefreshCache failed: %v", err)
	}
	resetRelayLogStateForTest()

	rows := make([]model.RelayLog, 0, relayLogCleanupBatchSize*2)
	for i := 0; i < relayLogCleanupBatchSize*2; i++ {
		rows = append(rows, model.RelayLog{ID: int64(i + 1), Time: time.Now().Unix(), RequestModelName: "gpt-4o", Success: true})
	}
	if err := dbpkg.GetDB().WithContext(ctx).CreateInBatches(&rows, 500).Error; err != nil {
		t.Fatalf("seed relay logs failed: %v", err)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := RelayLogClear(cancelCtx); err == nil {
		t.Fatal("expected the cancelled purge to report an error")
	}

	relayLogBuffer.flushLock.Lock()
	clearing := relayLogBuffer.clearing
	relayLogBuffer.flushLock.Unlock()
	if clearing {
		t.Fatal("expected the clearing flag to be released after cancellation")
	}
}

// Flushes queued during a purge are not lost: the buffer keeps draining.
func TestRelayLogFlushStillDrainsDuringClear(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatalf("settingRefreshCache failed: %v", err)
	}
	resetRelayLogStateForTest()

	rows := make([]model.RelayLog, 0, relayLogCleanupBatchSize*3)
	for i := 0; i < relayLogCleanupBatchSize*3; i++ {
		rows = append(rows, model.RelayLog{ID: int64(i + 1), Time: time.Now().Unix(), RequestModelName: "gpt-4o", Success: true})
	}
	if err := dbpkg.GetDB().WithContext(ctx).CreateInBatches(&rows, 500).Error; err != nil {
		t.Fatalf("seed relay logs failed: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var clearErr error
	go func() {
		defer wg.Done()
		clearErr = RelayLogClear(ctx)
	}()

	// Observing the flag while holding relayLogBuffer.flushLock guarantees that clear
	// has released the lock after establishing its cutoff.
	deadline := time.Now().Add(2 * time.Second)
	for {
		relayLogBuffer.flushLock.Lock()
		clearing := relayLogBuffer.clearing
		relayLogBuffer.flushLock.Unlock()
		if clearing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for RelayLogClear to establish its snapshot")
		}
		time.Sleep(time.Millisecond)
	}

	// Queue new logs after the cutoff and drain them. They must remain flushable
	// during the purge and must not be consumed by one of its later batches.
	for i := 0; i < 10; i++ {
		if err := RelayLogAdd(model.RelayLog{Time: time.Now().Unix(), RequestModelName: "gpt-4o-mini", Success: true}); err != nil {
			t.Fatalf("RelayLogAdd failed: %v", err)
		}
	}
	flushed := make(chan error, 1)
	go func() { flushed <- relayLogFlushPendingBatch(ctx, relayLogBatchSize) }()
	select {
	case err := <-flushed:
		if err != nil {
			t.Fatalf("flush during purge failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("flush blocked for the whole purge")
	}

	wg.Wait()
	if clearErr != nil {
		t.Fatalf("RelayLogClear failed: %v", clearErr)
	}

	var surviving int64
	if err := dbpkg.GetDB().WithContext(ctx).Model(&model.RelayLog{}).
		Where("request_model_name = ?", "gpt-4o-mini").
		Count(&surviving).Error; err != nil {
		t.Fatalf("count logs added after clear snapshot failed: %v", err)
	}
	if surviving != 10 {
		t.Fatalf("expected all 10 logs added after the clear snapshot to survive, got %d", surviving)
	}
}
