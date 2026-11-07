package balancer

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestWeightedCandidatesFollowTwoToOneRatio(t *testing.T) {
	Reset()
	group := model.Group{
		ID:   101,
		Mode: model.GroupModeWeighted,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 1, ModelName: "upstream-a", Weight: 2},
			{ID: 2, ChannelID: 2, ModelName: "upstream-b", Weight: 1},
		},
	}

	counts := make(map[int]int)
	for index := 0; index < 300; index++ {
		counts[firstCandidateChannel(t, group, "request-model", nil)]++
	}
	if counts[1] != 200 || counts[2] != 100 {
		t.Fatalf("weighted distribution = %d:%d, want 200:100", counts[1], counts[2])
	}
}

func TestRoundRobinStateIsolatedByGroupAndModel(t *testing.T) {
	Reset()
	groupA := model.Group{
		ID:   201,
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 1, ModelName: "upstream-a"},
			{ID: 2, ChannelID: 2, ModelName: "upstream-b"},
		},
	}
	groupB := model.Group{
		ID:   202,
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ID: 3, ChannelID: 3, ModelName: "upstream-c"},
			{ID: 4, ChannelID: 4, ModelName: "upstream-d"},
		},
	}

	if got := firstCandidateChannel(t, groupA, "model-a", nil); got != 1 {
		t.Fatalf("first group A candidate = %d, want 1", got)
	}
	if got := firstCandidateChannel(t, groupB, "model-a", nil); got != 3 {
		t.Fatalf("first group B candidate = %d, want 3", got)
	}
	if got := firstCandidateChannel(t, groupA, "model-b", nil); got != 1 {
		t.Fatalf("first model B candidate = %d, want 1", got)
	}
	if got := firstCandidateChannel(t, groupA, "model-a", nil); got != 2 {
		t.Fatalf("second group A/model A candidate = %d, want 2", got)
	}
}

func TestRoundRobinQualityTiersAdvanceIndependently(t *testing.T) {
	Reset()
	group := model.Group{
		ID:   301,
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 1, ModelName: "native-a"},
			{ID: 2, ChannelID: 2, ModelName: "native-b"},
			{ID: 3, ChannelID: 3, ModelName: "translated-a"},
			{ID: 4, ChannelID: 4, ModelName: "translated-b"},
			{ID: 5, ChannelID: 5, ModelName: "translated-c"},
		},
	}
	quality := func(item model.GroupItem) int {
		if item.ChannelID <= 2 {
			return 0
		}
		return 1
	}

	assertCandidateOrder(t, NewIteratorWithPreferenceAndQuality(group, 1, "request-model", nil, quality), []int{1, 2, 3, 4, 5})
	assertCandidateOrder(t, NewIteratorWithPreferenceAndQuality(group, 1, "request-model", nil, quality), []int{2, 1, 4, 5, 3})
	assertCandidateOrder(t, NewIteratorWithPreferenceAndQuality(group, 1, "request-model", nil, quality), []int{1, 2, 5, 3, 4})
}

func TestPreferencesStayWithinBestQualityTier(t *testing.T) {
	Reset()
	const (
		apiKeyID     = 77
		requestModel = "request-model"
	)
	group := model.Group{
		ID:   401,
		Mode: model.GroupModeFailover,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 1, ModelName: "native", Priority: 1},
			{ID: 2, ChannelID: 2, ModelName: "translated", Priority: 2},
		},
	}
	SetChannelAffinity(apiKeyID, group.ID, requestModel, 2, 202)
	iterator := NewIteratorWithPreferenceAndQuality(
		group,
		apiKeyID,
		requestModel,
		&SessionEntry{ChannelID: 1, ChannelKeyID: 101},
		func(item model.GroupItem) int {
			if item.ChannelID == 1 {
				return 0
			}
			return 1
		},
	)

	if !iterator.Next() || iterator.PreferenceSource() != PreferenceResponsesReplay {
		t.Fatal("expected replay preference in the best quality tier")
	}
	if !iterator.Next() {
		t.Fatal("expected lower-quality fallback candidate")
	}
	if source := iterator.PreferenceSource(); source != PreferenceNone {
		t.Fatalf("lower-quality candidate preference = %d, want none", source)
	}
}

