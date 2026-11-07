package balancer

import (
	"crypto/sha256"
	"encoding/binary"
	"math/bits"
	"sort"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

const (
	defaultStrategyStateLimit           = 4096
	defaultStrategyStateTTL             = 30 * time.Minute
	defaultStrategyStateCleanupInterval = time.Minute
	selectionRescaleThreshold           = uint64(1 << 63)
)

type balanceScope struct {
	groupID       int
	requestModel  string
	qualityTier   int
	qualityRanked bool
}

type strategyStateKey struct {
	balanceScope
	mode         model.GroupMode
	candidateSet [sha256.Size]byte
}

type candidateIdentity struct {
	itemID    int
	channelID int
	modelName string
}

type candidateState struct {
	identity   candidateIdentity
	weight     uint64
	selections uint64
}

type strategyStateEntry struct {
	mu          sync.Mutex
	candidates  []candidateState
	next        uint64
	lastUsed    time.Time
	lastUsedSeq uint64
}

type strategyStateStore struct {
	mu              sync.Mutex
	entries         map[strategyStateKey]*strategyStateEntry
	maxEntries      int
	ttl             time.Duration
	cleanupInterval time.Duration
	lastCleanup     time.Time
	accessSeq       uint64
	now             func() time.Time
}

var globalStrategyState = newStrategyStateStore(
	defaultStrategyStateLimit,
	defaultStrategyStateTTL,
	defaultStrategyStateCleanupInterval,
)

func newStrategyStateStore(maxEntries int, ttl, cleanupInterval time.Duration) *strategyStateStore {
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &strategyStateStore{
		entries:         make(map[strategyStateKey]*strategyStateEntry),
		maxEntries:      maxEntries,
		ttl:             ttl,
		cleanupInterval: cleanupInterval,
		now:             time.Now,
	}
}

func (s *strategyStateStore) roundRobin(scope balanceScope, items []model.GroupItem) []model.GroupItem {
	if len(items) == 0 {
		return nil
	}
	items = canonicalCandidates(items)

	s.mu.Lock()
	entry := s.entryLocked(newStrategyStateKey(scope, model.GroupModeRoundRobin, items), items)
	s.mu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()
	start := int(entry.next % uint64(len(items)))
	entry.next = uint64((start + 1) % len(items))

	result := make([]model.GroupItem, len(items))
	for index := range items {
		result[index] = items[(start+index)%len(items)]
	}
	return result
}

// preview copies only the relevant strategy entry. Existing ordering code runs
// against the private copy, so previews cannot consume turns or weighted credit.
func (s *strategyStateStore) preview(mode model.GroupMode, scope balanceScope, items []model.GroupItem) []model.GroupItem {
	if mode == model.GroupModeFailover {
		return sortByPriority(items)
	}
	items = canonicalCandidates(items)
	key := newStrategyStateKey(scope, mode, items)
	scratch := newStrategyStateStore(2, s.ttl, s.cleanupInterval)
	s.mu.Lock()
	if entry, ok := s.entries[key]; ok {
		entry.mu.Lock()
		scratch.entries[key] = &strategyStateEntry{candidates: append([]candidateState(nil), entry.candidates...), next: entry.next, lastUsed: entry.lastUsed, lastUsedSeq: entry.lastUsedSeq}
		entry.mu.Unlock()
	}
	scratch.lastCleanup, scratch.now = s.lastCleanup, s.now
	if len(s.entries) >= s.maxEntries {
		scratch.lastCleanup = time.Time{}
	}
	s.mu.Unlock()
	if mode == model.GroupModeWeighted {
		return scratch.weighted(scope, items)
	}
	return scratch.roundRobin(scope, items)
}

func (s *strategyStateStore) weighted(scope balanceScope, items []model.GroupItem) []model.GroupItem {
	if len(items) == 0 {
		return nil
	}
	items = canonicalCandidates(items)

	s.mu.Lock()
	entry := s.entryLocked(newStrategyStateKey(scope, model.GroupModeWeighted, items), items)
	s.mu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()
	rescaleSelections(entry.candidates)

	order := make([]int, len(items))
	for index := range order {
		order[index] = index
	}
	sort.SliceStable(order, func(left, right int) bool {
		return weightedUsageLess(entry.candidates[order[left]], entry.candidates[order[right]])
	})
	entry.candidates[order[0]].selections++

	result := make([]model.GroupItem, len(items))
	for index, candidateIndex := range order {
		result[index] = items[candidateIndex]
	}
	return result
}

func (s *strategyStateStore) adjustWeightedSelection(scope balanceScope, items []model.GroupItem, charged, selected model.GroupItem) {
	if groupItemIdentity(charged) == groupItemIdentity(selected) {
		return
	}
	items = canonicalCandidates(items)
	key := newStrategyStateKey(scope, model.GroupModeWeighted, items)
	s.mu.Lock()
	entry, ok := s.entries[key]
	s.mu.Unlock()
	if !ok {
		return
	}
	chargedIdentity := groupItemIdentity(charged)
	selectedIdentity := groupItemIdentity(selected)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	for index := range entry.candidates {
		switch entry.candidates[index].identity {
		case chargedIdentity:
			if entry.candidates[index].selections > 0 {
				entry.candidates[index].selections--
			}
		case selectedIdentity:
			entry.candidates[index].selections++
		}
	}
}

func (s *strategyStateStore) entryLocked(key strategyStateKey, items []model.GroupItem) *strategyStateEntry {
	now := s.now()
	s.cleanupLocked(now)

	entry, ok := s.entries[key]
	if !ok {
		if len(s.entries) >= s.maxEntries {
			s.evictOldestLocked()
		}
		entry = newStrategyStateEntry(items, key.mode == model.GroupModeWeighted, now)
		s.entries[key] = entry
	}
	s.accessSeq++
	entry.lastUsed = now
	entry.lastUsedSeq = s.accessSeq
	return entry
}

func newStrategyStateEntry(items []model.GroupItem, weighted bool, now time.Time) *strategyStateEntry {
	candidates := make([]candidateState, len(items))
	for index, item := range items {
		candidates[index] = candidateState{
			identity: candidateIdentity{
				itemID:    item.ID,
				channelID: item.ChannelID,
				modelName: item.ModelName,
			},
		}
		if weighted {
			candidates[index].weight = normalizedWeight(item.Weight)
		}
	}
	return &strategyStateEntry{candidates: candidates, lastUsed: now}
}

func normalizedWeight(weight int) uint64 {
	if weight <= 0 {
		return 1
	}
	return uint64(weight)
}

func weightedUsageLess(left, right candidateState) bool {
	leftHigh, leftLow := bits.Mul64(left.selections, right.weight)
	rightHigh, rightLow := bits.Mul64(right.selections, left.weight)
	if leftHigh != rightHigh {
		return leftHigh < rightHigh
	}
	return leftLow < rightLow
}

func rescaleSelections(candidates []candidateState) {
	for _, candidate := range candidates {
		if candidate.selections < selectionRescaleThreshold {
			continue
		}
		for index := range candidates {
			candidates[index].selections /= 2
		}
		return
	}
}

func (s *strategyStateStore) cleanupLocked(now time.Time) {
	if !s.lastCleanup.IsZero() && now.Before(s.lastCleanup) {
		s.lastCleanup = now
		for _, entry := range s.entries {
			if entry.lastUsed.After(now) {
				entry.lastUsed = now
			}
		}
	}
	cleanupDue := s.lastCleanup.IsZero() || now.Sub(s.lastCleanup) >= s.cleanupInterval
	if !cleanupDue && len(s.entries) < s.maxEntries {
		return
	}
	if s.ttl > 0 {
		for key, entry := range s.entries {
			age := now.Sub(entry.lastUsed)
			if age > s.ttl {
				delete(s.entries, key)
			} else if age < 0 {
				entry.lastUsed = now
			}
		}
	}
	s.lastCleanup = now
}

func (s *strategyStateStore) evictOldestLocked() {
	var oldestKey strategyStateKey
	var oldestSeq uint64
	found := false
	for key, entry := range s.entries {
		if !found || entry.lastUsedSeq < oldestSeq {
			oldestKey = key
			oldestSeq = entry.lastUsedSeq
			found = true
		}
	}
	if found {
		delete(s.entries, oldestKey)
	}
}

func (s *strategyStateStore) resetByChannel(channelID int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, entry := range s.entries {
		for _, candidate := range entry.candidates {
			if candidate.identity.channelID == channelID {
				delete(s.entries, key)
				break
			}
		}
	}
}

func (s *strategyStateStore) resetByGroup(groupID int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key := range s.entries {
		if key.groupID == groupID {
			delete(s.entries, key)
		}
	}
}

func (s *strategyStateStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = make(map[strategyStateKey]*strategyStateEntry)
	s.lastCleanup = time.Time{}
	s.accessSeq = 0
}

func canonicalCandidates(items []model.GroupItem) []model.GroupItem {
	result := append([]model.GroupItem(nil), items...)
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].ID != result[right].ID {
			return result[left].ID < result[right].ID
		}
		if result[left].ChannelID != result[right].ChannelID {
			return result[left].ChannelID < result[right].ChannelID
		}
		if result[left].ModelName != result[right].ModelName {
			return result[left].ModelName < result[right].ModelName
		}
		if result[left].Priority != result[right].Priority {
			return result[left].Priority < result[right].Priority
		}
		return result[left].Weight < result[right].Weight
	})
	return result
}

func newStrategyStateKey(scope balanceScope, mode model.GroupMode, items []model.GroupItem) strategyStateKey {
	hash := sha256.New()
	var encoded [8]byte
	writeInt := func(value int) {
		binary.BigEndian.PutUint64(encoded[:], uint64(int64(value)))
		_, _ = hash.Write(encoded[:])
	}
	for _, item := range items {
		writeInt(item.ID)
		writeInt(item.ChannelID)
		writeInt(len(item.ModelName))
		_, _ = hash.Write([]byte(item.ModelName))
		if mode == model.GroupModeWeighted {
			binary.BigEndian.PutUint64(encoded[:], normalizedWeight(item.Weight))
			_, _ = hash.Write(encoded[:])
		}
	}
	var candidateSet [sha256.Size]byte
	copy(candidateSet[:], hash.Sum(nil))
	return strategyStateKey{balanceScope: scope, mode: mode, candidateSet: candidateSet}
}
