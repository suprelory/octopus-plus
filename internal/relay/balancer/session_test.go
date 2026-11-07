package balancer

import (
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestChannelAffinityIsolatedByAPIKeyGroupAndModel(t *testing.T) {
	Reset()

	SetChannelAffinity(1, 101, "gpt-4o", 10, 100)
	SetChannelAffinity(2, 101, "gpt-4o", 20, 200)
	SetChannelAffinity(1, 202, "claude-3-5", 30, 300)
	SetChannelAffinity(1, 303, "gpt-4o", 40, 400)

	tests := []struct {
		apiKeyID     int
		groupID      int
		requestModel string
		channelID    int
		keyID        int
	}{
		{apiKeyID: 1, groupID: 101, requestModel: "gpt-4o", channelID: 10, keyID: 100},
		{apiKeyID: 2, groupID: 101, requestModel: "gpt-4o", channelID: 20, keyID: 200},
		{apiKeyID: 1, groupID: 202, requestModel: "claude-3-5", channelID: 30, keyID: 300},
		{apiKeyID: 1, groupID: 303, requestModel: "gpt-4o", channelID: 40, keyID: 400},
	}
	for _, test := range tests {
		entry := GetChannelAffinity(test.apiKeyID, test.groupID, test.requestModel)
		if entry == nil || entry.ChannelID != test.channelID || entry.ChannelKeyID != test.keyID {
			t.Fatalf("unexpected affinity for api key %d/model %q: %#v", test.apiKeyID, test.requestModel, entry)
		}
	}
	if entry := GetChannelAffinity(3, 101, "gpt-4o"); entry != nil {
		t.Fatalf("expected unrelated API key to have no affinity, got %#v", entry)
	}
	if entry := GetChannelAffinity(1, 404, "gpt-4o"); entry != nil {
		t.Fatalf("expected unrelated group to have no affinity, got %#v", entry)
	}
}

func TestChannelAffinityExpiresLazily(t *testing.T) {
	Reset()
	key := sessionKey(1, 11, "gpt-4o")
	channelAffinity.Store(key, &SessionEntry{
		GroupID:      11,
		ChannelID:    10,
		ChannelKeyID: 100,
		Timestamp:    time.Now().Add(-channelAffinityTTL() - time.Second),
	})

	if entry := GetChannelAffinity(1, 11, "gpt-4o"); entry != nil {
		t.Fatalf("expected expired affinity to be ignored, got %#v", entry)
	}
	if _, ok := channelAffinity.Load(key); ok {
		t.Fatal("expected expired affinity to be deleted during lookup")
	}
}

func TestIteratorPreferencePriority(t *testing.T) {
	Reset()
	const (
		apiKeyID     = 7
		groupID      = 71
		requestModel = "routing-model"
	)
	group := model.Group{
		ID:              groupID,
		Mode:            model.GroupModeFailover,
		SessionKeepTime: 60,
		Items: []model.GroupItem{
			{ChannelID: 1, ModelName: "upstream-1", Priority: 1},
			{ChannelID: 2, ModelName: "upstream-2", Priority: 2},
			{ChannelID: 3, ModelName: "upstream-3", Priority: 3},
		},
	}
	SetChannelAffinity(apiKeyID, groupID, requestModel, 2, 202)

	iterator := NewIteratorWithPreference(group, apiKeyID, requestModel, &SessionEntry{ChannelID: 1, ChannelKeyID: 101})
	expected := []struct {
		channelID int
		keyID     int
		source    PreferenceSource
	}{
		{channelID: 1, keyID: 101, source: PreferenceResponsesReplay},
		{channelID: 2, keyID: 202, source: PreferenceChannelAffinity},
		{channelID: 3, keyID: 0, source: PreferenceNone},
	}
	for index, want := range expected {
		if !iterator.Next() {
			t.Fatalf("expected candidate %d", index)
		}
		if got := iterator.Item().ChannelID; got != want.channelID {
			t.Fatalf("candidate %d channel = %d, want %d", index, got, want.channelID)
		}
		if got := iterator.StickyKeyID(); got != want.keyID {
			t.Fatalf("candidate %d preferred key = %d, want %d", index, got, want.keyID)
		}
		if got := iterator.PreferenceSource(); got != want.source {
			t.Fatalf("candidate %d source = %d, want %d", index, got, want.source)
		}
	}
	if iterator.Next() {
		t.Fatal("expected iterator to be exhausted")
	}
}

func TestIteratorFallsBackFromMissingReplayChannelToAffinity(t *testing.T) {
	Reset()
	const (
		apiKeyID     = 8
		groupID      = 81
		requestModel = "routing-model"
	)
	group := model.Group{
		ID:              groupID,
		Mode:            model.GroupModeFailover,
		SessionKeepTime: 60,
		Items: []model.GroupItem{
			{ChannelID: 1, ModelName: "upstream-1", Priority: 1},
			{ChannelID: 2, ModelName: "upstream-2", Priority: 2},
		},
	}
	SetChannelAffinity(apiKeyID, groupID, requestModel, 2, 202)

	iterator := NewIteratorWithPreference(group, apiKeyID, requestModel, &SessionEntry{ChannelID: 999, ChannelKeyID: 9999})
	if !iterator.Next() {
		t.Fatal("expected affinity candidate after missing replay channel")
	}
	if got := iterator.Item().ChannelID; got != 2 {
		t.Fatalf("expected affinity channel 2 first, got %d", got)
	}
	if got := iterator.PreferenceSource(); got != PreferenceChannelAffinity {
		t.Fatalf("expected channel affinity preference, got %d", got)
	}
	if got := iterator.StickyKeyID(); got != 202 {
		t.Fatalf("expected affinity key 202, got %d", got)
	}
}

func TestIteratorClearsOnlyCurrentGroupAffinityWhenChannelIsMissing(t *testing.T) {
	Reset()
	const (
		apiKeyID     = 12
		requestModel = "routing-model"
	)
	SetChannelAffinity(apiKeyID, 121, requestModel, 10, 100)
	SetChannelAffinity(apiKeyID, 122, requestModel, 20, 200)

	group := model.Group{
		ID:   122,
		Mode: model.GroupModeFailover,
		Items: []model.GroupItem{
			{ChannelID: 30, ModelName: "upstream-30", Priority: 1},
		},
	}
	iterator := NewIterator(group, apiKeyID, requestModel)
	if entry := GetChannelAffinity(apiKeyID, 122, requestModel); entry != nil {
		t.Fatalf("expected missing current-group affinity to be cleared, got %#v", entry)
	}
	if entry := GetChannelAffinity(apiKeyID, 121, requestModel); entry == nil || entry.ChannelID != 10 {
		t.Fatalf("expected other-group affinity to remain, got %#v", entry)
	}
	if iterator.Len() != 1 {
		t.Fatalf("expected normal fallback candidates after affinity cleanup, got %d", iterator.Len())
	}
}

func TestIteratorDetectsRemainingDifferentChannel(t *testing.T) {
	iterator := NewIterator(model.Group{
		Mode: model.GroupModeFailover,
		Items: []model.GroupItem{
			{ChannelID: 1, ModelName: "model-a", Priority: 1},
			{ChannelID: 1, ModelName: "model-b", Priority: 2},
			{ChannelID: 2, ModelName: "model-c", Priority: 3},
		},
	}, 1, "request-model")

	if !iterator.Next() {
		t.Fatal("expected first candidate")
	}
	if !iterator.HasRemainingDifferentChannel(1) {
		t.Fatal("expected a remaining channel-level fallback")
	}
	if iterator.HasRemainingDifferentChannelExcept(1, map[int]struct{}{2: {}}) {
		t.Fatal("did not expect a fallback after excluding the only different channel")
	}
	if !iterator.Next() {
		t.Fatal("expected second candidate")
	}
	if !iterator.HasRemainingDifferentChannel(1) {
		t.Fatal("expected channel 2 to remain")
	}
	if !iterator.Next() {
		t.Fatal("expected third candidate")
	}
	if iterator.HasRemainingDifferentChannel(2) {
		t.Fatal("did not expect another channel after the final candidate")
	}
}

func TestIteratorQualityPrecedesStickyPreference(t *testing.T) {
	Reset()
	group := model.Group{
		ID:   91,
		Mode: model.GroupModeFailover,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 1, ModelName: "degraded", Priority: 1},
			{ID: 2, ChannelID: 2, ModelName: "native", Priority: 2},
		},
	}
	SetChannelAffinity(1, 91, "routing-model", 1, 101)
	iterator := NewIteratorWithPreferenceAndQuality(group, 1, "routing-model", nil, func(item model.GroupItem) int {
		if item.ChannelID == 2 {
			return 0
		}
		return 2
	})
	if !iterator.Next() {
		t.Fatal("expected first candidate")
	}
	if got := iterator.Item().ChannelID; got != 2 {
		t.Fatalf("quality rank should precede affinity, got channel %d", got)
	}
}

