package sitesync

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func buildManualSyncTokens(account *model.SiteAccount, sections manualSyncSections) []model.SiteToken {
	base := make([]model.SiteToken, 0, len(account.Tokens)+len(sections.tokens))
	if !sections.tokensProvided {
		base = append(base, cloneSiteTokens(account.Tokens)...)
	} else {
		for _, token := range account.Tokens {
			if strings.TrimSpace(token.Source) == "manual" {
				base = append(base, token)
			}
		}
	}
	for _, incoming := range sections.tokens {
		matched := -1
		for index, existing := range base {
			if sameManualTokenIdentity(existing, incoming) {
				matched = index
				break
			}
		}
		if matched >= 0 {
			base[matched] = incoming
		} else {
			base = append(base, incoming)
		}
	}
	return mergePersistedSiteTokens(account.ID, account.Tokens, base, time.Now())
}

func sameManualTokenIdentity(left model.SiteToken, right model.SiteToken) bool {
	if model.NormalizeSiteGroupKey(left.GroupKey) != model.NormalizeSiteGroupKey(right.GroupKey) {
		return false
	}
	if sameComparableSiteTokenValue(left.Token, right.Token) {
		return true
	}
	leftName := normalizeSiteTokenName(left.Name)
	rightName := normalizeSiteTokenName(right.Name)
	return leftName != "" && rightName != "" && leftName == rightName
}

func buildManualSyncModels(sections manualSyncSections) ([]model.SiteModel, []siteGroupSyncResult, map[string]struct{}) {
	groupKeys := make([]string, 0, len(sections.models))
	for groupKey := range sections.models {
		groupKeys = append(groupKeys, model.NormalizeSiteGroupKey(groupKey))
	}
	sort.Strings(groupKeys)

	affected := make([]model.SiteModel, 0)
	results := make([]siteGroupSyncResult, 0, len(groupKeys))
	explicit := make(map[string]struct{}, len(groupKeys))
	for _, groupKey := range groupKeys {
		explicit[groupKey] = struct{}{}
		incoming := sections.models[groupKey]
		desired := make([]model.SiteModel, 0, len(incoming))
		for _, item := range incoming {
			desired = upsertManualModel(desired, item)
		}
		sortSiteModels(desired)
		affected = append(affected, desired...)
		result := siteGroupSyncResult{
			GroupKey:      groupKey,
			Status:        siteGroupSyncStatusSynced,
			Authoritative: true,
			ModelCount:    len(desired),
			Message:       fmt.Sprintf("手动导入后确认 %d 个模型", len(desired)),
		}
		if len(desired) == 0 {
			result.Status = siteGroupSyncStatusEmpty
			result.Message = "手动导入确认该分组当前没有模型"
		}
		results = append(results, result)
	}
	return affected, results, explicit
}

func addManualTokenRecoveryGroups(
	account *model.SiteAccount,
	finalTokens []model.SiteToken,
	sections manualSyncSections,
	results []siteGroupSyncResult,
	affected []model.SiteModel,
	explicit map[string]struct{},
) ([]siteGroupSyncResult, []model.SiteModel) {
	if !sections.tokensProvided {
		return results, affected
	}
	touched := collectManualSyncTouchedGroups(account, sections, explicit)
	for groupKey := range touched {
		if _, ok := explicit[groupKey]; ok || !hasUsableToken(tokensForGroup(finalTokens, groupKey)) || !manualGroupNeedsTokenRecovery(account.UserGroups, groupKey) {
			continue
		}
		groupModels := modelsForGroup(account.Models, groupKey)
		if len(groupModels) == 0 {
			continue
		}
		affected = append(affected, cloneSiteModels(groupModels)...)
		results = append(results, siteGroupSyncResult{
			GroupKey:      groupKey,
			Status:        siteGroupSyncStatusSynced,
			Authoritative: true,
			ModelCount:    len(groupModels),
			Message:       fmt.Sprintf("手动导入可用 Key 后沿用 %d 个历史模型", len(groupModels)),
		})
	}
	return results, affected
}

