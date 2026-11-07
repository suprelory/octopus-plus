package op

import (
	"context"
	"fmt"
	"sort"
	"strconv"
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

	channelIDs := make([]int, 0, len(channels))
	for _, channel := range channels {
		if !supportedChannelType(channel.Type) {
			continue
		}
		channelIDs = append(channelIDs, channel.ID)
	}
	bindingMap, err := SiteChannelBindingMapByChannelIDs(channelIDs, ctx)
	if err != nil {
		return nil, err
	}

	globalMode := ProjectedChannelGlobalAutoGroupMode()
	sources := make([]model.GroupAutoGroupSource, 0, len(channels))
	for _, channel := range channels {
		if !supportedChannelType(channel.Type) {
			continue
		}
		models := splitChannelModelNames(channel.Model, channel.CustomModel)
		binding, managed := bindingMap[channel.ID]
		effective := channel.AutoGroup
		globalOverride := managed && globalMode != model.AutoGroupTypeNone
		if globalOverride {
			effective = globalMode
		}

		source := model.GroupAutoGroupSource{
			ChannelID:          channel.ID,
			ChannelName:        channel.Name,
			Enabled:            channel.Enabled,
			Managed:            managed,
			AutoGroup:          channel.AutoGroup,
			EffectiveAutoGroup: effective,
			GlobalOverride:     globalOverride,
			ModelCount:         len(models),
			Models:             models,
			EndpointType:       channelEndpointType(channel.Type),
		}
		if managed {
			fillManagedAutoGroupSourceMetadata(&source, binding, ctx)
		}
		sources = append(sources, source)
	}

	sort.Slice(sources, func(i, j int) bool {
		left, right := autoGroupSourceSortName(sources[i]), autoGroupSourceSortName(sources[j])
		if left == right {
			return sources[i].ChannelID < sources[j].ChannelID
		}
		return left < right
	})

	return &model.GroupAutoGroupConfig{
		ProjectedGlobalAutoGroup: globalMode,
		Sources:                  sources,
	}, nil
}

func GroupAutoGroupConfigUpdate(req *model.GroupAutoGroupConfigUpdateRequest, ctx context.Context) (*model.GroupAutoGroupConfig, error) {
	if req == nil {
		return nil, newGroupAutoGroupBadRequestError("auto group config request is nil")
	}

	if req.ProjectedGlobalAutoGroup != nil {
		if !req.ProjectedGlobalAutoGroup.Valid() {
			return nil, newGroupAutoGroupBadRequestError("invalid projected global auto group type")
		}
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

	if req.ProjectedGlobalAutoGroup != nil {
		mode := *req.ProjectedGlobalAutoGroup
		if err := SettingSetString(model.SettingKeyProjectedChannelAutoGroupEnabled, strconv.Itoa(int(mode))); err != nil {
			return nil, err
		}
		// Preserve the old global-setting behavior: enabling a global projected-channel
		// mode immediately applies auto grouping to existing projected channels.
		if mode != model.AutoGroupTypeNone {
			if err := AutoGroupAllProjectedChannels(ctx); err != nil {
				return nil, err
			}
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

	targetIDs := make([]int, 0, len(targets))
	for id := range targets {
		targetIDs = append(targetIDs, id)
	}
	bindingMap, err := SiteChannelBindingMapByChannelIDs(targetIDs, ctx)
	if err != nil {
		return err
	}
	globalMode := ProjectedChannelGlobalAutoGroupMode()

	for id, channel := range targets {
		if !supportedChannelType(channel.Type) {
			continue
		}
		_, managed := bindingMap[id]
		mode := channel.AutoGroup
		if managed && globalMode != model.AutoGroupTypeNone {
			mode = globalMode
		}
		if mode == model.AutoGroupTypeNone {
			continue
		}
		ChannelAutoGroupWithMode(&channel, mode, ctx)
	}
	return nil
}

func fillManagedAutoGroupSourceMetadata(source *model.GroupAutoGroupSource, binding model.SiteChannelBinding, ctx context.Context) {
	if source == nil {
		return
	}
	siteID := binding.SiteID
	accountID := binding.SiteAccountID
	source.SiteID = &siteID
	source.SiteAccountID = &accountID
	baseGroupKey, routeType := model.ParseSiteChannelBindingKey(binding.GroupKey)
	source.SiteGroupKey = baseGroupKey
	if routeType != "" {
		source.EndpointType = string(routeType)
	}
	source.SiteGroupName = model.NormalizeSiteGroupName(baseGroupKey, "")

	site, err := SiteGet(binding.SiteID, ctx)
	if err != nil || site == nil {
		return
	}
	source.SiteName = site.Name
	for i := range site.Accounts {
		account := &site.Accounts[i]
		if account.ID != binding.SiteAccountID {
			continue
		}
		source.SiteAccountName = account.Name
		for _, group := range account.UserGroups {
			groupKey := model.NormalizeSiteGroupKey(group.GroupKey)
			if binding.SiteUserGroupID != nil && group.ID == *binding.SiteUserGroupID {
				source.SiteGroupKey = groupKey
				source.SiteGroupName = model.NormalizeSiteGroupName(groupKey, group.Name)
				return
			}
			if groupKey == baseGroupKey {
				source.SiteGroupName = model.NormalizeSiteGroupName(groupKey, group.Name)
			}
		}
		return
	}
}

func autoGroupSourceSortName(source model.GroupAutoGroupSource) string {
	parts := []string{source.ChannelName}
	if source.Managed {
		parts = []string{source.SiteName, source.SiteAccountName, source.SiteGroupName, source.EndpointType, source.ChannelName}
	}
	return strings.ToLower(strings.Join(parts, "/"))
}

func channelEndpointType(channelType outboundmodel.OutboundType) string {
	switch channelType {
	case outboundmodel.OutboundTypeOpenAIResponse:
		return string(model.SiteModelRouteTypeOpenAIResponse)
	case outboundmodel.OutboundTypeAnthropic:
		return string(model.SiteModelRouteTypeAnthropic)
	case outboundmodel.OutboundTypeGemini:
		return string(model.SiteModelRouteTypeGemini)
	case outboundmodel.OutboundTypeOpenAIEmbedding:
		return string(model.SiteModelRouteTypeOpenAIEmbedding)
	default:
		return string(model.SiteModelRouteTypeOpenAIChat)
	}
}
