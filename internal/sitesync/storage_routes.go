package sitesync

import (
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func inferSiteModelRouteType(item model.SiteModel) model.SiteModelRouteType {
	return model.InferSiteModelRouteType(item.ModelName)
}

func applyPersistedRouteState(item *model.SiteModel, existing *model.SiteModel, now time.Time) {
	if item == nil {
		return
	}

	if canPreservePersistedRouteState(existing) {
		item.RouteType = model.NormalizeSiteModelRouteType(existing.RouteType)
		item.RouteSource = model.NormalizeSiteModelRouteSource(existing.RouteSource, existing.ManualOverride)
		item.ManualOverride = existing.ManualOverride
		item.RouteRawPayload = existing.RouteRawPayload
		item.RouteUpdatedAt = existing.RouteUpdatedAt
		return
	}

	if routeType, routeRawPayload, explicit := resolveExplicitSyncRoute(item, existing); explicit {
		item.RouteType = routeType
		item.RouteSource = model.SiteModelRouteSourceSyncInferred
		item.ManualOverride = false
		item.RouteRawPayload = routeRawPayload
		if existing != nil &&
			model.NormalizeSiteModelRouteType(existing.RouteType) == routeType &&
			strings.TrimSpace(existing.RouteRawPayload) == strings.TrimSpace(routeRawPayload) &&
			!existing.ManualOverride &&
			existing.RouteSource == model.SiteModelRouteSourceSyncInferred {
			item.RouteUpdatedAt = existing.RouteUpdatedAt
			return
		}
		item.RouteUpdatedAt = &now
		return
	}

	item.RouteType = inferSiteModelRouteType(*item)
	item.RouteSource = model.SiteModelRouteSourceSyncInferred
	item.ManualOverride = false
	item.RouteRawPayload = ""
	if existing != nil &&
		model.NormalizeSiteModelRouteType(existing.RouteType) == item.RouteType &&
		strings.TrimSpace(existing.RouteRawPayload) == "" &&
		!existing.ManualOverride &&
		existing.RouteSource == model.SiteModelRouteSourceSyncInferred {
		item.RouteUpdatedAt = existing.RouteUpdatedAt
		return
	}
	item.RouteUpdatedAt = &now
}

func canPreservePersistedRouteState(existing *model.SiteModel) bool {
	if existing == nil || (!existing.ManualOverride && existing.RouteSource != model.SiteModelRouteSourceRuntimeLearned) {
		return false
	}
	routeType := model.NormalizeSiteModelRouteType(existing.RouteType)
	if !model.IsProjectedSiteModelRouteType(routeType) {
		return false
	}

	metadata, hasMetadata := model.ParseSiteModelRouteMetadata(existing.RouteRawPayload)
	if hasMetadata {
		if !metadata.RouteSupported || !model.IsProjectedSiteModelRouteType(metadata.RouteType) {
			return false
		}
	}
	return true
}

func resolveExplicitSyncRoute(item *model.SiteModel, existing *model.SiteModel) (model.SiteModelRouteType, string, bool) {
	if item != nil {
		if metadata, ok := model.ParseSiteModelRouteMetadata(item.RouteRawPayload); ok {
			return metadata.RouteType, item.RouteRawPayload, true
		}
		if strings.TrimSpace(string(item.RouteType)) != "" {
			routeType := model.NormalizeSiteModelRouteType(item.RouteType)
			return routeType, strings.TrimSpace(item.RouteRawPayload), true
		}
	}
	if existing != nil {
		if metadata, ok := model.ParseSiteModelRouteMetadata(existing.RouteRawPayload); ok {
			return metadata.RouteType, existing.RouteRawPayload, true
		}
	}
	return "", "", false
}
