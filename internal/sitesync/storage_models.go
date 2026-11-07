package sitesync

import (
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func preparePersistedSyncModels(accountID int, incoming []model.SiteModel, existingModelMap map[string]model.SiteModel, now time.Time) []model.SiteModel {
	prepared := make([]model.SiteModel, 0, len(incoming))
	for i := range incoming {
		item := incoming[i]
		item.SiteAccountID = accountID
		item.GroupKey = model.NormalizeSiteGroupKey(item.GroupKey)
		key := item.GroupKey + "\x00" + strings.TrimSpace(item.ModelName)
		if existing, ok := existingModelMap[key]; ok {
			item.ID = existing.ID
			item.Disabled = existing.Disabled
			applyPersistedRouteState(&item, &existing, now)
		} else {
			applyPersistedRouteState(&item, nil, now)
		}
		prepared = append(prepared, item)
	}
	return compactPersistedSiteModels(prepared)
}

func copyPersistedGroupSyncState(group *model.SiteUserGroup, existing model.SiteUserGroup) {
	group.ProjectionSuspended = existing.ProjectionSuspended
	group.ProjectionSuspendReason = existing.ProjectionSuspendReason
	group.ProjectionSuspendedAt = existing.ProjectionSuspendedAt
	group.ModelSyncStatus = existing.ModelSyncStatus
	group.ModelSyncMessage = existing.ModelSyncMessage
	group.ModelSyncAuthoritative = existing.ModelSyncAuthoritative
	group.ModelSyncModelCount = existing.ModelSyncModelCount
	group.LastModelSyncAt = existing.LastModelSyncAt
	group.LastModelSyncSuccessAt = existing.LastModelSyncSuccessAt
	group.ModelSyncFailureCount = existing.ModelSyncFailureCount
}

func applyPersistedGroupSyncState(group *model.SiteUserGroup, existing *model.SiteUserGroup, result siteGroupSyncResult, now time.Time) {
	if existing != nil {
		group.ModelSyncFailureCount = existing.ModelSyncFailureCount
		group.LastModelSyncSuccessAt = existing.LastModelSyncSuccessAt
	}
	group.ModelSyncStatus = modelSiteGroupSyncStatus(result.Status)
	group.ModelSyncMessage = sanitizeSiteStatusText(result.Message)
	group.ModelSyncAuthoritative = result.Authoritative
	group.ModelSyncModelCount = result.ModelCount
	group.LastModelSyncAt = &now

	switch result.Status {
	case siteGroupSyncStatusSynced:
		group.ProjectionSuspended = false
		group.ProjectionSuspendReason = ""
		group.ProjectionSuspendedAt = nil
		group.LastModelSyncSuccessAt = &now
		group.ModelSyncFailureCount = 0
	case siteGroupSyncStatusEmpty:
		group.ProjectionSuspended = true
		group.ProjectionSuspendReason = firstNonEmptyString(group.ModelSyncMessage, "上游当前无可用模型，已暂停投影")
		group.ProjectionSuspendedAt = &now
		group.ModelSyncFailureCount = 0
	case siteGroupSyncStatusRemoved:
		group.ProjectionSuspended = false
		group.ProjectionSuspendReason = ""
		group.ProjectionSuspendedAt = nil
		group.ModelSyncFailureCount = 0
	case siteGroupSyncStatusMissingKey:
		group.ProjectionSuspended = false
		group.ProjectionSuspendReason = ""
		group.ProjectionSuspendedAt = nil
		group.ModelSyncFailureCount++
	case siteGroupSyncStatusFailed, siteGroupSyncStatusUnresolved:
		group.ProjectionSuspended = false
		group.ProjectionSuspendReason = ""
		group.ProjectionSuspendedAt = nil
		group.ModelSyncFailureCount++
	default:
		if existing != nil {
			group.ProjectionSuspended = existing.ProjectionSuspended
			group.ProjectionSuspendReason = existing.ProjectionSuspendReason
			group.ProjectionSuspendedAt = existing.ProjectionSuspendedAt
		}
	}
}

func modelSiteGroupSyncStatus(status siteGroupSyncStatus) model.SiteGroupModelSyncStatus {
	switch status {
	case siteGroupSyncStatusSynced:
		return model.SiteGroupModelSyncStatusSynced
	case siteGroupSyncStatusEmpty:
		return model.SiteGroupModelSyncStatusEmpty
	case siteGroupSyncStatusFailed:
		return model.SiteGroupModelSyncStatusFailed
	case siteGroupSyncStatusUnresolved:
		return model.SiteGroupModelSyncStatusUnresolved
	case siteGroupSyncStatusMissingKey:
		return model.SiteGroupModelSyncStatusMissingKey
	case siteGroupSyncStatusRemoved:
		return model.SiteGroupModelSyncStatusRemoved
	default:
		return model.SiteGroupModelSyncStatusIdle
	}
}

func mergePersistedSiteModelsByGroup(existing []model.SiteModel, incoming []model.SiteModel, results []siteGroupSyncResult) []model.SiteModel {
	replaceGroups := make(map[string]struct{})
	for _, result := range results {
		switch result.Status {
		case siteGroupSyncStatusSynced, siteGroupSyncStatusEmpty, siteGroupSyncStatusRemoved:
			replaceGroups[model.NormalizeSiteGroupKey(result.GroupKey)] = struct{}{}
		}
	}

	merged := make([]model.SiteModel, 0, len(existing)+len(incoming))
	for _, item := range existing {
		groupKey := model.NormalizeSiteGroupKey(item.GroupKey)
		if _, ok := replaceGroups[groupKey]; ok {
			continue
		}
		item.GroupKey = groupKey
		item.ModelName = strings.TrimSpace(item.ModelName)
		if item.ModelName == "" {
			continue
		}
		merged = append(merged, item)
	}
	merged = append(merged, incoming...)
	return compactPersistedSiteModels(merged)
}

func compactPersistedSiteModels(items []model.SiteModel) []model.SiteModel {
	if len(items) <= 1 {
		return items
	}
	seen := make(map[string]int, len(items))
	result := make([]model.SiteModel, 0, len(items))
	for _, item := range items {
		groupKey := model.NormalizeSiteGroupKey(item.GroupKey)
		modelName := strings.TrimSpace(item.ModelName)
		if modelName == "" {
			continue
		}
		item.GroupKey = groupKey
		item.ModelName = modelName
		key := groupKey + "\x00" + modelName
		if index, ok := seen[key]; ok {
			// Keep the row with stronger persisted state if duplicates slip through.
			if result[index].ManualOverride || result[index].RouteSource == model.SiteModelRouteSourceRuntimeLearned {
				continue
			}
			if item.ManualOverride || item.RouteSource == model.SiteModelRouteSourceRuntimeLearned {
				result[index] = item
			}
			continue
		}
		seen[key] = len(result)
		result = append(result, item)
	}
	return result
}