func TestWeightedCandidatesAreConcurrentSafe(t *testing.T) {
	Reset()
	group := model.Group{
		ID:   501,
		Mode: model.GroupModeWeighted,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 1, ModelName: "upstream-a", Weight: 2},
			{ID: 2, ChannelID: 2, ModelName: "upstream-b", Weight: 1},
		},
	}

	var counts [3]atomic.Int64
	var waitGroup sync.WaitGroup
	for index := 0; index < 120; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			iterator := NewIterator(group, 1, "request-model")
			if iterator.Next() {
				counts[iterator.Item().ChannelID].Add(1)
			}
		}()
	}
	waitGroup.Wait()
	if first, second := counts[1].Load(), counts[2].Load(); first != 80 || second != 40 {
		t.Fatalf("concurrent weighted distribution = %d:%d, want 80:40", first, second)
	}
}

func TestRoundRobinCandidatesAreConcurrentSafe(t *testing.T) {
	Reset()
	group := model.Group{
		ID:   502,
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 1, ModelName: "upstream-a"},
			{ID: 2, ChannelID: 2, ModelName: "upstream-b"},
		},
	}

	var counts [3]atomic.Int64
	var waitGroup sync.WaitGroup
	for index := 0; index < 120; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			iterator := NewIterator(group, 1, "request-model")
			if iterator.Next() {
				counts[iterator.Item().ChannelID].Add(1)
			}
		}()
	}
	waitGroup.Wait()
	if first, second := counts[1].Load(), counts[2].Load(); first != 60 || second != 60 {
		t.Fatalf("concurrent round-robin distribution = %d:%d, want 60:60", first, second)
	}
}

func TestWeightedCandidatesNormalizeInvalidAndLargeWeights(t *testing.T) {
	Reset()
	invalidWeights := model.Group{
		ID:   601,
		Mode: model.GroupModeWeighted,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 1, ModelName: "zero", Weight: 0},
			{ID: 2, ChannelID: 2, ModelName: "negative", Weight: -10},
		},
	}
	counts := make(map[int]int)
	for index := 0; index < 20; index++ {
		counts[firstCandidateChannel(t, invalidWeights, "request-model", nil)]++
	}
	if counts[1] != 10 || counts[2] != 10 {
		t.Fatalf("normalized invalid-weight distribution = %d:%d, want 10:10", counts[1], counts[2])
	}

	Reset()
	maxInt := int(^uint(0) >> 1)
	largeWeights := model.Group{
		ID:   602,
		Mode: model.GroupModeWeighted,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 1, ModelName: "large-a", Weight: maxInt},
			{ID: 2, ChannelID: 2, ModelName: "large-b", Weight: maxInt / 2},
		},
	}
	counts = make(map[int]int)
	for index := 0; index < 300; index++ {
		counts[firstCandidateChannel(t, largeWeights, "request-model", nil)]++
	}
	if counts[1] < 199 || counts[1] > 201 || counts[2] < 99 || counts[2] > 101 {
		t.Fatalf("large-weight distribution = %d:%d, want approximately 200:100", counts[1], counts[2])
	}
}

func TestStrategyStateResetsForConfigurationAndChannelChanges(t *testing.T) {
	Reset()
	weighted := model.Group{
		ID:   701,
		Mode: model.GroupModeWeighted,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 1, ModelName: "upstream-a", Weight: 1},
			{ID: 2, ChannelID: 2, ModelName: "upstream-b", Weight: 1},
		},
	}
	if got := firstCandidateChannel(t, weighted, "request-model", nil); got != 1 {
		t.Fatalf("first equal-weight candidate = %d, want 1", got)
	}
	if got := firstCandidateChannel(t, weighted, "request-model", nil); got != 2 {
		t.Fatalf("second equal-weight candidate = %d, want 2", got)
	}
	weighted.Items[1].Weight = 2
	if got := firstCandidateChannel(t, weighted, "request-model", nil); got != 1 {
		t.Fatalf("candidate after weight change = %d, want fresh-state channel 1", got)
	}

	roundRobin := model.Group{
		ID:   702,
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ID: 3, ChannelID: 10, ModelName: "upstream-c"},
			{ID: 4, ChannelID: 11, ModelName: "upstream-d"},
		},
	}
	unrelated := model.Group{
		ID:   703,
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ID: 5, ChannelID: 20, ModelName: "upstream-e"},
			{ID: 6, ChannelID: 21, ModelName: "upstream-f"},
		},
	}
	firstCandidateChannel(t, roundRobin, "request-model", nil)
	firstCandidateChannel(t, unrelated, "request-model", nil)
	if got := firstCandidateChannel(t, roundRobin, "request-model", nil); got != 11 {
		t.Fatalf("second round-robin candidate = %d, want 11", got)
	}
	ResetStateByChannel(10)
	if got := firstCandidateChannel(t, roundRobin, "request-model", nil); got != 10 {
		t.Fatalf("candidate after channel reset = %d, want 10", got)
	}
	if got := firstCandidateChannel(t, unrelated, "request-model", nil); got != 21 {
		t.Fatalf("unrelated state after channel reset = %d, want 21", got)
	}
}

