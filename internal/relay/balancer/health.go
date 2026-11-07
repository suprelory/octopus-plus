package balancer

import (
	"container/heap"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

const (
	defaultChannelHealthLimit = 4096
	channelHealthTTL          = 30 * time.Minute
	channelLoadHalfLife       = time.Minute
	channelFailureHalfLife    = 5 * time.Minute
	channelHealthAlpha        = 0.2
	channelBaseRankPenalty    = 25.0
	channelInFlightPenalty    = 100.0
	channelLoadPenalty        = 20.0
	channelFailurePenalty     = 500.0
	channelLatencyPenalty     = 0.02
	channelConsecutivePenalty = 25.0
)

type channelHealthKey struct {
	channelID int
	modelName string
}

type channelHealthState struct {
	inFlight           int
	recentLoad         float64
	loadUpdated        time.Time
	latencyEWMA        float64
	failureEWMA        float64
	consecutiveFailure int
	samples            uint64
	failureUpdated     time.Time
	cooldownUntil      time.Time
	lastUsed           time.Time
	lastUsedSeq        uint64
}

type channelHealthSnapshot struct {
	InFlight           int
	RecentLoad         float64
	FailureRate        float64
	LatencyMillis      float64
	ConsecutiveFailure int
	CooldownUntil      time.Time
	DynamicScore       float64
}

type rankedHealthCandidate struct {
	item     model.GroupItem
	baseRank int
	score    float64
	health   channelHealthSnapshot
}

// rankedHealthHeap keeps the worst selected candidate at the root so a scan of
// all candidates can retain only the best K without sorting the full set.
type rankedHealthHeap struct {
	items []rankedHealthCandidate
	mode  model.GroupMode
}

func (h rankedHealthHeap) Len() int { return len(h.items) }
func (h rankedHealthHeap) Less(left, right int) bool {
	return compareRankedHealth(h.mode, h.items[right], h.items[left])
}
func (h rankedHealthHeap) Swap(left, right int) {
	h.items[left], h.items[right] = h.items[right], h.items[left]
}
func (h *rankedHealthHeap) Push(value any) { h.items = append(h.items, value.(rankedHealthCandidate)) }
func (h *rankedHealthHeap) Pop() any {
	last := len(h.items) - 1
	value := h.items[last]
	h.items = h.items[:last]
	return value
}

type channelHealthStore struct {
	mu          sync.Mutex
	entries     map[channelHealthKey]*channelHealthState
	maxEntries  int
	accessSeq   uint64
	lastCleanup time.Time
	now         func() time.Time
}

var globalChannelHealth = newChannelHealthStore(defaultChannelHealthLimit)

func newChannelHealthStore(maxEntries int) *channelHealthStore {
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &channelHealthStore{
		entries:    make(map[channelHealthKey]*channelHealthState),
		maxEntries: maxEntries,
		now:        time.Now,
	}
}

// channelHealthReservation represents the channel slot assigned to one real
// upstream attempt. Release is idempotent so both AttemptSpan.End and iterator
// cleanup can safely call it.
type channelHealthReservation struct {
	store  *channelHealthStore
	key    channelHealthKey
	active atomic.Bool
	once   sync.Once
}

func (r *channelHealthReservation) release() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.active.Store(false)
		if r.store != nil {
			r.store.release(r.key)
		}
	})
}

func (s *channelHealthStore) reserve(channelID int, modelName string) (*channelHealthReservation, channelHealthSnapshot) {
	key := channelHealthKey{channelID: channelID, modelName: strings.TrimSpace(modelName)}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	state, tracked := s.entryLocked(key, now)
	if !tracked {
		reservation := &channelHealthReservation{}
		reservation.active.Store(true)
		return reservation, channelHealthSnapshot{}
	}
	s.decayLoadLocked(state, now)
	snapshot := s.snapshotLocked(state, now)
	state.inFlight++
	state.recentLoad++
	state.loadUpdated = now
	state.lastUsed = now
	reservation := &channelHealthReservation{store: s, key: key}
	reservation.active.Store(true)
	return reservation, snapshot
}

func (s *channelHealthStore) release(key channelHealthKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, ok := s.entries[key]; ok && state.inFlight > 0 {
		state.inFlight--
		state.lastUsed = s.now()
	}
}

