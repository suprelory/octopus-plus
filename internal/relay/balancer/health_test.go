package balancer

import (
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestChannelHealthTracksAttemptReservationAndRelease(t *testing.T) {
	Reset()
	group := model.Group{
		ID:    801,
		Mode:  model.GroupModeRoundRobin,
		Items: []model.GroupItem{{ID: 1, ChannelID: 81, ModelName: "upstream"}},
	}
	iterator := NewIterator(group, 1, "request")
	if !iterator.Next() {
		t.Fatal("expected candidate")
	}
	if got := globalChannelHealth.snapshot(81, "upstream").InFlight; got != 0 {
		t.Fatalf("candidate inspection must not reserve channel, got %d", got)
	}

	span := iterator.StartAttempt(81, 811, "channel")
	if got := globalChannelHealth.snapshot(81, "upstream").InFlight; got != 1 {
		t.Fatalf("expected one in-flight attempt, got %d", got)
	}
	span.SetFailure("transient", true, time.Time{})
	span.End(model.AttemptFailed, 502, "upstream failed")
	state := globalChannelHealth.snapshot(81, "upstream")
	if state.InFlight != 0 {
		t.Fatalf("expected reservation release, got %d", state.InFlight)
	}
	if state.FailureRate <= 0 {
		t.Fatalf("expected failure signal to be recorded, got %#v", state)
	}

	second := iterator.StartAttempt(81, 811, "channel")
	if got := globalChannelHealth.snapshot(81, "upstream").InFlight; got != 1 {
		t.Fatalf("expected retry to reserve a fresh slot, got %d", got)
	}
	second.End(model.AttemptSuccess, 200, "")
	if got := globalChannelHealth.snapshot(81, "upstream").InFlight; got != 0 {
		t.Fatalf("expected retry reservation release, got %d", got)
	}
}

func TestAttemptEndDoesNotReleaseNewerReservation(t *testing.T) {
	Reset()
	iterator := NewIterator(model.Group{
		ID:   807,
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 141, ModelName: "model-a"},
			{ID: 2, ChannelID: 142, ModelName: "model-a"},
		},
	}, 1, "request")
	if !iterator.Next() {
		t.Fatal("expected first candidate")
	}
	first := iterator.StartAttempt(141, 1401, "first")
	if !iterator.Next() {
		t.Fatal("expected second candidate")
	}
	second := iterator.StartAttempt(142, 1402, "second")

	first.End(model.AttemptFailed, 502, "late completion")
	if got := globalChannelHealth.snapshot(142, "model-a").InFlight; got != 1 {
		t.Fatalf("older span released current reservation, got %d", got)
	}
	second.End(model.AttemptSuccess, 200, "")
	if got := globalChannelHealth.snapshot(142, "model-a").InFlight; got != 0 {
		t.Fatalf("expected current reservation release, got %d", got)
	}
}

func TestChannelHealthFailureDecayIsAppliedBeforeUpdate(t *testing.T) {
	clock := time.Unix(1_000, 0)
	store := newChannelHealthStore(4)
	store.now = func() time.Time { return clock }
	store.record(151, "model-a", model.AttemptFailed, time.Second, "transient", time.Time{})
	clock = clock.Add(20 * time.Minute)
	store.record(151, "model-a", model.AttemptFailed, time.Second, "transient", time.Time{})

	failureRate := store.snapshot(151, "model-a").FailureRate
	if failureRate < channelHealthAlpha || failureRate >= 0.21 {
		t.Fatalf("stale failure signal was not decayed before update: %f", failureRate)
	}
}

func TestChannelHealthStoreRemainsBoundedWhenAllEntriesAreActive(t *testing.T) {
	store := newChannelHealthStore(2)
	first, _ := store.reserve(161, "model-a")
	second, _ := store.reserve(162, "model-a")
	third, _ := store.reserve(163, "model-a")
	defer first.release()
	defer second.release()
	defer third.release()

	store.mu.Lock()
	entryCount := len(store.entries)
	store.mu.Unlock()
	if entryCount != 2 {
		t.Fatalf("health state entries = %d, want strict limit 2", entryCount)
	}
}

