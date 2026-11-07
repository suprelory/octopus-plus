package op

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func SiteAvailableModels(siteID int, ctx context.Context) ([]string, error) {
	var rows []model.SiteModel
	if err := db.GetDB().WithContext(ctx).
		Joins("JOIN site_accounts ON site_accounts.id = site_models.site_account_id").
		Where("site_accounts.site_id = ? AND site_models.disabled = ?", siteID, false).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	models := make([]string, 0, len(rows))
	for _, row := range rows {
		trimmed := strings.TrimSpace(row.ModelName)
		if trimmed == "" {
			continue
		}
		metadata, hasMetadata := model.ParseSiteModelRouteMetadata(row.RouteRawPayload)
		if hasMetadata && !metadata.RouteSupported {
			continue
		}
		routeType := model.NormalizeSiteModelRouteType(row.RouteType)
		if !model.IsProjectedSiteModelRouteType(routeType) {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		models = append(models, trimmed)
	}
	sort.Strings(models)
	return models, nil
}

func SiteModelRouteUpdate(accountID int, groupKey string, modelName string, routeType model.SiteModelRouteType, source model.SiteModelRouteSource, manualOverride bool, routeRawPayload string, ctx context.Context) error {
	normalizedRouteType, err := validateSiteModelRouteType(routeType)
	if err != nil {
		return err
	}
	now := time.Now()
	updates := map[string]any{
		"route_type":        normalizedRouteType,
		"route_source":      model.NormalizeSiteModelRouteSource(source, manualOverride),
		"manual_override":   manualOverride,
		"route_raw_payload": strings.TrimSpace(routeRawPayload),
		"route_updated_at":  &now,
	}
	return db.GetDB().WithContext(ctx).
		Model(&model.SiteModel{}).
		Where("site_account_id = ? AND group_key = ? AND model_name = ?", accountID, model.NormalizeSiteGroupKey(groupKey), strings.TrimSpace(modelName)).
		Updates(updates).Error
}

func SiteModelRouteUpdateIfNotManual(accountID int, groupKey string, modelName string, routeType model.SiteModelRouteType, source model.SiteModelRouteSource, routeRawPayload string, ctx context.Context) (bool, error) {
	normalizedRouteType, err := validateSiteModelRouteType(routeType)
	if err != nil {
		return false, err
	}
	now := time.Now()
	updates := map[string]any{
		"route_type":        normalizedRouteType,
		"route_source":      model.NormalizeSiteModelRouteSource(source, false),
		"manual_override":   false,
		"route_raw_payload": strings.TrimSpace(routeRawPayload),
		"route_updated_at":  &now,
	}
	result := db.GetDB().WithContext(ctx).
		Model(&model.SiteModel{}).
		Where("site_account_id = ? AND group_key = ? AND model_name = ? AND manual_override = ?", accountID, model.NormalizeSiteGroupKey(groupKey), strings.TrimSpace(modelName), false).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func validateSiteModelRouteType(routeType model.SiteModelRouteType) (model.SiteModelRouteType, error) {
	normalized := model.NormalizeSiteModelRouteType(routeType)
	if !model.IsProjectedSiteModelRouteType(normalized) {
		return model.SiteModelRouteTypeUnknown, fmt.Errorf("unsupported site model route type: %s", routeType)
	}
	return normalized, nil
}

func SiteModelDisabledUpdate(accountID int, groupKey string, modelName string, disabled bool, ctx context.Context) error {
	return db.GetDB().WithContext(ctx).
		Model(&model.SiteModel{}).
		Where("site_account_id = ? AND group_key = ? AND model_name = ?", accountID, model.NormalizeSiteGroupKey(groupKey), strings.TrimSpace(modelName)).
		Update("disabled", disabled).Error
}
