package sitesync

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

func ProjectAccount(ctx context.Context, accountID int) ([]int, error) {
	siteRecord, account, err := loadSiteAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if siteRecord.IsCheckinOnly() {
		return nil, nil
	}

	if !siteRecord.Enabled || !account.Enabled {
		bindings, err := listChannelBindingsByAccount(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		channelIDs := make([]int, 0, len(bindings))
		for _, binding := range bindings {
			channelIDs = append(channelIDs, binding.ChannelID)
			if err := op.ChannelEnabledManaged(binding.ChannelID, false, ctx); err != nil {
				log.Warnf("failed to disable managed channel %d: %v", binding.ChannelID, err)
			}
		}
		return channelIDs, nil
	}

	groupMap := make(map[string]model.SiteUserGroup)
	for _, item := range account.UserGroups {
		key := model.NormalizeSiteGroupKey(item.GroupKey)
		item.GroupKey = key
		item.Name = model.NormalizeSiteGroupName(key, item.Name)
		groupMap[key] = item
	}
	if len(groupMap) == 0 {
		groupMap[model.SiteDefaultGroupKey] = model.SiteUserGroup{SiteAccountID: account.ID, GroupKey: model.SiteDefaultGroupKey, Name: model.SiteDefaultGroupName}
	}

	tokenGroups := make(map[string][]model.SiteToken)
	for _, token := range account.Tokens {
		groupKey := model.NormalizeSiteGroupKey(token.GroupKey)
		token.GroupKey = groupKey
		token.GroupName = model.NormalizeSiteGroupName(groupKey, token.GroupName)
		tokenGroups[groupKey] = append(tokenGroups[groupKey], token)
		if _, ok := groupMap[groupKey]; !ok {
			groupMap[groupKey] = model.SiteUserGroup{SiteAccountID: account.ID, GroupKey: groupKey, Name: model.NormalizeSiteGroupName(groupKey, token.GroupName)}
		}
	}

	modelsByGroup := make(map[string][]model.SiteModel)
	for _, item := range account.Models {
		name := strings.TrimSpace(item.ModelName)
		if name == "" {
			continue
		}
		if item.Disabled {
			continue
		}
		groupKey := model.NormalizeSiteGroupKey(item.GroupKey)
		group, ok := groupMap[groupKey]
		if !ok || !isSiteGroupProjectionActive(siteRecord, account, group, tokenGroups[groupKey]) {
			continue
		}
		item.GroupKey = groupKey
		item.ModelName = name
		if !siteModelBelongsToProjectedGroup(item, groupKey) {
			continue
		}
		routeType, projectable := projectableSiteModelRouteType(item)
		if !projectable {
			continue
		}
		item.RouteType = routeType
		modelsByGroup[groupKey] = append(modelsByGroup[groupKey], item)
	}
	for groupKey, items := range modelsByGroup {
		modelsByGroup[groupKey] = compactSiteModels(items)
	}
	if err := syncProjectedModelPrices(ctx, modelsByGroup); err != nil {
		log.Warnf("failed to sync projected model prices (account=%d): %v", account.ID, err)
	}

	existingBindings, err := listChannelBindingsByAccount(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	bindingMap := make(map[string]model.SiteChannelBinding, len(existingBindings))
	for _, binding := range existingBindings {
		bindingMap[model.NormalizeSiteGroupKey(binding.GroupKey)] = binding
	}

	desiredKeys := make([]string, 0, len(groupMap))
	for groupKey, group := range groupMap {
		if isSiteGroupProjectionActive(siteRecord, account, group, tokenGroups[groupKey]) {
			desiredKeys = append(desiredKeys, groupKey)
		}
	}
	slices.Sort(desiredKeys)

	managedChannelIDs := make([]int, 0, len(desiredKeys))
	shouldSplit := shouldSplitForAccount(account, siteRecord)
	bindingChannelByKey := make(map[string]int)

	for _, groupKey := range desiredKeys {
		group := groupMap[groupKey]
		groupTokens := tokenGroups[groupKey]
		groupModels := modelsByGroup[groupKey]
		modelBuckets := partitionSiteModelsByRouteType(groupModels, shouldSplit, siteRecord)
		proxyMode, proxyConfigID := resolveSiteAccountProxy(siteRecord, account)
		enabled := siteRecord.Enabled && account.Enabled && hasUsableToken(groupTokens)
		for routeType, bucketModels := range modelBuckets {
			if len(bucketModels) == 0 {
				continue
			}
			obType := routeType.ToOutboundType()
			baseUrls := []model.BaseUrl{{URL: resolveProjectedChannelBaseURL(siteRecord, routeType), Delay: 0}}
			modelNames := extractSiteModelNames(bucketModels)
			bindingKey := compositeBindingKey(groupKey, obType, shouldSplit)
			channelPayload := model.Channel{
				Name:          buildManagedChannelName(siteRecord, account, group, obType),
				Type:          obType,
				Enabled:       enabled,
				BaseUrls:      baseUrls,
				Keys:          buildChannelKeys(groupTokens, siteRecord.Platform),
				Model:         strings.Join(modelNames, ","),
				CustomModel:   "",
				ProxyMode:     proxyMode,
				ProxyConfigID: proxyConfigID,
				AutoSync:      false,
				AutoGroup:     model.AutoGroupTypeNone,
				CustomHeader:  siteRecord.CustomHeader,
			}

			binding, exists := bindingMap[bindingKey]
			if !exists {
				reusedBinding, reused, err := reuseManagedChannelByName(ctx, siteRecord, account, group, bindingKey, channelPayload)
				if err != nil {
					return nil, err
				}
				if reused {
					binding = *reusedBinding
					bindingMap[bindingKey] = binding
					exists = true
				}
			}
			if !exists {
				if err := op.ChannelCreate(&channelPayload, ctx); err != nil {
					return nil, fmt.Errorf("failed to create managed channel: %w", err)
				}
				binding = model.SiteChannelBinding{SiteID: siteRecord.ID, SiteAccountID: account.ID, GroupKey: bindingKey, ChannelID: channelPayload.ID}
				if group.ID != 0 {
					binding.SiteUserGroupID = &group.ID
				}
				if err := db.GetDB().WithContext(ctx).Create(&binding).Error; err != nil {
					return nil, fmt.Errorf("failed to create site channel binding: %w", err)
				}
				bindingMap[bindingKey] = binding
				bindingChannelByKey[bindingKey] = channelPayload.ID
				managedChannelIDs = append(managedChannelIDs, channelPayload.ID)
				if effective := op.EffectiveProjectedChannelAutoGroup(channelPayload); effective != model.AutoGroupTypeNone {
					op.ChannelAutoGroupWithMode(&channelPayload, effective, ctx)
				}
				continue
			}

			existingChannel, err := op.ChannelGet(binding.ChannelID, ctx)
			if err != nil {
				if err := db.GetDB().WithContext(ctx).Delete(&binding).Error; err != nil {
					return nil, fmt.Errorf("failed to delete broken site channel binding: %w", err)
				}
				if err := op.ChannelCreate(&channelPayload, ctx); err != nil {
					return nil, fmt.Errorf("failed to recreate managed channel: %w", err)
				}
				binding.ChannelID = channelPayload.ID
				if group.ID != 0 {
					binding.SiteUserGroupID = &group.ID
				} else {
					binding.SiteUserGroupID = nil
				}
				if err := db.GetDB().WithContext(ctx).Create(&binding).Error; err != nil {
					return nil, fmt.Errorf("failed to recreate site channel binding: %w", err)
				}
				bindingChannelByKey[bindingKey] = channelPayload.ID
				managedChannelIDs = append(managedChannelIDs, channelPayload.ID)
				if effective := op.EffectiveProjectedChannelAutoGroup(channelPayload); effective != model.AutoGroupTypeNone {
					op.ChannelAutoGroupWithMode(&channelPayload, effective, ctx)
				}
				continue
			}

			updateReq := &model.ChannelUpdateRequest{ID: existingChannel.ID, Name: &channelPayload.Name, Type: &channelPayload.Type, Enabled: &channelPayload.Enabled, BaseUrls: &channelPayload.BaseUrls, Model: &channelPayload.Model, CustomModel: &channelPayload.CustomModel, ProxyMode: &channelPayload.ProxyMode, ProxyConfigID: channelPayload.ProxyConfigID, AutoSync: &channelPayload.AutoSync, CustomHeader: &channelPayload.CustomHeader, BypassManagedCheck: true}
			updateReq.KeysToAdd, updateReq.KeysToUpdate, updateReq.KeysToDelete = diffManagedChannelKeys(existingChannel.Keys, channelPayload.Keys)
			if _, err := op.ChannelUpdate(updateReq, ctx); err != nil {
				return nil, fmt.Errorf("failed to update managed channel: %w", err)
			}
			updateBinding := map[string]any{"group_key": bindingKey}
			if group.ID != 0 {
				updateBinding["site_user_group_id"] = group.ID
			} else {
				updateBinding["site_user_group_id"] = nil
			}
			if err := db.GetDB().WithContext(ctx).Model(&model.SiteChannelBinding{}).Where("id = ?", binding.ID).Updates(updateBinding).Error; err != nil {
				return nil, fmt.Errorf("failed to update site channel binding: %w", err)
			}
			bindingChannelByKey[bindingKey] = existingChannel.ID
			managedChannelIDs = append(managedChannelIDs, existingChannel.ID)
			updatedChannel, err := op.ChannelGet(existingChannel.ID, ctx)
			if err != nil {
				return nil, err
			}
			if effective := op.EffectiveProjectedChannelAutoGroup(*updatedChannel); effective != model.AutoGroupTypeNone {
				op.ChannelAutoGroupWithMode(updatedChannel, effective, ctx)
			}
		}
	}

	desiredSet := make(map[string]struct{})
	for _, groupKey := range desiredKeys {
		modelBuckets := partitionSiteModelsByRouteType(modelsByGroup[groupKey], shouldSplit, siteRecord)
		for routeType, bucketModels := range modelBuckets {
			if len(bucketModels) == 0 {
				continue
			}
			obType := routeType.ToOutboundType()
			desiredSet[compositeBindingKey(groupKey, obType, shouldSplit)] = struct{}{}
		}
	}
	if err := rewriteManagedGroupItemsForAccount(ctx, siteRecord, account, shouldSplit, groupMap, tokenGroups, account.Models, bindingChannelByKey); err != nil {
		return nil, err
	}
	for _, binding := range existingBindings {
		bindingKey := model.NormalizeSiteGroupKey(binding.GroupKey)
		if _, ok := desiredSet[bindingKey]; ok {
			continue
		}
		baseGroupKey, _ := parseCompositeBindingKey(bindingKey)
		if group, ok := groupMap[baseGroupKey]; ok && shouldPreserveSystemPausedProjection(group) {
			if err := updateSiteChannelBindingGroup(ctx, binding.ID, group); err != nil {
				return nil, err
			}
			if err := op.ChannelEnabledManaged(binding.ChannelID, false, ctx); err != nil {
				log.Warnf("failed to disable system-paused managed channel %d: %v", binding.ChannelID, err)
			}
			continue
		}
		if err := op.ChannelDelManaged(binding.ChannelID, ctx); err != nil {
			log.Warnf("failed to delete stale managed channel %d: %v", binding.ChannelID, err)
		}
		if err := db.GetDB().WithContext(ctx).Delete(&binding).Error; err != nil {
			return nil, fmt.Errorf("failed to delete stale site channel binding: %w", err)
		}
	}

	return managedChannelIDs, nil
}

func isSiteGroupProjectionActive(siteRecord *model.Site, account *model.SiteAccount, group model.SiteUserGroup, tokens []model.SiteToken) bool {
	if siteRecord == nil || account == nil {
		return false
	}
	if !siteRecord.Enabled || !account.Enabled {
		return false
	}
	if !hasUsableToken(tokens) {
		return false
	}
	if group.ProjectionDisabled || group.ProjectionSuspended {
		return false
	}
	switch group.ModelSyncStatus {
	case "", model.SiteGroupModelSyncStatusIdle,
		model.SiteGroupModelSyncStatusSynced,
		model.SiteGroupModelSyncStatusStale,
		model.SiteGroupModelSyncStatusFailed,
		model.SiteGroupModelSyncStatusUnresolved:
		return true
	default:
		return false
	}
}

func shouldPreserveSystemPausedProjection(group model.SiteUserGroup) bool {
	if group.ProjectionDisabled {
		return false
	}
	// Missing keys and authoritative empty model results clear projected channels,
	// even when the group still carries projection_suspended=true.
	switch group.ModelSyncStatus {
	case model.SiteGroupModelSyncStatusMissingKey,
		model.SiteGroupModelSyncStatusEmpty:
		return false
	}
	return group.ProjectionSuspended
}

func updateSiteChannelBindingGroup(ctx context.Context, bindingID int, group model.SiteUserGroup) error {
	updates := map[string]any{}
	if group.ID != 0 {
		updates["site_user_group_id"] = group.ID
	} else {
		updates["site_user_group_id"] = nil
	}
	if err := db.GetDB().WithContext(ctx).Model(&model.SiteChannelBinding{}).Where("id = ?", bindingID).Updates(updates).Error; err != nil {
		return fmt.Errorf("failed to update paused site channel binding: %w", err)
	}
	return nil
}

func ProjectSite(ctx context.Context, siteID int) error {
	siteRecord, err := op.SiteGet(siteID, ctx)
	if err != nil {
		return err
	}
	for _, account := range siteRecord.Accounts {
		if _, err := ProjectAccount(ctx, account.ID); err != nil {
			return err
		}
	}
	return nil
}