func (s *channelHealthStore) record(channelID int, modelName string, status model.AttemptStatus, duration time.Duration, failureClass string, retryAt time.Time, scope ...string) {
	if status != model.AttemptSuccess && (failureClass == "authentication" || failureClass == "permission" || (len(scope) > 0 && scope[0] == "key")) {
		return
	}
	key := channelHealthKey{channelID: channelID, modelName: strings.TrimSpace(modelName)}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	state, tracked := s.entryLocked(key, now)
	if !tracked {
		return
	}
	s.decayLoadLocked(state, now)
	state.lastUsed = now

	if status == model.AttemptSuccess {
		s.decayFailureLocked(state, now)
		state.failureEWMA *= 1 - channelHealthAlpha
		state.consecutiveFailure = 0
		state.cooldownUntil = time.Time{}
	} else if healthFailureClass(failureClass) {
		s.decayFailureLocked(state, now)
		state.failureEWMA = channelHealthAlpha + (1-channelHealthAlpha)*state.failureEWMA
		state.consecutiveFailure++
		if cooldown := channelHealthCooldown(failureClass, retryAt, now); cooldown.After(state.cooldownUntil) {
			state.cooldownUntil = cooldown
		}
	} else {
		return
	}
	if duration > 0 {
		s.updateLatencyLocked(state, duration)
	}
	state.samples++
}

func (s *channelHealthStore) snapshot(channelID int, modelName string) channelHealthSnapshot {
	now := s.now()
	key := channelHealthKey{channelID: channelID, modelName: strings.TrimSpace(modelName)}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.entries[key]
	if !ok {
		return channelHealthSnapshot{}
	}
	copy := *state
	s.decayLoadLocked(&copy, now)
	return s.snapshotLocked(&copy, now)
}

func (s *channelHealthStore) snapshotLocked(state *channelHealthState, now time.Time) channelHealthSnapshot {
	failureRate := decayedFailureRate(state, now)
	if failureRate < 0 {
		failureRate = 0
	}
	return channelHealthSnapshot{
		InFlight:           state.inFlight,
		RecentLoad:         state.recentLoad,
		FailureRate:        failureRate,
		LatencyMillis:      state.latencyEWMA,
		ConsecutiveFailure: state.consecutiveFailure,
		CooldownUntil:      state.cooldownUntil,
	}
}

func (s *channelHealthStore) rankCandidates(mode model.GroupMode, items []model.GroupItem, topK int) []model.GroupItem {
	if len(items) < 2 {
		return append([]model.GroupItem(nil), items...)
	}
	rankingNow := s.now()
	rankedItems := make([]rankedHealthCandidate, len(items))
	for index, item := range items {
		health := s.snapshot(item.ChannelID, item.ModelName)
		rankedItems[index] = rankedHealthCandidate{
			item:     item,
			baseRank: index,
			score:    healthPenalty(health, rankingNow) + float64(index)*channelBaseRankPenalty,
			health:   health,
		}
	}
	less := func(left, right int) bool {
		leftHard := healthCooldownActive(rankedItems[left].health, rankingNow)
		rightHard := healthCooldownActive(rankedItems[right].health, rankingNow)
		if leftHard != rightHard {
			return !leftHard
		}
		return compareRankedHealth(mode, rankedItems[left], rankedItems[right])
	}
	if topK <= 0 || topK >= len(rankedItems) {
		sort.SliceStable(rankedItems, less)
	} else {
		healthy := make([]rankedHealthCandidate, 0, len(rankedItems))
		hard := make([]rankedHealthCandidate, 0)
		for _, candidate := range rankedItems {
			if healthCooldownActive(candidate.health, rankingNow) {
				hard = append(hard, candidate)
			} else {
				healthy = append(healthy, candidate)
			}
		}
		if topK > len(healthy) {
			topK = len(healthy)
		}
		best := &rankedHealthHeap{mode: mode, items: make([]rankedHealthCandidate, 0, topK)}
		heap.Init(best)
		for _, candidate := range healthy {
			if best.Len() < topK {
				heap.Push(best, candidate)
				continue
			}
			if compareRankedHealth(mode, candidate, best.items[0]) {
				heap.Pop(best)
				heap.Push(best, candidate)
			}
		}
		selected := append([]rankedHealthCandidate(nil), best.items...)
		sort.SliceStable(selected, func(left, right int) bool {
			return compareRankedHealth(mode, selected[left], selected[right])
		})
		selectedRanks := make(map[int]struct{}, len(selected))
		for _, candidate := range selected {
			selectedRanks[candidate.baseRank] = struct{}{}
		}
		rankedItems = selected
		for _, candidate := range healthy {
			if _, selected := selectedRanks[candidate.baseRank]; !selected {
				rankedItems = append(rankedItems, candidate)
			}
		}
		rankedItems = append(rankedItems, hard...)
	}
	result := make([]model.GroupItem, len(rankedItems))
	for index := range rankedItems {
		result[index] = rankedItems[index].item
	}
	return result
}

func compareRankedHealth(mode model.GroupMode, left, right rankedHealthCandidate) bool {
	if mode == model.GroupModeFailover && left.item.Priority != right.item.Priority {
		return left.item.Priority < right.item.Priority
	}
	if left.score != right.score {
		return left.score < right.score
	}
	return left.baseRank < right.baseRank
}

