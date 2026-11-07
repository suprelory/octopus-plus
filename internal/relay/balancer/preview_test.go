package balancer

import (
	"reflect"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestPreviewPreservesStrategyHealthAffinityAndCircuitState(t *testing.T) {
	for _, mode := range []model.GroupMode{model.GroupModeRoundRobin, model.GroupModeWeighted, model.GroupModeFailover} {
		t.Run(groupModeName(mode), func(t *testing.T) {
			Reset()
			t.Cleanup(Reset)
			group := model.Group{ID: 99, Mode: mode, Items: []model.GroupItem{{ID: 1, ChannelID: 10, ModelName: "m", Weight: 2}, {ID: 2, ChannelID: 20, ModelName: "m", Weight: 1}}}
			option := AffinityOptions{Mode: "prefer", Source: "header", Scope: "session"}
			live := NewIterator(group, 1, "m", option)
			if !live.Next() {
				t.Fatal("no live candidate")
			}
			live.Close()
			globalChannelHealth.record(10, "m", model.AttemptFailed, time.Second, "transient", time.Time{})
			healthBefore := *globalChannelHealth.entries[channelHealthKey{channelID: 10, modelName: "m"}]
			SetRoutingAffinity(1, group.ID, "m", 999, 11, option)
			cacheKey := affinityKey(1, group.ID, "m", option)
			affinityBefore, _ := channelAffinity.Load(cacheKey)
			RecordFailureAt(20, 21, "m", FailureRateLimit, time.Now().Add(-time.Second))
			breaker := getOrCreateEntry(circuitKey(20, 21, "m"))
			breaker.RetryAt = time.Now().Add(-time.Second)
			accessBefore := globalStrategyState.accessSeq
			var expected int
			for index := 0; index < 5; index++ {
				preview := NewPreviewIterator(group, 1, "m", nil, option)
				if !preview.Next() {
					t.Fatal("no preview candidate")
				}
				if index == 0 {
					expected = preview.Item().ChannelID
				} else if expected != preview.Item().ChannelID {
					t.Fatal("preview consumed strategy state")
				}
				for preview.Next() {
				}
				preview.Close()
			}
			if globalStrategyState.accessSeq != accessBefore {
				t.Fatal("preview updated scheduler LRU")
			}
			if !reflect.DeepEqual(healthBefore, *globalChannelHealth.entries[channelHealthKey{channelID: 10, modelName: "m"}]) {
				t.Fatal("preview changed channel health")
			}
			if value, ok := channelAffinity.Load(cacheKey); !ok || value != affinityBefore {
				t.Fatal("preview invalidated stale affinity")
			}
			if breaker.State != StateOpen {
				t.Fatal("preview claimed a half-open probe")
			}
			next := NewIterator(group, 1, "m", option)
			defer next.Close()
			if !next.Next() || next.Item().ChannelID != expected {
				t.Fatalf("preview predicted %d, got %+v", expected, next.Item())
			}
		})
	}
}

func TestPreviewAffinityKeepsCapabilityQualityRank(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	group := model.Group{ID: 99, Mode: model.GroupModeRoundRobin, Items: []model.GroupItem{
		{ID: 1, ChannelID: 10, ModelName: "m"},
		{ID: 2, ChannelID: 20, ModelName: "m"},
	}}
	option := AffinityOptions{Mode: "prefer", Source: "header", Scope: "session"}
	SetRoutingAffinity(1, group.ID, "m", 20, 21, option)
	quality := func(model.GroupItem) int { return 2 }
	preview := NewPreviewIterator(group, 1, "m", quality, option)
	defer preview.Close()
	if !preview.Next() || preview.Item().ChannelID != 20 || preview.SelectionReason() != "channel_affinity" || preview.QualityRank() != 2 {
		t.Fatalf("affinity lost quality rank: %+v", preview)
	}
	live := NewIteratorWithPreferenceAndQuality(group, 1, "m", nil, quality, option)
	defer live.Close()
	if !live.Next() || live.Item() != preview.Item() || live.QualityRank() != preview.QualityRank() {
		t.Fatal("live routing disagrees with affinity preview")
	}
}
