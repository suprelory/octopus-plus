package relay

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

const (
	wsConversationStoreMaxEntries    = 10000
	wsConversationStoreMaxSize       = 100 * 1024 * 1024
	wsConversationStoreSweepInterval = 5 * time.Minute
)

type wsConversationStateEntry struct {
	state     *wsConversationState
	expiresAt time.Time
	size      int
}

// wsConversationStore is keyed partly on downstreamSessionID, which is minted
// fresh per client connection. Expired entries are therefore unreachable by
// lookup once their connection closes, so they need an active sweeper and
// capacity caps just like responsesReplayStore.
var wsConversationStore sync.Map // key: apiKeyID:requestModel:downstreamSessionID -> *wsConversationStateEntry

var wsConversationStoreStats struct {
	entries   atomic.Int64
	totalSize atomic.Int64
}

var wsConversationStoreSweepStop = make(chan struct{})

func init() {
	startWSConversationStoreSweeper()
}

func startWSConversationStoreSweeper() {
	go func() {
		ticker := time.NewTicker(wsConversationStoreSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sweepExpiredWSConversationStates()
			case <-wsConversationStoreSweepStop:
				return
			}
		}
	}()
}

func sweepExpiredWSConversationStates() {
	now := time.Now()
	removed := 0
	wsConversationStore.Range(func(key, value any) bool {
		entry, ok := value.(*wsConversationStateEntry)
		if !ok || entry == nil {
			if wsConversationStore.CompareAndDelete(key, value) {
				wsConversationStoreStats.entries.Add(-1)
				removed++
			}
			return true
		}
		if !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
			// CompareAndDelete so a concurrently stored entry is not discarded.
			if wsConversationStore.CompareAndDelete(key, entry) {
				wsConversationStoreStats.entries.Add(-1)
				wsConversationStoreStats.totalSize.Add(-int64(entry.size))
				removed++
			}
		}
		return true
	})
	if removed > 0 {
		log.Debugf("ws conversation store sweep: removed %d expired entries, current entries=%d, size=%d",
			removed, wsConversationStoreStats.entries.Load(), wsConversationStoreStats.totalSize.Load())
	}
}

func wsConversationStateKey(apiKeyID int, requestModel, downstreamSessionID string) string {
	return fmt.Sprintf("%d:%s:%s", apiKeyID, strings.TrimSpace(requestModel), strings.TrimSpace(downstreamSessionID))
}

func loadWSConversationState(apiKeyID int, requestModel, downstreamSessionID string) *wsConversationState {
	requestModel = strings.TrimSpace(requestModel)
	downstreamSessionID = strings.TrimSpace(downstreamSessionID)
	if requestModel == "" || downstreamSessionID == "" {
		return nil
	}

	key := wsConversationStateKey(apiKeyID, requestModel, downstreamSessionID)
	v, ok := wsConversationStore.Load(key)
	if !ok {
		return nil
	}

	entry, ok := v.(*wsConversationStateEntry)
	if !ok || entry == nil || entry.state == nil {
		if wsConversationStore.CompareAndDelete(key, v) {
			wsConversationStoreStats.entries.Add(-1)
		}
		return nil
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		if wsConversationStore.CompareAndDelete(key, entry) {
			wsConversationStoreStats.entries.Add(-1)
			wsConversationStoreStats.totalSize.Add(-int64(entry.size))
		}
		return nil
	}

	return cloneWSConversationState(entry.state)
}

