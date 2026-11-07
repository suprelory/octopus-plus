package op

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/utils/snowflake"
	"gorm.io/gorm"
)

const (
	relayLogBatchSize        = 200
	relayLogFlushInterval    = time.Second
	relayLogQueueSize        = 5000
	relayLogQueueBytes       = 64 << 20
	relayLogRecentMaxSize    = 100 // 最近日志缓存，用于实时查询/不落库模式
	relayLogRecentMaxBytes   = 8 << 20
	relayLogCleanupBatchSize = 1000
	relayLogCleanupBatchWait = 30 * time.Millisecond
	relayLogWriterMaxBatches = 25
)

// relayLogBufferState owns the buffers shared by writes, queries and clearing.
// When locks are combined, acquire flushLock before snapshotLock, then the
// pending/recent lock. The state must not be copied after first use.
type relayLogBufferState struct {
	pendingLock         sync.Mutex
	pending             []model.RelayLog
	pendingBytes        int64
	pendingContentBytes int64

	recentLock sync.Mutex
	recent     []model.RelayLog

	flushLock   sync.Mutex
	flushSignal chan struct{}

	// RelayLogAdd holds a read lock from ID allocation through its buffer writes.
	// RelayLogClear takes the write lock to mint a cutoff and prune the buffers,
	// making every log unambiguously older or newer than the active purge.
	snapshotLock sync.RWMutex

	// Protected by flushLock; rejects overlapping clears. A purge releases
	// flushLock before batched DB deletes so the writer can keep draining logs
	// admitted after the snapshot without overflowing the pending queue.
	clearing bool

	droppedTotal atomic.Uint64
	lastDropWarn atomic.Int64
}

var relayLogBuffer = relayLogBufferState{
	pending:     make([]model.RelayLog, 0, relayLogBatchSize),
	recent:      make([]model.RelayLog, 0, relayLogRecentMaxSize),
	flushSignal: make(chan struct{}, 1),
}

// RelayLogWriterRun flushes persisted relay logs from the in-memory queue in
// the background. It wakes either when the queue reaches relayLogBatchSize or
// on relayLogFlushInterval; request goroutines never write relay_logs directly.
func RelayLogWriterRun(ctx context.Context) {
	ticker := time.NewTicker(relayLogFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = RelayLogFlushPending(flushCtx)
			cancel()
			return
		case <-relayLogBuffer.flushSignal:
			if err := relayLogDrainPending(ctx, relayLogWriterMaxBatches); err != nil {
				log.Warnw("relay_log.flush_failed", "batch_size", relayLogBatchSize, "queue_length", RelayLogPendingLen(), "error", err.Error())
			}
		case <-ticker.C:
			if err := relayLogDrainPending(ctx, relayLogWriterMaxBatches); err != nil {
				log.Warnw("relay_log.flush_failed", "batch_size", relayLogBatchSize, "queue_length", RelayLogPendingLen(), "error", err.Error())
			}
		}
	}
}

func signalRelayLogFlush() {
	select {
	case relayLogBuffer.flushSignal <- struct{}{}:
	default:
	}
}

func appendRelayLogRecent(relayLog model.RelayLog) {
	if relayLog.Trace != nil {
		relayLog.Trace = relayTraceWithoutBodies(relayLog.Trace, "")
	}
	// Oversized bodies remain available from persisted logs. Keep their metadata
	// here without retaining a reference to the original body allocation.
	if relayLogApproxBytes(relayLog) > relayLogRecentMaxBytes {
		relayLog.RequestContent = ""
		relayLog.ResponseContent = ""
	}
	if relayLogApproxBytes(relayLog) > relayLogRecentMaxBytes {
		return
	}
	relayLogBuffer.recentLock.Lock()
	defer relayLogBuffer.recentLock.Unlock()
	relayLogBuffer.recent = append(relayLogBuffer.recent, relayLog)
	bytes := relayLogBatchApproxBytes(relayLogBuffer.recent)
	start := 0
	for len(relayLogBuffer.recent)-start > relayLogRecentMaxSize || bytes > relayLogRecentMaxBytes {
		bytes -= relayLogApproxBytes(relayLogBuffer.recent[start])
		start++
	}
	if start > 0 {
		// Copy so evicted strings are also released by the backing array.
		relayLogBuffer.recent = append([]model.RelayLog(nil), relayLogBuffer.recent[start:]...)
	}
}

