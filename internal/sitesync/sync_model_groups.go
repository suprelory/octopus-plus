package sitesync

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
)

func pickModelTokensByGroup(tokens []model.SiteToken) []model.SiteToken {
	if len(tokens) == 0 {
		return nil
	}

	order := make([]string, 0, len(tokens))
	selected := make(map[string]model.SiteToken, len(tokens))
	for _, token := range tokens {
		groupKey := model.NormalizeSiteGroupKey(token.GroupKey)
		token.GroupKey = groupKey
		token.GroupName = model.NormalizeSiteGroupName(groupKey, token.GroupName)
		if _, ok := selected[groupKey]; !ok {
			order = append(order, groupKey)
			selected[groupKey] = token
			continue
		}
		if shouldPreferGroupModelToken(token, selected[groupKey]) {
			selected[groupKey] = token
		}
	}

	result := make([]model.SiteToken, 0, len(order))
	for _, groupKey := range order {
		token := selected[groupKey]
		if strings.TrimSpace(token.Token) == "" {
			continue
		}
		result = append(result, token)
	}
	return result
}

func shouldPreferGroupModelToken(candidate model.SiteToken, current model.SiteToken) bool {
	candidateToken := strings.TrimSpace(candidate.Token)
	currentToken := strings.TrimSpace(current.Token)
	if candidateToken == "" {
		return false
	}
	if currentToken == "" {
		return true
	}
	if candidate.Enabled != current.Enabled {
		return candidate.Enabled
	}
	return candidate.IsDefault && !current.IsDefault
}

func syncSiteModelsByGroup(
	ctx context.Context,
	siteRecord *model.Site,
	account *model.SiteAccount,
	accessToken string,
	groupTokens []model.SiteToken,
	platformUserID int,
	source string,
	fetcher func(token model.SiteToken, allowGlobalFallback bool) (siteModelFetchResult, error),
) ([]model.SiteModel, []siteGroupSyncResult) {
	if len(groupTokens) == 0 {
		return nil, nil
	}

	allowGlobalFallback := len(groupTokens) == 1
	models := make([]model.SiteModel, 0)
	results := make([]siteGroupSyncResult, 0, len(groupTokens))
	seen := make(map[string]struct{})

	for _, token := range groupTokens {
		result, err := fetcher(token, allowGlobalFallback)
		groupResult := siteGroupSyncResult{
			GroupKey:  model.NormalizeSiteGroupKey(token.GroupKey),
			GroupName: model.NormalizeSiteGroupName(token.GroupKey, token.GroupName),
			HasKey:    true,
		}
		if err != nil {
			groupResult.Status = siteGroupSyncStatusFailed
			groupResult.Message = sanitizeSiteStatusMessage(err)
			results = append(results, groupResult)
			continue
		}
		if result.authoritative && len(result.names) == 0 {
			groupResult.Status = siteGroupSyncStatusEmpty
			groupResult.Authoritative = true
			groupResult.Message = firstNonEmptyString(strings.TrimSpace(result.message), "上游当前没有可用模型，已清空该分组历史模型")
			results = append(results, groupResult)
			continue
		}
		if len(result.names) == 0 {
			groupResult.Status = siteGroupSyncStatusUnresolved
			groupResult.Message = firstNonEmptyString(strings.TrimSpace(result.message), "本次未能确认该分组模型，已沿用历史投影")
			results = append(results, groupResult)
			continue
		}

		groupSource := strings.TrimSpace(result.source)
		if groupSource == "" {
			groupSource = source
		}
		groupModels := buildSiteModels(result.names, token.GroupKey, groupSource)
		if len(result.detections) > 0 {
			groupModels = applyKnownRouteDetectionsToSiteModels(groupModels, result.detections)
		} else {
			groupModels = applyDetectedRoutesToSiteModels(ctx, siteRecord, account, accessToken, token, platformUserID, groupModels)
		}
		for _, item := range groupModels {
			key := model.NormalizeSiteGroupKey(item.GroupKey) + "\x00" + strings.TrimSpace(item.ModelName)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			models = append(models, item)
		}
		groupResult.Status = siteGroupSyncStatusSynced
		groupResult.Authoritative = result.authoritative || len(groupModels) > 0
		groupResult.ModelCount = len(groupModels)
		groupResult.Message = firstNonEmptyString(strings.TrimSpace(result.message), fmt.Sprintf("同步到 %d 个模型", len(groupModels)))
		results = append(results, groupResult)
	}

	sort.Slice(models, func(i, j int) bool {
		leftGroup := model.NormalizeSiteGroupKey(models[i].GroupKey)
		rightGroup := model.NormalizeSiteGroupKey(models[j].GroupKey)
		if leftGroup == rightGroup {
			return models[i].ModelName < models[j].ModelName
		}
		return leftGroup < rightGroup
	})
	return models, results
}

