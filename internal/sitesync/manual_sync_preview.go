package sitesync

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func buildManualSyncPreviewGroups(
	siteRecord *model.Site,
	account *model.SiteAccount,
	groups []model.SiteUserGroup,
	tokens []model.SiteToken,
	models []model.SiteModel,
	explicitModelGroups map[string]struct{},
	results []siteGroupSyncResult,
) ([]ManualSyncPreviewGroup, int) {
	preparedGroups := cloneSiteGroups(groups)
	existingMap := make(map[string]model.SiteUserGroup, len(account.UserGroups))
	for _, group := range account.UserGroups {
		existingMap[model.NormalizeSiteGroupKey(group.GroupKey)] = group
	}
	resultMap := make(map[string]siteGroupSyncResult, len(results))
	for _, result := range results {
		resultMap[model.NormalizeSiteGroupKey(result.GroupKey)] = result
	}
	now := time.Now()
	for index := range preparedGroups {
		groupKey := model.NormalizeSiteGroupKey(preparedGroups[index].GroupKey)
		preparedGroups[index].GroupKey = groupKey
		var existing *model.SiteUserGroup
		if item, ok := existingMap[groupKey]; ok {
			copy := item
			existing = &copy
			preparedGroups[index].ProjectionDisabled = item.ProjectionDisabled
		}
		if result, ok := resultMap[groupKey]; ok {
			applyPersistedGroupSyncState(&preparedGroups[index], existing, result, now)
		} else if existing != nil {
			copyPersistedGroupSyncState(&preparedGroups[index], *existing)
		}
	}

	accountCopy := *account
	accountCopy.Tokens = tokens
	accountCopy.UserGroups = preparedGroups
	accountCopy.Models = models
	split := shouldSplitForAccount(&accountCopy, siteRecord)
	preview := make([]ManualSyncPreviewGroup, 0, len(preparedGroups))
	channelCount := 0
	for _, group := range preparedGroups {
		groupKey := model.NormalizeSiteGroupKey(group.GroupKey)
		groupTokens := tokensForGroup(tokens, groupKey)
		groupModels := modelsForGroup(models, groupKey)
		enabledModels := make([]model.SiteModel, 0, len(groupModels))
		routeSet := make(map[string]struct{})
		for _, item := range groupModels {
			routeType := item.RouteType
			if strings.TrimSpace(string(routeType)) == "" {
				routeType = model.InferSiteModelRouteType(item.ModelName)
			} else {
				routeType = model.NormalizeSiteModelRouteType(routeType)
			}
			item.RouteType = routeType
			routeSet[string(routeType)] = struct{}{}
			if !item.Disabled {
				enabledModels = append(enabledModels, item)
			}
		}
		routeTypes := make([]string, 0, len(routeSet))
		for routeType := range routeSet {
			routeTypes = append(routeTypes, routeType)
		}
		sort.Strings(routeTypes)
		willProject := isSiteGroupProjectionActive(siteRecord, &accountCopy, group, groupTokens) && len(enabledModels) > 0
		if willProject {
			channelCount += len(partitionSiteModelsByRouteType(enabledModels, split, siteRecord))
		}
		usable, masked := countManualTokens(groupTokens)
		action := "preserve"
		if _, ok := explicitModelGroups[groupKey]; ok {
			action = ManualSyncModeReplace
		}
		preview = append(preview, ManualSyncPreviewGroup{
			GroupKey:         groupKey,
			GroupName:        model.NormalizeSiteGroupName(groupKey, group.Name),
			TokenCount:       len(groupTokens),
			UsableTokenCount: usable,
			MaskedTokenCount: masked,
			ModelCount:       len(groupModels),
			ModelAction:      action,
			RouteTypes:       routeTypes,
			WillProject:      willProject,
		})
	}
	return preview, channelCount
}

func buildManualSyncWarnings(account *model.SiteAccount, sections manualSyncSections, finalTokens []model.SiteToken, finalModels []model.SiteModel, explicitModelGroups map[string]struct{}) []string {
	warnings := make([]string, 0)
	for _, incoming := range sections.tokens {
		if !model.IsMaskedSiteTokenValue(incoming.Token) {
			continue
		}
		resolved := false
		for _, finalToken := range finalTokens {
			if sameManualTokenIdentity(incoming, finalToken) && model.IsReadySiteToken(finalToken) && !model.IsMaskedSiteTokenValue(finalToken.Token) {
				resolved = true
				break
			}
		}
		if !resolved {
			warnings = append(warnings, fmt.Sprintf("分组 %q 的 Key %q 仅包含脱敏值，导入后不会启用对应渠道", model.NormalizeSiteGroupKey(incoming.GroupKey), incoming.Name))
		}
	}
	for groupKey := range explicitModelGroups {
		if len(modelsForGroup(finalModels, groupKey)) == 0 {
			continue
		}
		if !hasUsableToken(tokensForGroup(finalTokens, groupKey)) {
			warnings = append(warnings, fmt.Sprintf("分组 %q 有模型但没有可用完整 Key，不会投影，并会清理历史投影", groupKey))
		}
	}
	if sections.tokensProvided {
		manualCount := 0
		for _, token := range account.Tokens {
			if strings.TrimSpace(token.Source) == "manual" {
				manualCount++
			}
		}
		if manualCount > 0 {
			warnings = append(warnings, fmt.Sprintf("已保留 %d 个手工维护的 Key；替换模式只清理非手工 Key", manualCount))
		}
	}
	return warnings
}

func buildManualSyncMessage(sections manualSyncSections, results []siteGroupSyncResult) string {
	parts := make([]string, 0, 4)
	if sections.tokensProvided {
		parts = append(parts, fmt.Sprintf("解析 %d 个 Key", len(sections.tokens)))
	}
	if sections.groupsProvided {
		parts = append(parts, fmt.Sprintf("解析 %d 个分组", len(sections.groups)))
	}
	if len(sections.models) > 0 {
		parts = append(parts, fmt.Sprintf("处理 %d 个模型分组", len(sections.models)))
	}
	if sections.balance != nil || sections.balanceUsed != nil || sections.todayIncome != nil {
		parts = append(parts, "更新账户额度")
	}
	missingKeyCount := 0
	recoveredCount := 0
	for _, result := range results {
		switch result.Status {
		case siteGroupSyncStatusMissingKey:
			missingKeyCount++
		case siteGroupSyncStatusSynced:
			recoveredCount++
		}
	}
	if recoveredCount > 0 && len(sections.models) == 0 {
		parts = append(parts, "恢复历史模型投影")
	}
	if missingKeyCount > 0 {
		parts = append(parts, fmt.Sprintf("清理 %d 个缺少可用 Key 的分组历史投影", missingKeyCount))
	}
	if len(parts) == 0 {
		return "手动导入完成"
	}
	return "手动导入完成：" + strings.Join(parts, "，")
}

func countManualImportedModels(models map[string][]model.SiteModel) int {
	count := 0
	for _, items := range models {
		count += len(items)
	}
	return count
}

func countManualTokens(tokens []model.SiteToken) (int, int) {
	usable := 0
	masked := 0
	for _, token := range tokens {
		if model.IsMaskedSiteTokenValue(token.Token) || model.NormalizeSiteTokenValueStatus(token.ValueStatus, token.Token) == model.SiteTokenValueStatusMaskedPending {
			masked++
		}
		if isUsableSiteToken(token) {
			usable++
		}
	}
	return usable, masked
}

func normalizeManualWarnings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}