func enqueueRelayLogPending(relayLog model.RelayLog) bool {
	estimatedBytes := relayLogApproxBytes(relayLog)
	relayLogBuffer.pendingLock.Lock()
	defer relayLogBuffer.pendingLock.Unlock()
	contentBytes := relayTraceBytes(relayLog.Trace)
	if relayLogBuffer.pendingContentBytes+contentBytes > relayLogContentQueueBytes {
		relayLog.Trace = relayTraceWithoutBodies(relayLog.Trace, "queue_full")
		contentBytes = 0
	}
	if len(relayLogBuffer.pending) >= relayLogQueueSize || relayLogBuffer.pendingBytes+estimatedBytes > relayLogQueueBytes {
		dropped := relayLogBuffer.droppedTotal.Add(1)
		warnRelayLogDropped(dropped)
		return false
	}
	relayLogBuffer.pending = append(relayLogBuffer.pending, relayLog)
	relayLogBuffer.pendingBytes += estimatedBytes
	relayLogBuffer.pendingContentBytes += contentBytes
	if len(relayLogBuffer.pending) >= relayLogBatchSize {
		signalRelayLogFlush()
	}
	return true
}

func relayLogApproxBytes(relayLog model.RelayLog) int64 {
	size := 256
	if relayLog.Trace != nil {
		if metadata, err := json.Marshal(relayLog.Trace); err == nil {
			size += len(metadata)
		}
	}
	size += len(relayLog.RequestModelName) + len(relayLog.RequestAPIKeyName) + len(relayLog.ClientIP) + len(relayLog.EndpointType) + len(relayLog.ChannelName) + len(relayLog.ActualModelName) + len(relayLog.ReasoningEffort)
	size += len(relayLog.RequestContent) + len(relayLog.ResponseContent) + len(relayLog.Error)
	for _, attempt := range relayLog.Attempts {
		size += 96 + len(attempt.ChannelName) + len(attempt.ModelName) + len(attempt.AdapterType) + len(attempt.Msg)
	}
	return int64(size)
}

func warnRelayLogDropped(dropped uint64) {
	now := time.Now().Unix()
	last := relayLogBuffer.lastDropWarn.Load()
	if now-last < 60 {
		return
	}
	if relayLogBuffer.lastDropWarn.CompareAndSwap(last, now) {
		log.Warnw("relay_log.queue_full", "dropped_total", dropped, "queue_size", relayLogQueueSize, "queue_bytes", relayLogQueueBytes)
	}
}

func RelayLogPendingLen() int {
	relayLogBuffer.pendingLock.Lock()
	defer relayLogBuffer.pendingLock.Unlock()
	return len(relayLogBuffer.pending)
}

func relayLogDrainPending(ctx context.Context, maxBatches int) error {
	if maxBatches <= 0 {
		maxBatches = 1
	}
	for i := 0; i < maxBatches; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if RelayLogPendingLen() == 0 {
			return nil
		}
		if err := relayLogFlushPendingBatch(ctx, relayLogBatchSize); err != nil {
			return err
		}
	}
	if RelayLogPendingLen() > 0 {
		signalRelayLogFlush()
	}
	return nil
}