func TestIteratorKeepsPreferredChannelOnlyOnce(t *testing.T) {
	Reset()
	SetChannelAffinity(9, 92, "routing-model", 2, 202)
	group := model.Group{
		ID:   92,
		Mode: model.GroupModeFailover,
		Items: []model.GroupItem{
			{ChannelID: 1, ModelName: "upstream-1", Priority: 1},
			{ChannelID: 2, ModelName: "upstream-2a", Priority: 2},
			{ChannelID: 2, ModelName: "upstream-2b", Priority: 3},
			{ChannelID: 3, ModelName: "upstream-3", Priority: 4},
		},
	}

	iterator := NewIterator(group, 9, "routing-model")
	if iterator.Len() != 3 {
		t.Fatalf("expected one duplicate preferred-channel mapping to be removed, got %d candidates", iterator.Len())
	}
	preferredCount := 0
	for iterator.Next() {
		if iterator.Item().ChannelID == 2 {
			preferredCount++
		}
	}
	if preferredCount != 1 {
		t.Fatalf("expected preferred channel once, got %d", preferredCount)
	}
}

func TestInvalidateCurrentPreferenceClearsChannelAffinity(t *testing.T) {
	Reset()
	const (
		apiKeyID     = 10
		groupID      = 101
		requestModel = "routing-model"
	)
	SetChannelAffinity(apiKeyID, groupID, requestModel, 2, 202)
	group := model.Group{
		ID:              groupID,
		Mode:            model.GroupModeFailover,
		SessionKeepTime: 60,
		Items: []model.GroupItem{
			{ChannelID: 1, ModelName: "upstream-1", Priority: 1},
			{ChannelID: 2, ModelName: "upstream-2", Priority: 2},
			{ChannelID: 3, ModelName: "upstream-3", Priority: 3},
		},
	}

	iterator := NewIterator(group, apiKeyID, requestModel)
	if !iterator.Next() || iterator.PreferenceSource() != PreferenceChannelAffinity {
		t.Fatal("expected channel affinity to be the current preference")
	}
	iterator.InvalidateCurrentPreference()

	if entry := GetChannelAffinity(apiKeyID, groupID, requestModel); entry != nil {
		t.Fatalf("expected channel affinity to be cleared, got %#v", entry)
	}
	if !iterator.Next() {
		t.Fatal("expected another candidate to remain available as normal fallback")
	}
	if source := iterator.PreferenceSource(); source != PreferenceNone {
		t.Fatalf("expected fallback candidate to have no preference marker, got %d", source)
	}
}

