package op

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
)

type SiteChannelListOptions struct {
	IncludeHistory bool
}

func SiteChannelListWithOptions(ctx context.Context, opts SiteChannelListOptions) ([]model.SiteChannelCard, error) {
	sites, err := SiteList(ctx)
	if err != nil {
		return nil, err
	}
	// Check-in sites are never synced, so they have no channels to show.
	sites = slices.DeleteFunc(sites, func(site model.Site) bool { return site.IsCheckinOnly() })
	histories := map[int]map[string]*model.SiteModelHistorySummary{}
	if opts.IncludeHistory {
		accountIDs := make([]int, 0)
		for _, site := range sites {
			for _, account := range site.Accounts {
				accountIDs = append(accountIDs, account.ID)
			}
		}
		histories, err = SiteChannelModelHourlyForAccounts(ctx, accountIDs)
		if err != nil {
			return nil, err
		}
	}
	cards := make([]model.SiteChannelCard, 0, len(sites))
	for _, site := range sites {
		card, err := buildSiteChannelCardWithHistories(ctx, site, histories)
		if err != nil {
			return nil, err
		}
		cards = append(cards, card)
	}
	return cards, nil
}

func SiteChannelGet(siteID int, ctx context.Context) (*model.SiteChannelCard, error) {
	site, err := SiteGet(siteID, ctx)
	if err != nil {
		return nil, err
	}
	card, err := buildSiteChannelCard(ctx, *site)
	if err != nil {
		return nil, err
	}
	return &card, nil
}

func SiteChannelAccountGet(siteID int, accountID int, ctx context.Context) (*model.SiteChannelAccount, error) {
	site, err := SiteGet(siteID, ctx)
	if err != nil {
		return nil, err
	}
	var target *model.SiteAccount
	for i := range site.Accounts {
		if site.Accounts[i].ID == accountID {
			target = &site.Accounts[i]
			break
		}
	}
	if target == nil {
		return nil, newSiteChannelAccountNotFoundError()
	}
	historyMap, _ := SiteChannelModelHourlyForAccount(ctx, target.ID)
	view := model.SiteChannelAccount{
		SiteID:      site.ID,
		AccountID:   target.ID,
		AccountName: target.Name,
		Enabled:     target.Enabled,
		AutoSync:    target.AutoSync,
		Groups:      buildSiteChannelGroups(ctx, *site, *target, historyMap),
	}
	view.GroupCount = len(view.Groups)
	view.ModelCount = countSiteChannelModels(view.Groups)
	view.RouteSummaries = summarizeSiteRoutes(view.Groups)
	return &view, nil
}

func SiteChannelModelHistory(siteID int, accountID int, ctx context.Context) (map[string]*model.SiteModelHistorySummary, error) {
	site, err := SiteGet(siteID, ctx)
	if err != nil {
		return nil, err
	}
	for _, account := range site.Accounts {
		if account.ID == accountID {
			return SiteChannelModelHourlyForAccount(ctx, account.ID)
		}
	}
	return nil, newSiteChannelAccountNotFoundError()
}

func buildSiteChannelCard(ctx context.Context, site model.Site) (model.SiteChannelCard, error) {
	accountIDs := make([]int, 0, len(site.Accounts))
	for _, account := range site.Accounts {
		accountIDs = append(accountIDs, account.ID)
	}
	histories, err := SiteChannelModelHourlyForAccounts(ctx, accountIDs)
	if err != nil {
		return model.SiteChannelCard{}, err
	}
	return buildSiteChannelCardWithHistories(ctx, site, histories)
}