func addManualMissingKeyGroups(
	account *model.SiteAccount,
	finalTokens []model.SiteToken,
	sections manualSyncSections,
	results []siteGroupSyncResult,
	explicit map[string]struct{},
) []siteGroupSyncResult {
	touched := collectManualSyncTouchedGroups(account, sections, explicit)
	if len(touched) == 0 {
		return results
	}

	resultIndexes := make(map[string]int, len(results))
	for index := range results {
		resultIndexes[model.NormalizeSiteGroupKey(results[index].GroupKey)] = index
	}
	for groupKey := range touched {
		groupKey = model.NormalizeSiteGroupKey(groupKey)
		if hasUsableToken(tokensForGroup(finalTokens, groupKey)) {
			continue
		}
		missingKeyResult := siteGroupSyncResult{
			GroupKey: groupKey,
			Status:   siteGroupSyncStatusMissingKey,
			Message:  "手动导入后该分组没有可用 Key，无法投影，已清理历史投影",
		}
		if index, ok := resultIndexes[groupKey]; ok {
			results[index] = missingKeyResult
			continue
		}
		resultIndexes[groupKey] = len(results)
		results = append(results, missingKeyResult)
	}
	return results
}

func collectManualSyncTouchedGroups(account *model.SiteAccount, sections manualSyncSections, explicit map[string]struct{}) map[string]struct{} {
	touched := make(map[string]struct{})
	for groupKey := range explicit {
		touched[model.NormalizeSiteGroupKey(groupKey)] = struct{}{}
	}
	if !sections.tokensProvided {
		return touched
	}
	if account != nil {
		for _, group := range account.UserGroups {
			touched[model.NormalizeSiteGroupKey(group.GroupKey)] = struct{}{}
		}
		for _, token := range account.Tokens {
			touched[model.NormalizeSiteGroupKey(token.GroupKey)] = struct{}{}
		}
		for _, item := range account.Models {
			touched[model.NormalizeSiteGroupKey(item.GroupKey)] = struct{}{}
		}
	}
	for _, token := range sections.tokens {
		touched[model.NormalizeSiteGroupKey(token.GroupKey)] = struct{}{}
	}
	return touched
}

func manualGroupNeedsTokenRecovery(groups []model.SiteUserGroup, groupKey string) bool {
	groupKey = model.NormalizeSiteGroupKey(groupKey)
	for _, group := range groups {
		if model.NormalizeSiteGroupKey(group.GroupKey) != groupKey {
			continue
		}
		return group.ProjectionSuspended || group.ModelSyncStatus == model.SiteGroupModelSyncStatusMissingKey
	}
	return false
}

func buildManualSyncGroups(account *model.SiteAccount, sections manualSyncSections, tokens []model.SiteToken, models []model.SiteModel, explicitModelGroups map[string]struct{}) []model.SiteUserGroup {
	groupMap := make(map[string]model.SiteUserGroup)
	if !sections.groupsProvided {
		for _, group := range account.UserGroups {
			groupKey := model.NormalizeSiteGroupKey(group.GroupKey)
			group.GroupKey = groupKey
			group.Name = model.NormalizeSiteGroupName(groupKey, group.Name)
			groupMap[groupKey] = group
		}
	}
	for _, incoming := range sections.groups {
		groupKey := model.NormalizeSiteGroupKey(incoming.GroupKey)
		incoming.GroupKey = groupKey
		incoming.Name = model.NormalizeSiteGroupName(groupKey, incoming.Name)
		if existing, ok := findSiteGroup(account.UserGroups, groupKey); ok {
			incoming.ID = existing.ID
			incoming.SiteAccountID = existing.SiteAccountID
		}
		groupMap[groupKey] = incoming
	}
	for _, token := range tokens {
		groupKey := model.NormalizeSiteGroupKey(token.GroupKey)
		if _, ok := groupMap[groupKey]; !ok {
			groupMap[groupKey] = model.SiteUserGroup{GroupKey: groupKey, Name: model.NormalizeSiteGroupName(groupKey, token.GroupName)}
		}
	}
	for _, item := range models {
		groupKey := model.NormalizeSiteGroupKey(item.GroupKey)
		if _, ok := groupMap[groupKey]; !ok {
			groupMap[groupKey] = model.SiteUserGroup{GroupKey: groupKey, Name: model.NormalizeSiteGroupName(groupKey, "")}
		}
	}
	for groupKey := range explicitModelGroups {
		if _, ok := groupMap[groupKey]; !ok {
			groupMap[groupKey] = model.SiteUserGroup{GroupKey: groupKey, Name: model.NormalizeSiteGroupName(groupKey, "")}
		}
	}
	if len(groupMap) == 0 {
		groupMap[model.SiteDefaultGroupKey] = model.SiteUserGroup{GroupKey: model.SiteDefaultGroupKey, Name: model.SiteDefaultGroupName}
	}
	groups := make([]model.SiteUserGroup, 0, len(groupMap))
	for _, group := range groupMap {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].GroupKey < groups[j].GroupKey })
	return groups
}