func healthPenalty(snapshot channelHealthSnapshot, now time.Time) float64 {
	penalty := float64(snapshot.InFlight)*channelInFlightPenalty +
		snapshot.RecentLoad*channelLoadPenalty +
		snapshot.FailureRate*channelFailurePenalty +
		float64(snapshot.ConsecutiveFailure)*channelConsecutivePenalty
	if snapshot.LatencyMillis > 0 {
		penalty += snapshot.LatencyMillis * channelLatencyPenalty
	}
	if healthCooldownActive(snapshot, now) {
		penalty += 1_000_000
	}
	return penalty
}

func healthCooldownActive(snapshot channelHealthSnapshot, now time.Time) bool {
	return !snapshot.CooldownUntil.IsZero() && now.Before(snapshot.CooldownUntil)
}

func healthFailureClass(class string) bool {
	switch strings.TrimSpace(class) {
	case "transient", "rate_limit", "quota", "authentication", "permission", "model_unsupported":
		return true
	default:
		return false
	}
}

func channelHealthCooldown(class string, retryAt, now time.Time) time.Time {
	if retryAt.After(now) {
		return retryAt
	}
	switch strings.TrimSpace(class) {
	case "rate_limit":
		return now.Add(30 * time.Second)
	case "quota":
		return now.Add(5 * time.Minute)
	case "authentication", "permission", "model_unsupported":
		return now.Add(10 * time.Minute)
	default:
		return time.Time{}
	}
}

func (s *channelHealthStore) updateLatencyLocked(state *channelHealthState, duration time.Duration) {
	millis := float64(duration.Milliseconds())
	if millis <= 0 {
		return
	}
	if state.latencyEWMA <= 0 {
		state.latencyEWMA = millis
		return
	}
	state.latencyEWMA = channelHealthAlpha*millis + (1-channelHealthAlpha)*state.latencyEWMA
}

func (s *channelHealthStore) decayLoadLocked(state *channelHealthState, now time.Time) {
	if state.loadUpdated.IsZero() {
		state.loadUpdated = now
		return
	}
	if elapsed := now.Sub(state.loadUpdated); elapsed > 0 {
		state.recentLoad *= math.Exp(-elapsed.Seconds() / channelLoadHalfLife.Seconds())
		state.loadUpdated = now
	}
}

func (s *channelHealthStore) decayFailureLocked(state *channelHealthState, now time.Time) {
	state.failureEWMA = decayedFailureRate(state, now)
	state.failureUpdated = now
}

func decayedFailureRate(state *channelHealthState, now time.Time) float64 {
	if state.failureUpdated.IsZero() || !now.After(state.failureUpdated) {
		return state.failureEWMA
	}
	return state.failureEWMA * math.Exp(-now.Sub(state.failureUpdated).Seconds()/channelFailureHalfLife.Seconds())
}

func (s *channelHealthStore) entryLocked(key channelHealthKey, now time.Time) (*channelHealthState, bool) {
	if state, ok := s.entries[key]; ok {
		state.lastUsed = now
		s.accessSeq++
		state.lastUsedSeq = s.accessSeq
		return state, true
	}
	if len(s.entries) >= s.maxEntries {
		if !s.evictOldestLocked() {
			return nil, false
		}
	}
	s.accessSeq++
	state := &channelHealthState{lastUsed: now, lastUsedSeq: s.accessSeq, loadUpdated: now}
	s.entries[key] = state
	return state, true
}

func (s *channelHealthStore) cleanupLocked(now time.Time) {
	if !s.lastCleanup.IsZero() && now.Before(s.lastCleanup) {
		s.lastCleanup = now
	}
	if !s.lastCleanup.IsZero() && now.Sub(s.lastCleanup) < time.Minute && len(s.entries) < s.maxEntries {
		return
	}
	for key, state := range s.entries {
		if state.inFlight == 0 && now.Sub(state.lastUsed) > channelHealthTTL {
			delete(s.entries, key)
		}
	}
	s.lastCleanup = now
}

func (s *channelHealthStore) evictOldestLocked() bool {
	var oldestKey channelHealthKey
	var oldestSeq uint64
	found := false
	for key, state := range s.entries {
		if state.inFlight > 0 {
			continue
		}
		if !found || state.lastUsedSeq < oldestSeq {
			oldestKey = key
			oldestSeq = state.lastUsedSeq
			found = true
		}
	}
	if found {
		delete(s.entries, oldestKey)
	}
	return found
}

func (s *channelHealthStore) resetByChannel(channelID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.entries {
		if key.channelID == channelID {
			delete(s.entries, key)
		}
	}
}

func (s *channelHealthStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = make(map[channelHealthKey]*channelHealthState)
	s.accessSeq = 0
	s.lastCleanup = time.Time{}
}
