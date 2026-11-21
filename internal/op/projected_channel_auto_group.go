package op

import (
	"context"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func ProjectedChannelGlobalAutoGroupMode() model.AutoGroupType {
	if !db.LegacyRuntime() {
		return model.AutoGroupTypeNone
	}
	value, err := SettingGetString(model.SettingKeyProjectedChannelAutoGroupEnabled)
	if err != nil {
		return model.AutoGroupTypeNone
	}
	mode, ok := model.ParseAutoGroupSettingValue(value)
	if !ok {
		return model.AutoGroupTypeNone
	}
	return mode
}

func ProjectedChannelGlobalAutoGroupEnabled() bool {
	return ProjectedChannelGlobalAutoGroupMode() != model.AutoGroupTypeNone
}

func EffectiveProjectedChannelAutoGroup(channel model.Channel) model.AutoGroupType {
	if !supportedChannelType(channel.Type) {
		return model.AutoGroupTypeNone
	}
	if mode := ProjectedChannelGlobalAutoGroupMode(); mode != model.AutoGroupTypeNone {
		return mode
	}
	return channel.AutoGroup
}

func AutoGroupAllProjectedChannels(ctx context.Context) error {
	mode := ProjectedChannelGlobalAutoGroupMode()
	if mode == model.AutoGroupTypeNone {
		return nil
	}
	channels := channelCache.GetAll()
	if len(channels) == 0 {
		return nil
	}
	channelIDs := make([]int, 0, len(channels))
	for id := range channels {
		channelIDs = append(channelIDs, id)
	}
	bindingMap, err := SiteChannelBindingMapByChannelIDs(channelIDs, ctx)
	if err != nil {
		return err
	}
	for id, channel := range channels {
		if _, ok := bindingMap[id]; !ok {
			continue
		}
		if !supportedChannelType(channel.Type) {
			continue
		}
		ChannelAutoGroupWithMode(&channel, mode, ctx)
	}
	return nil
}
