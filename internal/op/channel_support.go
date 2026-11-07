package op

import (
	"fmt"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

// validateChannelReference validates channels before creating route bindings.
func validateChannelReference(channelID int) (model.Channel, error) {
	if channelID <= 0 {
		return model.Channel{}, fmt.Errorf("channel id is required")
	}
	channel, ok := channelCache.Get(channelID)
	if !ok {
		return model.Channel{}, fmt.Errorf("channel not found")
	}
	if _, ok := outbound.Descriptor(channel.Type); !ok {
		return model.Channel{}, fmt.Errorf("unsupported channel type: %d", channel.Type)
	}
	return channel, nil
}

func supportedChannelType(channelType outbound.OutboundType) bool {
	_, ok := outbound.Descriptor(channelType)
	return ok
}
