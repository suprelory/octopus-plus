package op

import (
	"context"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func normalizeEditableSourceTokenValue(value string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("key 不能为空")
	}
	if model.IsMaskedSiteTokenValue(normalized) {
		return "", fmt.Errorf("必须填写完整 Key，不能保存脱敏值")
	}
	return normalized, nil
}

func UpdateSiteSourceKeys(siteID int, accountID int, req *model.SiteSourceKeyUpdateRequest, ctx context.Context) error {
	if req == nil {
		return fmt.Errorf("site source key update request is nil")
	}
	targetGroupKey := model.NormalizeSiteGroupKey(req.GroupKey)

	site, err := SiteGet(siteID, ctx)
	if err != nil {
		return err
	}

	var account *model.SiteAccount
	for i := range site.Accounts {
		if site.Accounts[i].ID == accountID {
			account = &site.Accounts[i]
			break
		}
	}
	if account == nil {
		return newSiteChannelAccountNotFoundError()
	}

	return db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existingTokens []model.SiteToken
		if err := tx.Where("site_account_id = ? AND group_key = ?", accountID, targetGroupKey).Find(&existingTokens).Error; err != nil {
			return err
		}

		validIDs := make(map[int]model.SiteToken, len(existingTokens))
		for _, token := range existingTokens {
			validIDs[token.ID] = token
		}

		for _, item := range req.KeysToAdd {
			normalizedToken, err := normalizeEditableSourceTokenValue(item.Token)
			if err != nil {
				return err
			}
			row := model.SiteToken{
				SiteAccountID: accountID,
				Name:          strings.TrimSpace(item.Name),
				Token:         normalizedToken,
				GroupKey:      targetGroupKey,
				GroupName:     model.NormalizeSiteGroupName(targetGroupKey, targetGroupKey),
				Enabled:       item.Enabled,
				ValueStatus:   model.SiteTokenValueStatusReady,
				Source:        "manual",
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}

		for _, item := range req.KeysToUpdate {
			existing, ok := validIDs[item.ID]
			if !ok {
				continue
			}
			updates := map[string]any{}
			if item.Enabled != nil {
				updates["enabled"] = *item.Enabled
			}
			if item.Name != nil {
				updates["name"] = strings.TrimSpace(*item.Name)
			}
			if existing.Source != "manual" {
				updates["source"] = "manual"
			}
			if item.Token != nil {
				normalizedToken, err := normalizeEditableSourceTokenValue(*item.Token)
				if err != nil {
					return err
				}
				if existing.ValueStatus == model.SiteTokenValueStatusMaskedPending && model.IsMaskedSiteTokenValue(existing.Token) {
					if !model.SiteMaskedTokenMatches(normalizedToken, existing.Token) {
						return fmt.Errorf("新 Key 与已有脱敏 Key 模式不匹配，请确认输入")
					}
				}
				updates["token"] = normalizedToken
				updates["value_status"] = model.NormalizeSiteTokenValueStatus(existing.ValueStatus, normalizedToken)
			}
			if len(updates) == 0 {
				continue
			}
			if err := tx.Model(&model.SiteToken{}).Where("id = ? AND site_account_id = ? AND group_key = ?", item.ID, accountID, targetGroupKey).Updates(updates).Error; err != nil {
				return err
			}
		}

		if len(req.KeysToDelete) > 0 {
			deletableIDs := make([]int, 0, len(req.KeysToDelete))
			for _, id := range req.KeysToDelete {
				if _, ok := validIDs[id]; ok {
					deletableIDs = append(deletableIDs, id)
				}
			}
			if len(deletableIDs) > 0 {
				if err := tx.Where("id IN ? AND site_account_id = ? AND group_key = ?", deletableIDs, accountID, targetGroupKey).Delete(&model.SiteToken{}).Error; err != nil {
					return err
				}
			}
		}

		readyKey, err := siteGroupHasReadyTokenTx(tx, accountID, targetGroupKey)
		if err != nil {
			return err
		}
		hasModel, err := siteGroupHasModelTx(tx, accountID, targetGroupKey)
		if err != nil {
			return err
		}
		if !readyKey || !hasModel {
			return nil
		}
		return restoreSystemPausedSiteGroupProjectionTx(tx, accountID, targetGroupKey)
	})
}

func siteGroupHasReadyTokenTx(tx *gorm.DB, accountID int, groupKey string) (bool, error) {
	var tokens []model.SiteToken
	if err := tx.Where("site_account_id = ? AND group_key = ?", accountID, model.NormalizeSiteGroupKey(groupKey)).Find(&tokens).Error; err != nil {
		return false, err
	}
	for _, token := range tokens {
		if token.Enabled && model.IsReadySiteToken(token) && !model.IsMaskedSiteTokenValue(token.Token) {
			return true, nil
		}
	}
	return false, nil
}

func siteGroupHasModelTx(tx *gorm.DB, accountID int, groupKey string) (bool, error) {
	var count int64
	if err := tx.Model(&model.SiteModel{}).
		Where("site_account_id = ? AND group_key = ? AND disabled = ?", accountID, model.NormalizeSiteGroupKey(groupKey), false).
		Where("TRIM(model_name) <> ''").
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func restoreSystemPausedSiteGroupProjectionTx(tx *gorm.DB, accountID int, groupKey string) error {
	return tx.Model(&model.SiteUserGroup{}).
		Where("site_account_id = ? AND group_key = ?", accountID, model.NormalizeSiteGroupKey(groupKey)).
		Where("projection_suspended = ? OR model_sync_status IN ?", true, []model.SiteGroupModelSyncStatus{model.SiteGroupModelSyncStatusEmpty, model.SiteGroupModelSyncStatusMissingKey}).
		Updates(map[string]any{
			"projection_suspended":      false,
			"projection_suspend_reason": "",
			"projection_suspended_at":   nil,
			"model_sync_status":         model.SiteGroupModelSyncStatusIdle,
			"model_sync_message":        "",
			"model_sync_authoritative":  false,
			"model_sync_failure_count":  0,
		}).Error
}