func buildSiteChannelCardWithHistories(ctx context.Context, site model.Site, histories map[int]map[string]*model.SiteModelHistorySummary) (model.SiteChannelCard, error) {
	card := model.SiteChannelCard{
		SiteID:       site.ID,
		SiteName:     site.Name,
		BaseURL:      site.BaseURL,
		Platform:     site.Platform,
		Enabled:      site.Enabled,
		AccountCount: len(site.Accounts),
		Accounts:     make([]model.SiteChannelAccount, 0, len(site.Accounts)),
	}
	for _, account := range site.Accounts {
		history := histories[account.ID]
		if history == nil {
			history = map[string]*model.SiteModelHistorySummary{}
		}
		view := model.SiteChannelAccount{
			SiteID:      site.ID,
			AccountID:   account.ID,
			AccountName: account.Name,
			Enabled:     account.Enabled,
			AutoSync:    account.AutoSync,
			Groups:      buildSiteChannelGroups(ctx, site, account, history),
		}
		view.GroupCount = len(view.Groups)
		view.ModelCount = countSiteChannelModels(view.Groups)
		view.RouteSummaries = summarizeSiteRoutes(view.Groups)
		card.Accounts = append(card.Accounts, view)
	}
	return card, nil
}

func buildSiteChannelGroups(ctx context.Context, site model.Site, account model.SiteAccount, historyMap map[string]*model.SiteModelHistorySummary) []model.SiteChannelGroup {
	split := model.ShouldSplitSiteChannelRoutes(site.Platform)
	groups := make(map[string]*model.SiteChannelGroup)
	projectedChannels := make(map[int]*model.Channel)
	for _, group := range account.UserGroups {
		key := model.NormalizeSiteGroupKey(group.GroupKey)
		groups[key] = newSiteChannelGroupView(key, model.NormalizeSiteGroupName(key, group.Name), group)
	}
	for _, token := range account.Tokens {
		key := model.NormalizeSiteGroupKey(token.GroupKey)
		group := ensureSiteChannelGroup(groups, key, token.GroupName)
		group.KeyCount++
		if model.NormalizeSiteTokenValueStatus(token.ValueStatus, token.Token) == model.SiteTokenValueStatusMaskedPending {
			group.MaskedPendingKeyCount++
		}
		if token.Enabled && model.IsReadySiteToken(token) && !model.IsMaskedSiteTokenValue(token.Token) {
			group.EnabledKeyCount++
		}
		var lastSyncAt *int64
		if token.LastSyncAt != nil && !token.LastSyncAt.IsZero() {
			unix := token.LastSyncAt.UnixMilli()
			lastSyncAt = &unix
		}
		group.SourceKeys = append(group.SourceKeys, model.SiteSourceKey{
			ID:          token.ID,
			Enabled:     token.Enabled,
			Token:       token.Token,
			TokenMasked: maskProjectedChannelKey(token.Token),
			Name:        token.Name,
			GroupKey:    key,
			GroupName:   model.NormalizeSiteGroupName(key, token.GroupName),
			ValueStatus: model.NormalizeSiteTokenValueStatus(token.ValueStatus, token.Token),
			LastSyncAt:  lastSyncAt,
		})
	}
	for _, binding := range account.ChannelBindings {
		baseKey, _ := model.ParseSiteChannelBindingKey(binding.GroupKey)
		group := ensureSiteChannelGroup(groups, baseKey, baseKey)
		channel, err := ChannelGet(binding.ChannelID, ctx)
		if err != nil || !supportedChannelType(channel.Type) {
			continue
		}
		group.HasProjectedChannel = true
		group.ProjectedChannelIDs = append(group.ProjectedChannelIDs, binding.ChannelID)
		if _, ok := projectedChannels[binding.ChannelID]; ok {
			continue
		}
		projectedChannels[binding.ChannelID] = channel
		routeType := model.SiteModelRouteTypeOpenAIChat
		if _, parsed := model.ParseSiteChannelBindingKey(binding.GroupKey); parsed != "" {
			routeType = parsed
		}
		paramOverride := ""
		if channel.ParamOverride != nil {
			paramOverride = *channel.ParamOverride
		}
		globalAutoGroup := ProjectedChannelGlobalAutoGroupEnabled()
		group.ProjectedChannels = append(group.ProjectedChannels, model.SiteProjectedChannelSettings{
			ChannelID:      channel.ID,
			ChannelName:    channel.Name,
			RouteType:      routeType,
			AutoGroup:      channel.AutoGroup,
			EffectiveGroup: EffectiveProjectedChannelAutoGroup(*channel),
			ParamOverride:  paramOverride,
			GlobalOverride: globalAutoGroup,
		})
		for _, key := range channel.Keys {
			group.ProjectedKeys = append(group.ProjectedKeys, model.SiteProjectedKey{
				ID:               key.ID,
				ChannelID:        channel.ID,
				ChannelName:      channel.Name,
				Enabled:          key.Enabled,
				ChannelKey:       key.ChannelKey,
				ChannelKeyMasked: maskProjectedChannelKey(key.ChannelKey),
				Remark:           key.Remark,
				StatusCode:       key.StatusCode,
				LastUseTimeStamp: key.LastUseTimeStamp,
				TotalCost:        key.TotalCost,
			})
		}
	}
	for _, item := range account.Models {
		key := model.NormalizeSiteGroupKey(item.GroupKey)
		if !siteModelBelongsToGroup(item, key) {
			continue
		}
		group := ensureSiteChannelGroup(groups, key, key)
		routeMetadata, _ := model.ParseSiteModelRouteMetadata(item.RouteRawPayload)
		channelID, hasChannel := findProjectedChannelID(account.ChannelBindings, key, item.RouteType, split)
		modelView := model.SiteChannelModel{
			ModelName:      item.ModelName,
			Source:         item.Source,
			RouteType:      model.NormalizeSiteModelRouteType(item.RouteType),
			RouteSource:    model.NormalizeSiteModelRouteSource(item.RouteSource, item.ManualOverride),
			ManualOverride: item.ManualOverride,
			Disabled:       item.Disabled,
			RouteMetadata:  routeMetadata,
			History:        historyMap[key+"\x00"+item.ModelName],
		}
		if hasChannel {
			id := channelID
			modelView.ProjectedChannelID = &id
		}
		group.Models = append(group.Models, modelView)
	}
	result := make([]model.SiteChannelGroup, 0, len(groups))
	for _, item := range groups {
		item.HasKeys = item.KeyCount > 0
		sort.Slice(item.ProjectedChannelIDs, func(i, j int) bool { return item.ProjectedChannelIDs[i] < item.ProjectedChannelIDs[j] })
		sort.Slice(item.ProjectedChannels, func(i, j int) bool { return item.ProjectedChannels[i].ChannelID < item.ProjectedChannels[j].ChannelID })
		sort.Slice(item.SourceKeys, func(i, j int) bool {
			if item.SourceKeys[i].Name == item.SourceKeys[j].Name {
				return item.SourceKeys[i].ID < item.SourceKeys[j].ID
			}
			return item.SourceKeys[i].Name < item.SourceKeys[j].Name
		})
		sort.Slice(item.ProjectedKeys, func(i, j int) bool {
			if item.ProjectedKeys[i].ChannelID == item.ProjectedKeys[j].ChannelID {
				return item.ProjectedKeys[i].ID < item.ProjectedKeys[j].ID
			}
			return item.ProjectedKeys[i].ChannelID < item.ProjectedKeys[j].ChannelID
		})
		sort.Slice(item.Models, func(i, j int) bool { return item.Models[i].ModelName < item.Models[j].ModelName })
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GroupKey < result[j].GroupKey })
	return result
}

