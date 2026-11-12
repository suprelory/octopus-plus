package sitesync

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"gorm.io/gorm"
)

func loadSiteAccount(ctx context.Context, accountID int) (*model.Site, *model.SiteAccount, error) {
	account, err := op.SiteAccountGet(accountID, ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("site account not found")
	}
	siteRecord, err := op.SiteGet(account.SiteID, ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("site not found")
	}
	return siteRecord, account, nil
}

// Sync, manual import and key creation only apply to subscription sites.
func loadSyncableSiteAccount(ctx context.Context, accountID int) (*model.Site, *model.SiteAccount, error) {
	siteRecord, account, err := loadSiteAccount(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	if siteRecord.IsCheckinOnly() {
		return nil, nil, newCheckinOnlySiteError()
	}
	return siteRecord, account, nil
}

func listChannelBindingsByAccount(ctx context.Context, accountID int) ([]model.SiteChannelBinding, error) {
	var bindings []model.SiteChannelBinding
	if err := db.GetDB().WithContext(ctx).Where("site_account_id = ?", accountID).Order("id ASC").Find(&bindings).Error; err != nil {
		return nil, err
	}
	return bindings, nil
}

func deleteManagedChannelsByAccount(ctx context.Context, accountID int) error {
	bindings, err := listChannelBindingsByAccount(ctx, accountID)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if err := op.ChannelDelManaged(binding.ChannelID, ctx); err != nil {
			if isMissingManagedChannelError(err) {
				log.Warnf("managed channel %d already missing; deleting stale site binding", binding.ChannelID)
				continue
			}
			return fmt.Errorf("failed to delete managed channel %d: %w", binding.ChannelID, err)
		}
	}
	return db.GetDB().WithContext(ctx).Where("site_account_id = ?", accountID).Delete(&model.SiteChannelBinding{}).Error
}

func isMissingManagedChannelError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "channel not found")
}