func TestResetStateByChannelClearsOnlyMatchingAffinity(t *testing.T) {
	Reset()
	SetChannelAffinity(1, 111, "gpt-4o", 10, 100)
	SetChannelAffinity(2, 222, "gpt-4o", 20, 200)
	SetChannelAffinity(3, 333, "claude-3-5", 10, 300)

	ResetStateByChannel(10)

	if entry := GetChannelAffinity(1, 111, "gpt-4o"); entry != nil {
		t.Fatalf("expected first matching affinity to be cleared, got %#v", entry)
	}
	if entry := GetChannelAffinity(3, 333, "claude-3-5"); entry != nil {
		t.Fatalf("expected second matching affinity to be cleared, got %#v", entry)
	}
	if entry := GetChannelAffinity(2, 222, "gpt-4o"); entry == nil || entry.ChannelID != 20 {
		t.Fatalf("expected unrelated affinity to remain, got %#v", entry)
	}
}

func TestResetStateByGroupClearsOnlyMatchingAffinity(t *testing.T) {
	Reset()
	SetChannelAffinity(1, 111, "gpt-4o", 10, 100)
	SetChannelAffinity(2, 111, "claude-3-5", 20, 200)
	SetChannelAffinity(3, 222, "gpt-4o", 30, 300)

	ResetStateByGroup(111)

	if entry := GetChannelAffinity(1, 111, "gpt-4o"); entry != nil {
		t.Fatalf("expected first matching group affinity to be cleared, got %#v", entry)
	}
	if entry := GetChannelAffinity(2, 111, "claude-3-5"); entry != nil {
		t.Fatalf("expected second matching group affinity to be cleared, got %#v", entry)
	}
	if entry := GetChannelAffinity(3, 222, "gpt-4o"); entry == nil || entry.ChannelID != 30 {
		t.Fatalf("expected unrelated group affinity to remain, got %#v", entry)
	}
}
