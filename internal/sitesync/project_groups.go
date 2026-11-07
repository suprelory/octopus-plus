package sitesync

import (
	"context"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func rewriteManagedGroupItemsForAccount(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, split bool, groupMap map[string]model.SiteUserGroup, tokenGroups map[string][]model.SiteToken, accountModels []model.SiteModel, bindingChannelByKey map[string]int) error {
	if account == nil {
		return nil
	}
	accountID := account.ID
	var bindings []model.SiteChannelBinding
	if err := db.GetDB().WithContext(ctx).Where("site_account_id = ?", accountID).Find(&bindings).Error; err != nil {
		return fmt.Errorf("failed to list bindings for group rewrite: %w", err)
	}
	if len(bindings) == 0 {
		return nil
	}
	channelIDs := make([]int, 0, len(bindings))
	for _, binding := range bindings {
		channelIDs = append(channelIDs, binding.ChannelID)
	}
	var items []model.GroupItem
	if err := db.GetDB().WithContext(ctx).Where("channel_id IN ?", channelIDs).Find(&items).Error; err != nil {
		return fmt.Errorf("failed to list group items for rewrite: %w", err)
	}
	if len(items) == 0 {
		return nil
	}
	modelRouteMap := make(map[string]model.SiteModelRouteType)
	activeModelKeys := make(map[string]struct{})
	for _, item := range accountModels {
		if item.Disabled {
			continue
		}
		baseGroupKey := model.NormalizeSiteGroupKey(item.GroupKey)
		group, ok := groupMap[baseGroupKey]
		if !ok || !isSiteGroupProjectionActive(siteRecord, account, group, tokenGroups[baseGroupKey]) {
			continue
		}
		if !siteModelBelongsToProjectedGroup(item, baseGroupKey) {
			continue
		}
		item.GroupKey = baseGroupKey
		item.ModelName = strings.TrimSpace(item.ModelName)
		if item.ModelName == "" {
			continue
		}
		key := model.NormalizeSiteGroupKey(item.GroupKey) + "\x00" + strings.TrimSpace(item.ModelName)
		activeModelKeys[key] = struct{}{}
		routeType, projectable := projectableSiteModelRouteType(item)
		if !projectable {
			continue
		}
		if !split {
			routeType = model.SiteModelRouteTypeFromOutboundType(platformOutboundType(siteRecord))
		}
		if model.IsProjectedSiteModelRouteType(routeType) {
			modelRouteMap[key] = routeType
		}
	}
	affectedGroupIDs := make(map[int]struct{})
	deleteItemIDs := make([]int, 0)
	for _, item := range items {
		var binding *model.SiteChannelBinding
		for i := range bindings {
			if bindings[i].ChannelID == item.ChannelID {
				binding = &bindings[i]
				break
			}
		}
		if binding == nil {
			continue
		}
		baseGroupKey, _ := parseCompositeBindingKey(binding.GroupKey)
		if group, ok := groupMap[baseGroupKey]; ok && shouldPreserveSystemPausedProjection(group) {
			continue
		}
		modelKey := baseGroupKey + "\x00" + strings.TrimSpace(item.ModelName)
		if _, ok := activeModelKeys[modelKey]; !ok {
			deleteItemIDs = append(deleteItemIDs, item.ID)
			affectedGroupIDs[item.GroupID] = struct{}{}
			continue
		}
		routeType, ok := modelRouteMap[modelKey]
		if !ok {
			deleteItemIDs = append(deleteItemIDs, item.ID)
			affectedGroupIDs[item.GroupID] = struct{}{}
			continue
		}
		targetBindingKey := compositeBindingKey(baseGroupKey, routeType.ToOutboundType(), split)
		targetChannelID, ok := bindingChannelByKey[targetBindingKey]
		if !ok {
			deleteItemIDs = append(deleteItemIDs, item.ID)
			affectedGroupIDs[item.GroupID] = struct{}{}
			continue
		}
		if targetChannelID == item.ChannelID {
			continue
		}
		if err := db.GetDB().WithContext(ctx).Model(&model.GroupItem{}).Where("id = ?", item.ID).Update("channel_id", targetChannelID).Error; err != nil {
			return fmt.Errorf("failed to rewrite group item %d: %w", item.ID, err)
		}
		affectedGroupIDs[item.GroupID] = struct{}{}
	}
	if len(deleteItemIDs) > 0 {
		if err := db.GetDB().WithContext(ctx).Where("id IN ?", deleteItemIDs).Delete(&model.GroupItem{}).Error; err != nil {
			return fmt.Errorf("failed to delete stale group items: %w", err)
		}
	}
	if len(affectedGroupIDs) == 0 {
		return nil
	}
	groupIDs := make([]int, 0, len(affectedGroupIDs))
	for id := range affectedGroupIDs {
		groupIDs = append(groupIDs, id)
	}
	if err := op.GroupRefreshCacheByIDs(groupIDs, ctx); err != nil {
		return fmt.Errorf("failed to refresh group cache after rewrite: %w", err)
	}
	return nil
}

// shouldSplitForAccount 决定是否为账号启用渠道拆分。
// 当检测到账号内有多种手动覆盖的 RouteType 时，自动启用拆分，
// 使得不同端点格式的模型可以分配到不同的投影渠道。
func shouldSplitForAccount(account *model.SiteAccount, site *model.Site) bool {
	// 防御性检查：确保不会因 nil 输入而 panic
	if site == nil || account == nil {
		return false
	}

	// 优先级 1: 站点配置了协议路径覆盖，强制拆分
	if len(site.RouteBaseURLs) > 0 {
		return true
	}

	// 优先级 2: 平台默认策略
	if model.ShouldSplitSiteChannelRoutes(site.Platform) {
		return true
	}

	// 优先级 3: 检测手动覆盖是否与平台默认类型不同
	// 只要有任何手动覆盖与默认不同，或有多种手动覆盖类型，就启用拆分
	siteDefaultRoute := model.SiteModelRouteTypeFromOutboundType(platformOutboundType(site))
	routeTypes := make(map[model.SiteModelRouteType]struct{})
	for _, m := range account.Models {
		if m.Disabled {
			continue // 跳过禁用的模型
		}
		if !m.ManualOverride {
			continue // 跳过自动推断的模型
		}
		rt := model.NormalizeSiteModelRouteType(m.RouteType)
		if !model.IsProjectedSiteModelRouteType(rt) {
			continue // 跳过非投影类型
		}
		// 如果手动覆盖与平台默认不同，需要拆分
		if rt != siteDefaultRoute {
			return true
		}
		routeTypes[rt] = struct{}{}
		if len(routeTypes) > 1 {
			return true // 检测到混合类型，提前返回
		}
	}

	// 所有手动覆盖都与平台默认相同，不需要拆分
	return false
}