func siteModelBelongsToGroup(item model.SiteModel, groupKey string) bool {
	metadata, ok := model.ParseSiteModelRouteMetadata(item.RouteRawPayload)
	if !ok || len(metadata.EnableGroups) == 0 {
		return true
	}
	targetGroupKey := model.NormalizeSiteGroupKey(groupKey)
	for _, explicitGroupKey := range metadata.EnableGroups {
		if model.NormalizeSiteGroupKey(explicitGroupKey) == targetGroupKey {
			return true
		}
	}
	return false
}

func maskProjectedChannelKey(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 8 {
		return trimmed
	}
	return trimmed[:4] + "..." + trimmed[len(trimmed)-4:]
}

func ensureSiteChannelGroup(groups map[string]*model.SiteChannelGroup, groupKey string, groupName string) *model.SiteChannelGroup {
	groupKey = model.NormalizeSiteGroupKey(groupKey)
	if item, ok := groups[groupKey]; ok {
		if strings.TrimSpace(item.GroupName) == "" {
			item.GroupName = model.NormalizeSiteGroupName(groupKey, groupName)
		}
		return item
	}
	item := newSiteChannelGroupView(groupKey, model.NormalizeSiteGroupName(groupKey, groupName), model.SiteUserGroup{})
	groups[groupKey] = item
	return item
}