func TestGroupResetDoesNotAffectGroupsSharingAChannel(t *testing.T) {
	Reset()
	groupA := model.Group{
		ID:   704,
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 10, ModelName: "shared"},
			{ID: 2, ChannelID: 11, ModelName: "group-a"},
		},
	}
	groupB := model.Group{
		ID:   705,
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ID: 3, ChannelID: 10, ModelName: "shared"},
			{ID: 4, ChannelID: 12, ModelName: "group-b"},
		},
	}

	firstCandidateChannel(t, groupA, "request-model", nil)
	firstCandidateChannel(t, groupB, "request-model", nil)
	ResetStateByGroup(groupA.ID)
	if got := firstCandidateChannel(t, groupA, "request-model", nil); got != 10 {
		t.Fatalf("group A candidate after group reset = %d, want 10", got)
	}
	if got := firstCandidateChannel(t, groupB, "request-model", nil); got != 12 {
		t.Fatalf("group B candidate after group A reset = %d, want 12", got)
	}
}

func TestStrategyStateStoreExpiresAndBoundsEntries(t *testing.T) {
	clock := time.Unix(1_000, 0)
	store := newStrategyStateStore(2, time.Minute, time.Minute)
	store.now = func() time.Time { return clock }
	items := []model.GroupItem{{ID: 1, ChannelID: 1, ModelName: "upstream"}}
	scopeOne := balanceScope{groupID: 1, requestModel: "model"}
	scopeTwo := balanceScope{groupID: 2, requestModel: "model"}
	scopeThree := balanceScope{groupID: 3, requestModel: "model"}
	scopeFour := balanceScope{groupID: 4, requestModel: "model"}

	store.roundRobin(scopeOne, items)
	clock = clock.Add(30 * time.Second)
	store.roundRobin(scopeTwo, items)
	clock = clock.Add(40 * time.Second)
	store.roundRobin(scopeThree, items)

	keyOne := newStrategyStateKey(scopeOne, model.GroupModeRoundRobin, items)
	keyTwo := newStrategyStateKey(scopeTwo, model.GroupModeRoundRobin, items)
	store.mu.Lock()
	_, expiredStillPresent := store.entries[keyOne]
	entryCount := len(store.entries)
	store.mu.Unlock()
	if expiredStillPresent || entryCount != 2 {
		t.Fatalf("post-expiry state: expired_present=%t entries=%d, want false/2", expiredStillPresent, entryCount)
	}

	store.roundRobin(scopeFour, items)
	store.mu.Lock()
	_, oldestStillPresent := store.entries[keyTwo]
	entryCount = len(store.entries)
	store.mu.Unlock()
	if oldestStillPresent || entryCount != 2 {
		t.Fatalf("bounded state: oldest_present=%t entries=%d, want false/2", oldestStillPresent, entryCount)
	}
}

func TestStickyPreferenceDoesNotAdvanceWeightedState(t *testing.T) {
	Reset()
	const (
		apiKeyID     = 17
		requestModel = "request-model"
	)
	group := model.Group{
		ID:   801,
		Mode: model.GroupModeWeighted,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 1, ModelName: "upstream-a", Weight: 1},
			{ID: 2, ChannelID: 2, ModelName: "upstream-b", Weight: 1},
		},
	}
	SetChannelAffinity(apiKeyID, group.ID, requestModel, 2, 22)
	for index := 0; index < 5; index++ {
		iterator := NewIterator(group, apiKeyID, requestModel)
		if !iterator.Next() || iterator.Item().ChannelID != 2 || !iterator.IsSticky() {
			t.Fatalf("sticky selection %d = %#v", index, iterator)
		}
	}
	DeleteChannelAffinity(apiKeyID, group.ID, requestModel)
	if got := firstCandidateChannel(t, group, requestModel, nil); got != 1 {
		t.Fatalf("first ordinary candidate after sticky traffic = %d, want 1", got)
	}
}

