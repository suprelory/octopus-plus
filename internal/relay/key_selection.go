package relay

import (
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
)

// selectAndReserveRelayKey skips circuit-broken keys while preserving the
// reservation for the first usable key. The caller must invoke the returned
// release function when a non-empty key is returned.
func selectAndReserveRelayKey(iter *balancer.Iterator, channel *dbmodel.Channel, excludedKeyIDs map[int]struct{}) (dbmodel.ChannelKey, func()) {
	if excludedKeyIDs == nil {
		excludedKeyIDs = make(map[int]struct{})
	}
	selectOpts := dbmodel.ChannelKeySelectOptions{
		ExcludeKeyIDs:   excludedKeyIDs,
		PreferredKeyID:  iter.StickyKeyID(),
		InFlightPenalty: 1,
	}
	for {
		usedKey, releaseKey := balancer.SelectAndReserveChannelKey(channel, selectOpts)
		if usedKey.ChannelKey == "" {
			return usedKey, releaseKey
		}
		if !iter.SkipCircuitBreak(channel.ID, usedKey.ID, channel.Name) {
			return usedKey, releaseKey
		}
		releaseKey()
		excludedKeyIDs[usedKey.ID] = struct{}{}
	}
}
