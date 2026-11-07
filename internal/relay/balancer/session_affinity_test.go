package balancer

import (
	"github.com/suprelory/octopus-plus/internal/model"
	"testing"
	"time"
)

func TestStrictAffinityPreservesBindingAndReplayPreference(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	group := model.Group{ID: 321, Mode: model.GroupModeFailover, Items: []model.GroupItem{{ID: 1, ChannelID: 10, ModelName: "m"}, {ID: 2, ChannelID: 20, ModelName: "m"}}}
	strict := AffinityOptions{Mode: "strict", Scope: "session-a", Source: "header"}
	SetRoutingAffinity(1, group.ID, "m", 20, 21, strict)
	it := NewIterator(group, 1, "m", strict)
	if !it.Next() || it.Item().ChannelID != 20 {
		t.Fatal("strict binding ignored")
	}
	it.InvalidateCurrentPreference()
	if it.Next() {
		t.Fatal("strict binding migrated to another channel")
	}
	if GetChannelAffinity(1, group.ID, "m", strict) == nil {
		t.Fatal("failed strict binding was deleted")
	}
	group.Items = group.Items[:1]
	if missing := NewIterator(group, 1, "m", strict); missing.Len() != 0 {
		t.Fatal("removed strict channel must not silently migrate")
	}
	replay := NewIteratorWithPreferenceAndQuality(group, 1, "m", &SessionEntry{ChannelID: 10, ChannelKeyID: 11}, nil, strict)
	if !replay.Next() || replay.Item().ChannelID != 10 || replay.PreferenceSource() != PreferenceResponsesReplay {
		t.Fatal("ordinary affinity overrode replay recovery")
	}
}

func TestPreferredSessionCanMigrateButOffDoesNotRefresh(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	group := model.Group{ID: 322, Mode: model.GroupModeFailover, Items: []model.GroupItem{{ID: 1, ChannelID: 10, ModelName: "m"}, {ID: 2, ChannelID: 20, ModelName: "m"}}}
	option := AffinityOptions{Mode: "prefer", Scope: "session", Source: "session_id"}
	SetRoutingAffinity(1, group.ID, "m", 20, 21, option)
	globalChannelHealth.record(20, "m", model.AttemptFailed, time.Second, "rate_limit", time.Now().Add(time.Minute), "channel")
	it := NewIterator(group, 1, "m", option)
	if !it.Next() || it.Item().ChannelID != 10 {
		t.Fatal("cooling channel overrode normal routing")
	}
	it.RecordAffinity(10, 11)
	if entry := GetChannelAffinity(1, group.ID, "m", option); entry == nil || entry.ChannelID != 10 {
		t.Fatalf("migration=%+v", entry)
	}
	off := option
	off.Mode = "off"
	NewIterator(group, 1, "m", off).RecordAffinity(20, 21)
	if GetChannelAffinity(1, group.ID, "m", option).ChannelID != 10 {
		t.Fatal("disabled affinity updated the binding")
	}
}