func storeWSConversationState(apiKeyID int, requestModel string, state *wsConversationState, ttl time.Duration) {
	requestModel = strings.TrimSpace(requestModel)
	downstreamSessionID := ""
	if state != nil {
		downstreamSessionID = strings.TrimSpace(state.DownstreamSessionID)
	}
	if requestModel == "" || state == nil || downstreamSessionID == "" {
		return
	}
	if ttl <= 0 {
		ttl = wsClientMaxAge
	}

	cloned := cloneWSConversationState(state)
	if cloned == nil {
		return
	}
	cloned.RequestModel = requestModel

	key := wsConversationStateKey(apiKeyID, requestModel, downstreamSessionID)
	estimatedSize := estimateStateSize(cloned)
	newEntry := &wsConversationStateEntry{
		state:     cloned,
		expiresAt: time.Now().Add(ttl),
		size:      estimatedSize,
	}

	// Swap first, then reconcile stats against the previous value.
	old, loaded := wsConversationStore.Swap(key, newEntry)
	if loaded {
		oldEntry, ok := old.(*wsConversationStateEntry)
		if !ok || oldEntry == nil {
			wsConversationStoreStats.totalSize.Add(int64(estimatedSize))
			return
		}
		// Same key keeps its slot; only the size delta matters. A growing
		// transcript can still push the store over its limit, so roll back to
		// the previous entry rather than dropping the session outright.
		wsConversationStoreStats.totalSize.Add(int64(estimatedSize) - int64(oldEntry.size))
		if estimatedSize > oldEntry.size && wsConversationStoreStats.totalSize.Load() > wsConversationStoreMaxSize {
			if wsConversationStore.CompareAndSwap(key, newEntry, oldEntry) {
				wsConversationStoreStats.totalSize.Add(int64(oldEntry.size) - int64(estimatedSize))
			}
			log.Warnf("ws conversation store size limit reached after replacement (size=%d), rolling back",
				wsConversationStoreStats.totalSize.Load())
		}
		return
	}

	currentEntries := wsConversationStoreStats.entries.Add(1)
	wsConversationStoreStats.totalSize.Add(int64(estimatedSize))
	if currentEntries > wsConversationStoreMaxEntries ||
		wsConversationStoreStats.totalSize.Load() > wsConversationStoreMaxSize {
		if wsConversationStore.CompareAndDelete(key, newEntry) {
			wsConversationStoreStats.entries.Add(-1)
			wsConversationStoreStats.totalSize.Add(-int64(estimatedSize))
		}
		log.Warnf("ws conversation store capacity limit reached (entries=%d, size=%d), skipping save",
			currentEntries-1, wsConversationStoreStats.totalSize.Load())
	}
}

func deleteWSConversationState(apiKeyID int, requestModel, downstreamSessionID string) {
	requestModel = strings.TrimSpace(requestModel)
	downstreamSessionID = strings.TrimSpace(downstreamSessionID)
	if requestModel == "" || downstreamSessionID == "" {
		return
	}
	key := wsConversationStateKey(apiKeyID, requestModel, downstreamSessionID)
	if old, loaded := wsConversationStore.LoadAndDelete(key); loaded {
		wsConversationStoreStats.entries.Add(-1)
		if entry, ok := old.(*wsConversationStateEntry); ok && entry != nil {
			wsConversationStoreStats.totalSize.Add(-int64(entry.size))
		}
	}
}

func resolveWSConversationState(apiKeyID int, requestModel string, localState *wsConversationState, allowStoredRestore bool, downstreamSessionID string) *wsConversationState {
	requestModel = strings.TrimSpace(requestModel)
	downstreamSessionID = strings.TrimSpace(downstreamSessionID)
	if requestModel == "" {
		return localState
	}
	if localState != nil && localState.MatchesRequestModel(requestModel) {
		return localState
	}
	if !allowStoredRestore {
		return nil
	}
	return loadWSConversationState(apiKeyID, requestModel, downstreamSessionID)
}

func wsConversationStateToSticky(state *wsConversationState) *balancer.SessionEntry {
	if state == nil || state.ChannelID <= 0 {
		return nil
	}
	return &balancer.SessionEntry{
		ChannelID:    state.ChannelID,
		ChannelKeyID: state.ChannelKeyID,
		Timestamp:    time.Now(),
	}
}

func wsConversationStateTTL(sessionKeepTimeSec int) time.Duration {
	if sessionKeepTimeSec <= 0 {
		return wsClientMaxAge
	}
	ttl := time.Duration(sessionKeepTimeSec) * time.Second
	if ttl > wsClientMaxAge {
		return wsClientMaxAge
	}
	return ttl
}

// deleteWSConversationStatesBySession drops every model's state for one client
// connection. Called when the connection closes: those keys embed the
// connection's session ID and can never be looked up again.
func deleteWSConversationStatesBySession(apiKeyID int, downstreamSessionID string) {
	downstreamSessionID = strings.TrimSpace(downstreamSessionID)
	if downstreamSessionID == "" {
		return
	}
	prefix := fmt.Sprintf("%d:", apiKeyID)
	suffix := ":" + downstreamSessionID
	wsConversationStore.Range(func(key, value any) bool {
		keyStr, ok := key.(string)
		if !ok || !strings.HasPrefix(keyStr, prefix) || !strings.HasSuffix(keyStr, suffix) {
			return true
		}
		if wsConversationStore.CompareAndDelete(key, value) {
			wsConversationStoreStats.entries.Add(-1)
			if entry, ok := value.(*wsConversationStateEntry); ok && entry != nil {
				wsConversationStoreStats.totalSize.Add(-int64(entry.size))
			}
		}
		return true
	})
}

func resetWSConversationStateStore() {
	wsConversationStore = sync.Map{}
	wsConversationStoreStats.entries.Store(0)
	wsConversationStoreStats.totalSize.Store(0)
}