func persistSyncSnapshot(ctx context.Context, accountID int, snapshot *syncSnapshot) error {
	if snapshot == nil {
		return newSnapshotNilError()
	}
	now := time.Now()
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existingGroups []model.SiteUserGroup
		if err := tx.Where("site_account_id = ?", accountID).Find(&existingGroups).Error; err != nil {
			return err
		}
		existingGroupMap := make(map[string]model.SiteUserGroup, len(existingGroups))
		for _, group := range existingGroups {
			existingGroupMap[model.NormalizeSiteGroupKey(group.GroupKey)] = group
		}

		if err := tx.Where("site_account_id = ?", accountID).Delete(&model.SiteUserGroup{}).Error; err != nil {
			return err
		}

		var existingTokens []model.SiteToken
		if err := tx.Where("site_account_id = ?", accountID).Order("id ASC").Find(&existingTokens).Error; err != nil {
			return err
		}

		var existingModels []model.SiteModel
		if err := tx.Where("site_account_id = ?", accountID).Find(&existingModels).Error; err != nil {
			return err
		}
		existingModelMap := make(map[string]model.SiteModel, len(existingModels))
		for _, item := range existingModels {
			key := model.NormalizeSiteGroupKey(item.GroupKey) + "\x00" + strings.TrimSpace(item.ModelName)
			existingModelMap[key] = item
		}

		updatePayload := map[string]any{
			"last_sync_at":      &now,
			"last_sync_status":  snapshot.status,
			"last_sync_message": sanitizeSiteStatusText(snapshot.message),
			"balance":           snapshot.balance,
			"balance_used":      snapshot.balanceUsed,
			"today_income":      snapshot.todayIncome,
		}
		if strings.TrimSpace(snapshot.accessToken) != "" {
			updatePayload["access_token"] = snapshotTokenUpdate(tx, snapshot.accessToken)
		}
		accountQuery := tx.Model(&model.SiteAccount{}).Where("id = ?", accountID)
		if snapshot.credentialVersion != nil {
			var current model.SiteAccount
			if err := tx.First(&current, accountID).Error; err != nil {
				return err
			}
			replacement := current
			replacement.AccessToken = strings.TrimSpace(snapshot.accessToken)
			accountQuery = accountQuery.Where("credential_version = ?", *snapshot.credentialVersion)
			updatePayload["access_token"] = strings.TrimSpace(snapshot.accessToken)
			updatePayload["token_expires_at"] = int64(0)
			updatePayload["credential_version"] = *snapshot.credentialVersion + 1
			if !current.HasSameAccessTokenSession(&replacement) {
				updatePayload["token_refresh_lease"], updatePayload["token_refresh_lease_until"] = "", 0
				updatePayload["token_refreshed_at"] = int64(0)
			}
		}
		written := accountQuery.Updates(updatePayload)
		if written.Error != nil {
			return written.Error
		}
		if snapshot.credentialVersion != nil && written.RowsAffected != 1 {
			return newSub2APISessionError(sub2APISessionChanged, "订阅账号凭据已更新，请重新预览后再应用")
		}

		groupResultMap := make(map[string]siteGroupSyncResult, len(snapshot.groupResults))
		for _, result := range snapshot.groupResults {
			groupResultMap[model.NormalizeSiteGroupKey(result.GroupKey)] = result
		}
		for i := range snapshot.groups {
			snapshot.groups[i].SiteAccountID = accountID
			snapshot.groups[i].GroupKey = model.NormalizeSiteGroupKey(snapshot.groups[i].GroupKey)
			var existing *model.SiteUserGroup
			if item, ok := existingGroupMap[snapshot.groups[i].GroupKey]; ok {
				itemCopy := item
				existing = &itemCopy
				snapshot.groups[i].ProjectionDisabled = item.ProjectionDisabled
			}
			if result, ok := groupResultMap[snapshot.groups[i].GroupKey]; ok {
				applyPersistedGroupSyncState(&snapshot.groups[i], existing, result, now)
			} else if existing != nil {
				copyPersistedGroupSyncState(&snapshot.groups[i], *existing)
			}
		}
		mergedTokens := mergePersistedSiteTokens(accountID, existingTokens, snapshot.tokens, now)
		incomingModels := preparePersistedSyncModels(accountID, snapshot.models, existingModelMap, now)
		finalModels := mergePersistedSiteModelsByGroup(existingModels, incomingModels, snapshot.groupResults)

		if len(snapshot.groups) > 0 {
			if err := tx.Create(&snapshot.groups).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("site_account_id = ?", accountID).Delete(&model.SiteToken{}).Error; err != nil {
			return err
		}
		if len(mergedTokens) > 0 {
			if err := tx.Create(&mergedTokens).Error; err != nil {
				return err
			}
		}
		if len(finalModels) > 0 {
			if err := tx.Where("site_account_id = ?", accountID).Delete(&model.SiteModel{}).Error; err != nil {
				return err
			}
			if err := tx.Create(&finalModels).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Where("site_account_id = ?", accountID).Delete(&model.SiteModel{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func updateAccountSyncState(ctx context.Context, accountID int, status model.SiteExecutionStatus, message string, accessToken string) error {
	now := time.Now()
	updatePayload := map[string]any{
		"last_sync_at":      &now,
		"last_sync_status":  status,
		"last_sync_message": sanitizeSiteStatusText(message),
	}
	if strings.TrimSpace(accessToken) != "" {
		updatePayload["access_token"] = snapshotTokenUpdate(db.GetDB(), accessToken)
	}
	return db.GetDB().WithContext(ctx).Model(&model.SiteAccount{}).Where("id = ?", accountID).Updates(updatePayload).Error
}

// A sync snapshot may outlive a Sub2API refresh or a manual credential edit.
// Only the session manager and explicit credential edits can write its token.
func snapshotTokenUpdate(conn *gorm.DB, token string) any {
	sites := conn.Model(&model.Site{}).Select("id").Where("platform = ?", model.SitePlatformSub2API)
	return gorm.Expr("CASE WHEN site_id IN (?) THEN access_token ELSE ? END", sites, strings.TrimSpace(token))
}

func updateAccountCheckinState(tx *gorm.DB, siteRecord *model.Site, account *model.SiteAccount, result *model.SiteCheckinResult, accessToken string, now time.Time) error {
	if siteRecord == nil || account == nil {
		return fmt.Errorf("site or account is nil")
	}
	status, message := result.Status, result.Message
	updatePayload := map[string]any{
		"last_checkin_at":      &now,
		"last_checkin_status":  status,
		"last_checkin_message": sanitizeSiteStatusText(message),
	}
	account.LastCheckinAt = &now
	account.LastCheckinStatus = status
	if status == model.SiteExecutionStatusPending {
		next := now.Add(10 * time.Minute)
		updatePayload["next_auto_checkin_at"] = &next
		if !account.AutoCheckin || !account.Enabled || !siteRecord.Enabled {
			updatePayload["next_auto_checkin_at"] = nil
		}
	} else if status == model.SiteExecutionStatusSuccess {
		successAt := now
		// A completed workflow can remain on the account after switching adapters.
		// Only the workflow that produced this result determines its success date.
		if result.Workflow != nil {
			successAt = result.Workflow.StartedAt
		}
		account.LastCheckinSuccessAt = &successAt
		account.CheckinFailureCount = 0
		updatePayload["last_checkin_success_at"] = &successAt
		updatePayload["checkin_failure_count"] = 0
		nextAt := buildNextAutoCheckinAt(siteRecord, account, now)
		account.NextAutoCheckinAt = nextAt
		updatePayload["next_auto_checkin_at"] = nextAt
	} else {
		account.CheckinFailureCount++
		updatePayload["checkin_failure_count"] = account.CheckinFailureCount
		var nextAt *time.Time
		if siteRecord.Enabled && account.Enabled && account.AutoCheckin && siteRecord.ResolveCheckinCapability().Enabled {
			nextAt = buildNextCheckinRetryAt(siteRecord, now, status, message, account.CheckinFailureCount)
			if result.Workflow != nil && result.Workflow.Finished {
				nextAt = buildNextAutoCheckinAt(siteRecord, account, now)
			}
		}
		account.NextAutoCheckinAt = nextAt
		updatePayload["next_auto_checkin_at"] = nextAt
	}
	if !siteRecord.CheckinHTTPEnabled && account.LinkedAccountID == nil && account.CredentialType != model.SiteCredentialTypeLinkedAccount && strings.TrimSpace(accessToken) != "" {
		updatePayload["access_token"] = strings.TrimSpace(accessToken)
	}
	return tx.Model(&model.SiteAccount{}).Where("id = ?", account.ID).Updates(updatePayload).Error
}