func TestUnvisitedLowerQualityTierDoesNotAdvance(t *testing.T) {
	Reset()
	group := model.Group{
		ID:   802,
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 1, ModelName: "native-a"},
			{ID: 2, ChannelID: 2, ModelName: "native-b"},
			{ID: 3, ChannelID: 3, ModelName: "fallback-a"},
			{ID: 4, ChannelID: 4, ModelName: "fallback-b"},
		},
	}
	quality := func(item model.GroupItem) int {
		if item.ChannelID < 3 {
			return 0
		}
		return 1
	}
	for index := 0; index < 3; index++ {
		iterator := NewIteratorWithPreferenceAndQuality(group, 1, "request-model", nil, quality)
		if !iterator.Next() {
			t.Fatal("expected best-tier candidate")
		}
	}

	iterator := NewIteratorWithPreferenceAndQuality(group, 1, "request-model", nil, quality)
	for iterator.Next() {
		if quality(iterator.Item()) == 1 {
			if iterator.Item().ChannelID != 3 {
				t.Fatalf("first visited fallback candidate = %d, want 3", iterator.Item().ChannelID)
			}
			return
		}
	}
	t.Fatal("expected fallback tier")
}

func TestCandidateSetVariantsKeepIndependentState(t *testing.T) {
	store := newStrategyStateStore(8, time.Hour, time.Minute)
	scope := balanceScope{groupID: 9, requestModel: "model", qualityTier: 1, qualityRanked: true}
	itemsAB := []model.GroupItem{
		{ID: 1, ChannelID: 1, ModelName: "a"},
		{ID: 2, ChannelID: 2, ModelName: "b"},
	}
	itemsAC := []model.GroupItem{
		{ID: 1, ChannelID: 1, ModelName: "a"},
		{ID: 3, ChannelID: 3, ModelName: "c"},
	}
	if got := store.roundRobin(scope, itemsAB)[0].ChannelID; got != 1 {
		t.Fatalf("first AB candidate = %d, want 1", got)
	}
	if got := store.roundRobin(scope, itemsAC)[0].ChannelID; got != 1 {
		t.Fatalf("first AC candidate = %d, want 1", got)
	}
	if got := store.roundRobin(scope, itemsAB)[0].ChannelID; got != 2 {
		t.Fatalf("second AB candidate after AC request = %d, want 2", got)
	}
}

func TestStrategyStateHandlesClockRollback(t *testing.T) {
	clock := time.Unix(2_000, 0)
	store := newStrategyStateStore(2, time.Minute, time.Minute)
	store.now = func() time.Time { return clock }
	items := []model.GroupItem{{ID: 1, ChannelID: 1, ModelName: "upstream"}}
	store.roundRobin(balanceScope{groupID: 1}, items)
	clock = clock.Add(-time.Hour)
	store.roundRobin(balanceScope{groupID: 2}, items)
	clock = clock.Add(2 * time.Minute)
	store.roundRobin(balanceScope{groupID: 3}, items)

	store.mu.Lock()
	entryCount := len(store.entries)
	store.mu.Unlock()
	if entryCount > 2 {
		t.Fatalf("state bound after clock rollback = %d, want <= 2", entryCount)
	}
}

func firstCandidateChannel(t *testing.T, group model.Group, requestModel string, quality QualityRanker) int {
	t.Helper()
	iterator := NewIteratorWithPreferenceAndQuality(group, 1, requestModel, nil, quality)
	if !iterator.Next() {
		t.Fatal("expected at least one candidate")
	}
	return iterator.Item().ChannelID
}

func assertCandidateOrder(t *testing.T, iterator *Iterator, expected []int) {
	t.Helper()
	actual := make([]int, 0, iterator.Len())
	for iterator.Next() {
		actual = append(actual, iterator.Item().ChannelID)
	}
	if len(actual) != len(expected) {
		t.Fatalf("candidate count = %d, want %d", len(actual), len(expected))
	}
	for index, channelID := range expected {
		if got := actual[index]; got != channelID {
			t.Fatalf("candidate %d = channel %d, want %d", index, got, channelID)
		}
	}
}
