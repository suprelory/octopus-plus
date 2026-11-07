package op

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func SiteChannelResetAccountRoutes(siteID int, accountID int, ctx context.Context) error {
	return db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []model.SiteModel
		if err := tx.Joins("JOIN site_accounts ON site_accounts.id = site_models.site_account_id").
			Where("site_accounts.site_id = ? AND site_models.site_account_id = ?", siteID, accountID).
			Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			routeType := model.InferSiteModelRouteType(row.ModelName)
			routeRawPayload := ""
			metadata, hasMetadata := model.ParseSiteModelRouteMetadata(row.RouteRawPayload)
			if hasMetadata {
				routeType = metadata.RouteType
				routeRawPayload = row.RouteRawPayload
			}
			if err := tx.Model(&model.SiteModel{}).Where("id = ?", row.ID).Updates(map[string]any{
				"route_type":        routeType,
				"route_source":      model.SiteModelRouteSourceSyncInferred,
				"manual_override":   false,
				"route_raw_payload": routeRawPayload,
				"route_updated_at":  gorm.Expr("CURRENT_TIMESTAMP"),
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func SiteManualModelsAdd(siteID int, accountID int, req *model.SiteManualModelAddRequest, ctx context.Context) error {
	if req == nil {
		return fmt.Errorf("manual model add request is nil")
	}
	groupKey := model.NormalizeSiteGroupKey(req.GroupKey)
	if len(req.Models) == 0 {
		return fmt.Errorf("models is required")
	}
	if _, err := siteChannelAccount(siteID, accountID, ctx); err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(req.Models))
	rows := make([]model.SiteModel, 0, len(req.Models))
	now := time.Now()
	for _, item := range req.Models {
		modelName := strings.TrimSpace(item.ModelName)
		if modelName == "" {
			return fmt.Errorf("model name is required")
		}
		if _, ok := seen[modelName]; ok {
			return fmt.Errorf("duplicate model in request: %s", modelName)
		}
		seen[modelName] = struct{}{}
		routeType := model.NormalizeSiteModelRouteType(item.RouteType)
		if routeType == model.SiteModelRouteTypeUnknown {
			return fmt.Errorf("unsupported route type for model %s", modelName)
		}
		rows = append(rows, model.SiteModel{
			SiteAccountID:  accountID,
			GroupKey:       groupKey,
			ModelName:      modelName,
			Source:         "manual",
			RouteType:      routeType,
			RouteSource:    model.SiteModelRouteSourceManualOverride,
			ManualOverride: true,
			RouteUpdatedAt: &now,
		})
	}

	return db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []model.SiteModel
		if err := tx.Where("site_account_id = ? AND group_key = ?", accountID, groupKey).Find(&existing).Error; err != nil {
			return err
		}
		for _, item := range existing {
			if _, ok := seen[strings.TrimSpace(item.ModelName)]; ok {
				return fmt.Errorf("model already exists: %s", item.ModelName)
			}
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
		readyKey, err := siteGroupHasReadyTokenTx(tx, accountID, groupKey)
		if err != nil {
			return err
		}
		if !readyKey {
			return nil
		}
		return restoreSystemPausedSiteGroupProjectionTx(tx, accountID, groupKey)
	})
}

func SiteManualModelDelete(siteID int, accountID int, req *model.SiteManualModelDeleteRequest, ctx context.Context) error {
	if req == nil {
		return fmt.Errorf("manual model delete request is nil")
	}
	if _, err := siteChannelAccount(siteID, accountID, ctx); err != nil {
		return err
	}
	groupKey := model.NormalizeSiteGroupKey(req.GroupKey)
	modelName := strings.TrimSpace(req.ModelName)
	if modelName == "" {
		return fmt.Errorf("model name is required")
	}
	result := db.GetDB().WithContext(ctx).
		Where("site_account_id = ? AND group_key = ? AND model_name = ? AND source = ?", accountID, groupKey, modelName, "manual").
		Delete(&model.SiteModel{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("manual model not found")
	}
	return nil
}