func relayLogFlushPendingBatch(ctx context.Context, batchSize int) error {
	if batchSize <= 0 {
		batchSize = relayLogBatchSize
	}
	relayLogBuffer.flushLock.Lock()
	defer relayLogBuffer.flushLock.Unlock()

	relayLogBuffer.pendingLock.Lock()
	if len(relayLogBuffer.pending) == 0 {
		relayLogBuffer.pendingLock.Unlock()
		return nil
	}
	if batchSize > len(relayLogBuffer.pending) {
		batchSize = len(relayLogBuffer.pending)
	}
	batch := make([]model.RelayLog, batchSize)
	copy(batch, relayLogBuffer.pending[:batchSize])
	batchBytes := relayLogBatchApproxBytes(batch)
	relayLogBuffer.pendingLock.Unlock()

	start := time.Now()
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.CreateInBatches(&batch, relayLogBatchSize).Error; err != nil {
			return err
		}
		return persistRelayContents(tx, batch)
	})
	if err != nil {
		return err
	}
	duration := time.Since(start)
	log.Debugw("relay_log.flush", "batch_size", len(batch), "duration", duration.String(), "queue_length", RelayLogPendingLen())

	relayLogBuffer.pendingLock.Lock()
	if len(relayLogBuffer.pending) >= batchSize && relayLogBuffer.pending[0].ID == batch[0].ID && relayLogBuffer.pending[batchSize-1].ID == batch[batchSize-1].ID {
		relayLogBuffer.pending = relayLogBuffer.pending[batchSize:]
		relayLogBuffer.pendingBytes -= batchBytes
	} else {
		flushed := make(map[int64]struct{}, len(batch))
		for _, item := range batch {
			flushed[item.ID] = struct{}{}
		}
		kept := relayLogBuffer.pending[:0]
		keptBytes := int64(0)
		for _, item := range relayLogBuffer.pending {
			if _, ok := flushed[item.ID]; !ok {
				kept = append(kept, item)
				keptBytes += relayLogApproxBytes(item)
			}
		}
		relayLogBuffer.pending = kept
		relayLogBuffer.pendingBytes = keptBytes
	}
	if relayLogBuffer.pendingBytes < 0 {
		relayLogBuffer.pendingBytes = 0
	}
	relayLogBuffer.pendingContentBytes = 0
	for _, item := range relayLogBuffer.pending {
		relayLogBuffer.pendingContentBytes += relayTraceBytes(item.Trace)
	}
	if len(relayLogBuffer.pending) == 0 {
		relayLogBuffer.pending = make([]model.RelayLog, 0, relayLogBatchSize)
		relayLogBuffer.pendingBytes = 0
	}
	relayLogBuffer.pendingLock.Unlock()

	return nil
}

func relayLogBatchApproxBytes(batch []model.RelayLog) int64 {
	var total int64
	for _, item := range batch {
		total += relayLogApproxBytes(item)
	}
	return total
}

func RelayLogFlushPending(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if RelayLogPendingLen() == 0 {
			return nil
		}
		if err := relayLogFlushPendingBatch(ctx, relayLogBatchSize); err != nil {
			return err
		}
	}
}

func RelayLogAdd(relayLog model.RelayLog) error {
	enabled, err := SettingGetBool(model.SettingKeyRelayLogKeepEnabled)
	if err != nil {
		return err
	}

	relayLogBuffer.snapshotLock.RLock()
	relayLog.ID = snowflake.GenerateID()
	if relayLog.Trace != nil {
		relayLog.RequestContent, relayLog.ResponseContent = "", ""
		captureEnabled, _ := SettingGetBool(model.SettingKeyRelayLogContentEnabled)
		if !enabled || !captureEnabled {
			relayLog.Trace = relayTraceWithoutBodies(relayLog.Trace, "disabled")
		}
	}
	recent := relayLog
	if !enabled {
		// Disabling retention keeps live query metadata, but no new bodies.
		recent.RequestContent = ""
		recent.ResponseContent = ""
	}
	appendRelayLogRecent(recent)

	if enabled {
		enqueueRelayLogPending(relayLog)
	}
	notifySubscribers(relayLogLightCopy(relayLog))
	relayLogBuffer.snapshotLock.RUnlock()

	return nil
}
