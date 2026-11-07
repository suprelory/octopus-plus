package op

import (
	"context"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestGroupGetEnabledMapCachesCopiesAndInvalidatesOnChannelChange(t *testing.T) {
	const (
		groupName = "group-resolution-cache-test"
		channelID = -7001
	)
	defer groupMap.Del(groupName)
	defer enabledGroupCache.Del(groupName)
	defer channelCache.Del(channelID)

	groupMap.Set(groupName, model.Group{
		ID:   -7002,
		Name: groupName,
		Items: []model.GroupItem{{
			ID:        -7003,
			ChannelID: channelID,
			ModelName: "mapped-model",
		}},
	})
	setChannelCache(channelID, model.Channel{ID: channelID, Enabled: true})

	first, err := GroupGetEnabledMap(groupName, context.Background())
	if err != nil || len(first.Items) != 1 {
		t.Fatalf("expected one enabled item, group=%#v err=%v", first, err)
	}
	first.Items[0].ModelName = "mutated"
	second, err := GroupGetEnabledMap(groupName, context.Background())
	if err != nil || second.Items[0].ModelName != "mapped-model" {
		t.Fatalf("cache returned mutable state, group=%#v err=%v", second, err)
	}

	setChannelCache(channelID, model.Channel{ID: channelID, Enabled: false})
	third, err := GroupGetEnabledMap(groupName, context.Background())
	if err != nil || len(third.Items) != 0 {
		t.Fatalf("expected channel change to invalidate cached candidates, group=%#v err=%v", third, err)
	}
}
