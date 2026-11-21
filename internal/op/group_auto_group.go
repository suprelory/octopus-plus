package op

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	outboundmodel "github.com/suprelory/octopus-plus/polywire/outbound"
)

func GroupAutoGroupConfigGet(ctx context.Context) (*model.GroupAutoGroupConfig, error) {
	channels, err := ChannelList(ctx)
	if err != nil {
		return nil, err
	}

	sources := make([]model.GroupAutoGroupSource, 0, len(channels))
	for _, channel := range channels {
		if !supportedChannelType(channel.Type) {
			continue
		}
		models := splitChannelModelNames(channel.Model, channel.CustomModel)
		source := model.GroupAutoGroupSource{
			ChannelID:          channel.ID,
			ChannelName:        channel.Name,
			Enabled:            channel.Enabled,
			AutoGroup:          channel.AutoGroup,
			EffectiveAutoGroup: channel.AutoGroup,
			ModelCount:         len(models),
			Models:             models,
			EndpointType:       channelEndpointType(channel.Type),
		}
		sources = append(sources, source)
	}

	sort.Slice(sources, func(i, j int) bool {
		left, right := strings.ToLower(sources[i].ChannelName), strings.ToLower(sources[j].ChannelName)
		if left == right {
			return sources[i].ChannelID < sources[j].ChannelID
		}
		return left < right
	})

	return &model.GroupAutoGroupConfig{Sources: sources}, nil
}

func GroupAutoGroupConfigUpdate(req *model.GroupAutoGroupConfigUpdateRequest, ctx context.Context) (*model.GroupAutoGroupConfig, error) {
	if req == nil {
		return nil, newGroupAutoGroupBadRequestError("auto group config request is nil")
	}

	if req.ProjectedGlobalAutoGroup != nil {
		return nil, newGroupAutoGroupBadRequestError("projected channels are no longer supported")
	}
	seen := make(map[int]struct{}, len(req.Items))
	for _, item := range req.Items {
		if item.ChannelID <= 0 {
			return nil, newGroupAutoGroupBadRequestError("channel id is required")
		}
		if _, ok := seen[item.ChannelID]; ok {
			return nil, newGroupAutoGroupBadRequestError(fmt.Sprintf("duplicate channel: %d", item.ChannelID))
		}
		seen[item.ChannelID] = struct{}{}
		channel, ok := channelCache.Get(item.ChannelID)
		if !ok {
			return nil, newGroupAutoGroupNotFoundError(fmt.Sprintf("channel not found: %d", item.ChannelID))
		}
		if !supportedChannelType(channel.Type) {
			return nil, newGroupAutoGroupBadRequestError(fmt.Sprintf("unsupported channel type: %d", channel.Type))
		}
		if item.AutoGroup == nil {
			continue
		}
		if !item.AutoGroup.Valid() {
			return nil, newGroupAutoGroupBadRequestError("invalid auto group type")
		}
	}

	for _, item := range req.Items {
		if item.AutoGroup == nil {
			continue
		}
		if err := ChannelAutoGroupUpdate(item.ChannelID, *item.AutoGroup, ctx); err != nil {
			return nil, err
		}
	}

	if req.RunNow {
		if err := RunGroupAutoGroup(nil, ctx); err != nil {
			return nil, err
		}
	}

	return GroupAutoGroupConfigGet(ctx)
}

func ChannelAutoGroupUpdate(channelID int, mode model.AutoGroupType, ctx context.Context) error {
	if channelID <= 0 {
		return newGroupAutoGroupBadRequestError("channel id is required")
	}
	if !mode.Valid() {
		return newGroupAutoGroupBadRequestError("invalid auto group type")
	}
	channel, err := validateChannelReference(channelID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return newGroupAutoGroupNotFoundError(err.Error())
		}
		return newGroupAutoGroupBadRequestError(err.Error())
	}
	if err := db.GetDB().WithContext(ctx).Model(&model.Channel{}).Where("id = ?", channel.ID).Update("auto_group", mode).Error; err != nil {
		return err
	}
	return channelRefreshCacheByID(channelID, ctx)
}

func RunGroupAutoGroup(channelIDs []int, ctx context.Context) error {
	allChannels := channelCache.GetAll()
	if len(allChannels) == 0 {
		return nil
	}

	targets := make(map[int]model.Channel)
	if len(channelIDs) == 0 {
		for id, channel := range allChannels {
			targets[id] = channel
		}
	} else {
		for _, id := range channelIDs {
			if _, ok := targets[id]; ok {
				continue
			}
			channel, ok := allChannels[id]
			if !ok {
				return newGroupAutoGroupNotFoundError(fmt.Sprintf("channel not found: %d", id))
			}
			if !supportedChannelType(channel.Type) {
				return newGroupAutoGroupBadRequestError(fmt.Sprintf("unsupported channel type: %d", channel.Type))
			}
			targets[id] = channel
		}
	}

	for _, channel := range targets {
		if !supportedChannelType(channel.Type) {
			continue
		}
		if channel.AutoGroup == model.AutoGroupTypeNone {
			continue
		}
		ChannelAutoGroup(&channel, ctx)
	}
	return nil
}

func channelEndpointType(channelType outboundmodel.OutboundType) string {
	switch channelType {
	case outboundmodel.OutboundTypeOpenAIResponse:
		return "openai_response"
	case outboundmodel.OutboundTypeAnthropic:
		return "anthropic"
	case outboundmodel.OutboundTypeGemini:
		return "gemini"
	case outboundmodel.OutboundTypeOpenAIEmbedding:
		return "openai_embedding"
	default:
		return "openai_chat"
	}
}