func tokensForGroup(tokens []model.SiteToken, groupKey string) []model.SiteToken {
	groupKey = model.NormalizeSiteGroupKey(groupKey)
	result := make([]model.SiteToken, 0)
	for _, token := range tokens {
		if model.NormalizeSiteGroupKey(token.GroupKey) == groupKey {
			result = append(result, token)
		}
	}
	return result
}

func modelsForGroup(models []model.SiteModel, groupKey string) []model.SiteModel {
	groupKey = model.NormalizeSiteGroupKey(groupKey)
	result := make([]model.SiteModel, 0)
	for _, item := range models {
		if model.NormalizeSiteGroupKey(item.GroupKey) == groupKey {
			result = append(result, item)
		}
	}
	return result
}

func findSiteGroup(groups []model.SiteUserGroup, groupKey string) (model.SiteUserGroup, bool) {
	groupKey = model.NormalizeSiteGroupKey(groupKey)
	for _, group := range groups {
		if model.NormalizeSiteGroupKey(group.GroupKey) == groupKey {
			return group, true
		}
	}
	return model.SiteUserGroup{}, false
}

func upsertManualGroup(groups []model.SiteUserGroup, incoming model.SiteUserGroup) []model.SiteUserGroup {
	groupKey := model.NormalizeSiteGroupKey(incoming.GroupKey)
	incoming.GroupKey = groupKey
	incoming.Name = model.NormalizeSiteGroupName(groupKey, incoming.Name)
	for index := range groups {
		if model.NormalizeSiteGroupKey(groups[index].GroupKey) == groupKey {
			groups[index] = incoming
			return groups
		}
	}
	return append(groups, incoming)
}

func upsertManualModel(models []model.SiteModel, incoming model.SiteModel) []model.SiteModel {
	incoming.GroupKey = model.NormalizeSiteGroupKey(incoming.GroupKey)
	incoming.ModelName = strings.TrimSpace(incoming.ModelName)
	for index := range models {
		if model.NormalizeSiteGroupKey(models[index].GroupKey) == incoming.GroupKey && strings.TrimSpace(models[index].ModelName) == incoming.ModelName {
			models[index] = incoming
			return models
		}
	}
	return append(models, incoming)
}

func sortSiteModels(models []model.SiteModel) {
	sort.Slice(models, func(i, j int) bool {
		leftGroup := model.NormalizeSiteGroupKey(models[i].GroupKey)
		rightGroup := model.NormalizeSiteGroupKey(models[j].GroupKey)
		if leftGroup != rightGroup {
			return leftGroup < rightGroup
		}
		return strings.TrimSpace(models[i].ModelName) < strings.TrimSpace(models[j].ModelName)
	})
}

func cloneSiteTokens(items []model.SiteToken) []model.SiteToken {
	return append([]model.SiteToken(nil), items...)
}

func cloneSiteGroups(items []model.SiteUserGroup) []model.SiteUserGroup {
	return append([]model.SiteUserGroup(nil), items...)
}

func cloneSiteModels(items []model.SiteModel) []model.SiteModel {
	return append([]model.SiteModel(nil), items...)
}