func expandExplicitGroupModelsToGroups(
	items []model.SiteModel,
	groups []model.SiteUserGroup,
	tokens []model.SiteToken,
) []model.SiteModel {
	if len(items) == 0 || len(groups) == 0 {
		return items
	}

	groupKeys := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		groupKey := model.NormalizeSiteGroupKey(group.GroupKey)
		groupKeys[groupKey] = struct{}{}
	}

	groupKeysWithTokens := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		groupKey := model.NormalizeSiteGroupKey(token.GroupKey)
		groupKeysWithTokens[groupKey] = struct{}{}
	}

	groupsWithoutTokens := make(map[string]struct{})
	for groupKey := range groupKeys {
		if _, ok := groupKeysWithTokens[groupKey]; ok {
			continue
		}
		groupsWithoutTokens[groupKey] = struct{}{}
	}
	if len(groupsWithoutTokens) == 0 {
		return items
	}

	expanded := make([]model.SiteModel, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		groupKey := model.NormalizeSiteGroupKey(item.GroupKey)
		modelName := strings.TrimSpace(item.ModelName)
		key := groupKey + "\x00" + modelName
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			expanded = append(expanded, item)
		}

		metadata, ok := model.ParseSiteModelRouteMetadata(item.RouteRawPayload)
		if !ok || len(metadata.EnableGroups) == 0 {
			continue
		}
		for _, explicitGroupKey := range metadata.EnableGroups {
			targetGroupKey := model.NormalizeSiteGroupKey(explicitGroupKey)
			if _, ok := groupsWithoutTokens[targetGroupKey]; !ok {
				continue
			}
			targetKey := targetGroupKey + "\x00" + modelName
			if _, ok := seen[targetKey]; ok {
				continue
			}
			copy := item
			copy.ID = 0
			copy.SiteAccountID = 0
			copy.GroupKey = targetGroupKey
			expanded = append(expanded, copy)
			seen[targetKey] = struct{}{}
		}
	}
	return expanded
}

func mergeSiteGroups(groups []model.SiteUserGroup, tokens []model.SiteToken) []model.SiteUserGroup {
	merged := make(map[string]model.SiteUserGroup)
	for _, item := range groups {
		key := model.NormalizeSiteGroupKey(item.GroupKey)
		item.GroupKey = key
		item.Name = model.NormalizeSiteGroupName(key, item.Name)
		merged[key] = item
	}
	for _, token := range tokens {
		key := model.NormalizeSiteGroupKey(token.GroupKey)
		if _, ok := merged[key]; ok {
			continue
		}
		merged[key] = model.SiteUserGroup{GroupKey: key, Name: model.NormalizeSiteGroupName(key, token.GroupName)}
	}
	if len(merged) == 0 {
		merged[model.SiteDefaultGroupKey] = model.SiteUserGroup{GroupKey: model.SiteDefaultGroupKey, Name: model.SiteDefaultGroupName}
	}
	result := make([]model.SiteUserGroup, 0, len(merged))
	for _, group := range merged {
		result = append(result, group)
	}
	return result
}