func newSiteChannelGroupView(groupKey string, groupName string, group model.SiteUserGroup) *model.SiteChannelGroup {
	var projectionSuspendedAt *int64
	if group.ProjectionSuspendedAt != nil && !group.ProjectionSuspendedAt.IsZero() {
		unix := group.ProjectionSuspendedAt.UnixMilli()
		projectionSuspendedAt = &unix
	}
	var lastModelSyncAt *int64
	if group.LastModelSyncAt != nil && !group.LastModelSyncAt.IsZero() {
		unix := group.LastModelSyncAt.UnixMilli()
		lastModelSyncAt = &unix
	}
	var lastModelSyncSuccessAt *int64
	if group.LastModelSyncSuccessAt != nil && !group.LastModelSyncSuccessAt.IsZero() {
		unix := group.LastModelSyncSuccessAt.UnixMilli()
		lastModelSyncSuccessAt = &unix
	}
	status := group.ModelSyncStatus
	if status == "" {
		status = model.SiteGroupModelSyncStatusIdle
	}
	return &model.SiteChannelGroup{
		GroupKey:                groupKey,
		GroupName:               groupName,
		ProjectionDisabled:      group.ProjectionDisabled,
		ProjectionSuspended:     group.ProjectionSuspended,
		ProjectionSuspendReason: group.ProjectionSuspendReason,
		ProjectionSuspendedAt:   projectionSuspendedAt,
		ModelSyncStatus:         status,
		ModelSyncMessage:        group.ModelSyncMessage,
		ModelSyncAuthoritative:  group.ModelSyncAuthoritative,
		ModelSyncModelCount:     group.ModelSyncModelCount,
		LastModelSyncAt:         lastModelSyncAt,
		LastModelSyncSuccessAt:  lastModelSyncSuccessAt,
		ModelSyncFailureCount:   group.ModelSyncFailureCount,
		ProjectedChannelIDs:     make([]int, 0),
		ProjectedChannels:       make([]model.SiteProjectedChannelSettings, 0),
		SourceKeys:              make([]model.SiteSourceKey, 0),
		ProjectedKeys:           make([]model.SiteProjectedKey, 0),
		Models:                  make([]model.SiteChannelModel, 0),
	}
}

func countSiteChannelModels(groups []model.SiteChannelGroup) int {
	total := 0
	for _, group := range groups {
		total += len(group.Models)
	}
	return total
}

func summarizeSiteRoutes(groups []model.SiteChannelGroup) []model.SiteRouteSummary {
	counts := make(map[model.SiteModelRouteType]int)
	for _, group := range groups {
		for _, item := range group.Models {
			counts[item.RouteType]++
		}
	}
	result := make([]model.SiteRouteSummary, 0, len(counts))
	for routeType, count := range counts {
		result = append(result, model.SiteRouteSummary{RouteType: routeType, Count: count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RouteType < result[j].RouteType })
	return result
}

func findProjectedChannelID(bindings []model.SiteChannelBinding, groupKey string, routeType model.SiteModelRouteType, split bool) (int, bool) {
	if !model.IsProjectedSiteModelRouteType(routeType) {
		return 0, false
	}
	targetKey := model.ComposeSiteChannelBindingKey(groupKey, routeType, split)
	for _, binding := range bindings {
		if model.NormalizeSiteGroupKey(binding.GroupKey) == targetKey {
			return binding.ChannelID, true
		}
	}
	if split {
		fallbackKey := model.NormalizeSiteGroupKey(groupKey)
		for _, binding := range bindings {
			if model.NormalizeSiteGroupKey(binding.GroupKey) == fallbackKey {
				return binding.ChannelID, true
			}
		}
	}
	return 0, false
}