func TestChannelHealthMovesRateLimitedCandidateBehindHealthyCandidate(t *testing.T) {
	Reset()
	deadline := time.Now().Add(time.Minute)
	globalChannelHealth.record(91, "model-a", model.AttemptFailed, 100*time.Millisecond, "rate_limit", deadline)
	iterator := NewIterator(model.Group{
		ID:   802,
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 91, ModelName: "model-a"},
			{ID: 2, ChannelID: 92, ModelName: "model-a"},
		},
	}, 1, "request")
	if !iterator.Next() {
		t.Fatal("expected candidate")
	}
	if got := iterator.Item().ChannelID; got != 92 {
		t.Fatalf("expected healthy candidate first, got channel %d", got)
	}
}

func TestChannelHealthAvoidsChannelWithActiveAttempt(t *testing.T) {
	Reset()
	group := model.Group{
		ID:   803,
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 101, ModelName: "model-a"},
			{ID: 2, ChannelID: 102, ModelName: "model-a"},
		},
	}
	first := NewIterator(group, 1, "request")
	if !first.Next() {
		t.Fatal("expected first candidate")
	}
	span := first.StartAttempt(101, 1001, "busy")
	defer span.End(model.AttemptSuccess, 200, "")

	second := NewIterator(group, 1, "request")
	if !second.Next() {
		t.Fatal("expected second candidate")
	}
	if got := second.Item().ChannelID; got != 102 {
		t.Fatalf("expected idle channel first, got channel %d", got)
	}
}

func TestChannelHealthRanksOnlyBestTopKAndKeepsFallbacks(t *testing.T) {
	Reset()
	reservation, _ := globalChannelHealth.reserve(111, "model-a")
	defer reservation.release()
	iterator := NewIterator(model.Group{
		ID:         804,
		Mode:       model.GroupModeRoundRobin,
		MaxRetries: 3,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 111, ModelName: "model-a"},
			{ID: 2, ChannelID: 112, ModelName: "model-a"},
			{ID: 3, ChannelID: 113, ModelName: "model-a"},
			{ID: 4, ChannelID: 114, ModelName: "model-a"},
			{ID: 5, ChannelID: 115, ModelName: "model-a"},
		},
	}, 1, "request")
	assertCandidateOrder(t, iterator, []int{112, 113, 114, 111, 115})
}

func TestChannelHealthPreservesFailoverPriorityUntilHardCooldown(t *testing.T) {
	Reset()
	group := model.Group{
		ID:   805,
		Mode: model.GroupModeFailover,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 121, ModelName: "model-a", Priority: 1},
			{ID: 2, ChannelID: 122, ModelName: "model-a", Priority: 2},
		},
	}
	globalChannelHealth.record(121, "model-a", model.AttemptFailed, 500*time.Millisecond, "transient", time.Time{})
	if got := firstCandidateChannel(t, group, "request", nil); got != 121 {
		t.Fatalf("soft health signal crossed failover priority, got channel %d", got)
	}

	globalChannelHealth.record(121, "model-a", model.AttemptFailed, 500*time.Millisecond, "rate_limit", time.Now().Add(time.Minute))
	if got := firstCandidateChannel(t, group, "request-2", nil); got != 122 {
		t.Fatalf("hard cooldown did not move candidate behind fallback, got channel %d", got)
	}
}

func TestChannelHealthAdjustsWeightedUsageToActualCandidate(t *testing.T) {
	Reset()
	group := model.Group{
		ID:         806,
		Mode:       model.GroupModeWeighted,
		MaxRetries: 3,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 131, ModelName: "model-a", Weight: 2},
			{ID: 2, ChannelID: 132, ModelName: "model-a", Weight: 1},
		},
	}
	reservation, _ := globalChannelHealth.reserve(131, "model-a")
	defer reservation.release()
	iterator := NewIterator(group, 1, "request")
	if !iterator.Next() {
		t.Fatal("expected candidate")
	}
	if iterator.Item().ChannelID != 132 {
		t.Fatalf("expected health ranking to select channel 132, got %#v", iterator.Item())
	}

	items := canonicalCandidates(group.Items)
	key := newStrategyStateKey(balanceScope{groupID: group.ID, requestModel: "request"}, model.GroupModeWeighted, items)
	globalStrategyState.mu.Lock()
	entry := globalStrategyState.entries[key]
	globalStrategyState.mu.Unlock()
	if entry == nil {
		t.Fatal("expected weighted strategy state")
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	selections := make(map[int]uint64)
	for _, candidate := range entry.candidates {
		selections[candidate.identity.channelID] = candidate.selections
	}
	if selections[131] != 0 || selections[132] != 1 {
		t.Fatalf("weighted usage charged wrong candidate: %#v", selections)
	}
}
