package relay

import (
	"testing"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
)

func TestSelectAndReserveRelayKeyHonorsExclusionsAndReleases(t *testing.T) {
	balancer.Reset()
	t.Cleanup(balancer.Reset)

	group := dbmodel.Group{
		ID:   1,
		Mode: dbmodel.GroupModeRoundRobin,
		Items: []dbmodel.GroupItem{{
			ChannelID: 10,
			ModelName: "mapped-model",
		}},
	}
	iter := balancer.NewIterator(group, 1, "request-model")
	defer iter.Close()
	if !iter.Next() {
		t.Fatal("expected a relay candidate")
	}

	channel := &dbmodel.Channel{
		ID:   10,
		Name: "primary",
		Keys: []dbmodel.ChannelKey{
			{ID: 100, Enabled: true, ChannelKey: "excluded", TotalCost: 0},
			{ID: 101, Enabled: true, ChannelKey: "selected", TotalCost: 1},
		},
	}
	excludedKeyIDs := map[int]struct{}{100: {}}
	usedKey, releaseKey := selectAndReserveRelayKey(iter, channel, excludedKeyIDs)
	if usedKey.ID != 101 {
		t.Fatalf("selected key = %d, want 101", usedKey.ID)
	}
	if got := balancer.InFlightKeyCount(usedKey.ID); got != 1 {
		t.Fatalf("in-flight reservation count = %d, want 1", got)
	}
	releaseKey()
	if got := balancer.InFlightKeyCount(usedKey.ID); got != 0 {
		t.Fatalf("in-flight reservation count after release = %d, want 0", got)
	}
}
