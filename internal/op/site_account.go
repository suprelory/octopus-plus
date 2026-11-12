package op

import (
	"context"
	"fmt"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func normalizeSiteAccountProxyFields(account *model.SiteAccount) {
	if account == nil {
		return
	}
	if account.ProxyMode == "" {
		account.ProxyMode = model.ProxyUsageModeInherit
	}
	if account.ProxyMode != model.ProxyUsageModePool {
		account.ProxyConfigID = nil
	}
}

func SiteAccountGet(id int, ctx context.Context) (*model.SiteAccount, error) {
	var account model.SiteAccount
	if err := db.GetDB().WithContext(ctx).
		Preload("Tokens").
		Preload("UserGroups").
		Preload("Models").
		Preload("ChannelBindings").
		First(&account, id).Error; err != nil {
		return nil, err
	}
	normalizeSiteAccountProxyFields(&account)
	return &account, nil
}

func SiteAccountCreate(account *model.SiteAccount, ctx context.Context) error {
	if account == nil {
		return fmt.Errorf("site account is nil")
	}
	if err := account.Validate(); err != nil {
		return err
	}
	account.CheckinSourceAccountID = nil
	account.CheckinPending = nil // Execution state is owned by the server.
	account.CredentialVersion = 0
	account.TokenRefreshedAt = 0
	account.TokenRefreshLease, account.TokenRefreshLeaseUntil = "", 0
	var site model.Site
	if err := db.GetDB().WithContext(ctx).First(&site, account.SiteID).Error; err != nil {
		return fmt.Errorf("load account site: %w", err)
	}
	if err := validateSiteAccountCredentials(&site, account, ctx); err != nil {
		return err
	}
	if site.IsCheckinOnly() {
		account.AutoSync, account.AutoSyncSet = false, true
	} else {
		account.AutoCheckin, account.AutoCheckinSet = false, true
	}
	if account.ProxyMode == model.ProxyUsageModePool && account.ProxyConfigID != nil {
		if _, err := ProxyURLForConfig(*account.ProxyConfigID, ctx); err != nil {
			return err
		}
	}
	if (account.EnabledSet && !account.Enabled) || (account.AutoSyncSet && !account.AutoSync) || (account.AutoCheckinSet && !account.AutoCheckin) {
		explicitEnabled := account.Enabled
		explicitAutoSync := account.AutoSync
		explicitAutoCheckin := account.AutoCheckin
		updates := map[string]any{}
		if account.EnabledSet && !account.Enabled {
			updates["enabled"] = false
		}
		if account.AutoSyncSet && !account.AutoSync {
			updates["auto_sync"] = false
		}
		if account.AutoCheckinSet && !account.AutoCheckin {
			updates["auto_checkin"] = false
		}
		err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(account).Error; err != nil {
				return err
			}
			return tx.Model(&model.SiteAccount{}).Where("id = ?", account.ID).Updates(updates).Error
		})
		if account.EnabledSet {
			account.Enabled = explicitEnabled
		}
		if account.AutoSyncSet {
			account.AutoSync = explicitAutoSync
		}
		if account.AutoCheckinSet {
			account.AutoCheckin = explicitAutoCheckin
		}
		return err
	}
	return db.GetDB().WithContext(ctx).Create(account).Error
}

func SiteAccountUpdate(req *model.SiteAccountUpdateRequest, ctx context.Context) (*model.SiteAccount, error) {
	if req == nil {
		return nil, fmt.Errorf("site account update request is nil")
	}

	var account model.SiteAccount
	if err := db.GetDB().WithContext(ctx).First(&account, req.ID).Error; err != nil {
		return nil, fmt.Errorf("site account not found")
	}

	merged := account
	var selectFields []string
	updates := model.SiteAccount{ID: req.ID}
	var site model.Site
	if err := db.GetDB().WithContext(ctx).First(&site, account.SiteID).Error; err != nil {
		return nil, fmt.Errorf("load account site: %w", err)
	}
	isCheckin := site.IsCheckinOnly()
	credentialsWritten := req.CredentialType != nil || req.Username != nil || req.Password != nil ||
		req.AccessToken != nil || req.APIKey != nil || req.RefreshToken != nil || req.Cookie != nil ||
		req.TokenExpiresAt != nil || req.PlatformUserIDSet || req.ProxyMode != nil || req.ProxyConfigIDSet
	if credentialsWritten && site.Platform == model.SitePlatformSub2API && !isCheckin && req.CredentialVersion == nil {
		return nil, siteCredentialConflictError()
	}
	if credentialsWritten && req.CredentialVersion != nil && *req.CredentialVersion != account.CredentialVersion {
		return nil, siteCredentialConflictError()
	}

	if req.Name != nil {
		merged.Name = *req.Name
		selectFields = append(selectFields, "name")
	}
	if req.CredentialType != nil {
		merged.CredentialType = *req.CredentialType
		selectFields = append(selectFields, "credential_type")
	}
	if req.Username != nil {
		merged.Username = *req.Username
		selectFields = append(selectFields, "username")
	}
	if req.Password != nil {
		merged.Password = *req.Password
		selectFields = append(selectFields, "password")
	}
	if req.AccessToken != nil {
		merged.AccessToken = *req.AccessToken
		selectFields = append(selectFields, "access_token")
	}
	if req.APIKey != nil {
		merged.APIKey = *req.APIKey
		selectFields = append(selectFields, "api_key")
	}
	if req.RefreshToken != nil {
		merged.RefreshToken = *req.RefreshToken
		selectFields = append(selectFields, "refresh_token")
	}
	if req.Cookie != nil {
		merged.Cookie = *req.Cookie
		selectFields = append(selectFields, "cookie")
	}
	if req.LinkedAccountIDSet {
		merged.LinkedAccountID = req.LinkedAccountID
		selectFields = append(selectFields, "linked_account_id")
	}
	if req.TokenExpiresAt != nil {
		merged.TokenExpiresAt = *req.TokenExpiresAt
		selectFields = append(selectFields, "token_expires_at")
	}
	if req.PlatformUserIDSet {
		merged.PlatformUserID = req.PlatformUserID
		selectFields = append(selectFields, "platform_user_id")
	}
	if req.ProxyMode != nil {
		merged.ProxyMode = *req.ProxyMode
		selectFields = append(selectFields, "proxy_mode")
	}
	if req.ProxyConfigIDSet || (req.ProxyMode != nil && *req.ProxyMode != model.ProxyUsageModePool) {
		if req.ProxyMode != nil && *req.ProxyMode != model.ProxyUsageModePool {
			merged.ProxyConfigID = nil
		} else {
			merged.ProxyConfigID = req.ProxyConfigID
		}
		selectFields = append(selectFields, "proxy_config_id")
	}
	if req.Enabled != nil {
		merged.Enabled = *req.Enabled
		selectFields = append(selectFields, "enabled")
	}
	if req.AutoSync != nil {
		merged.AutoSync = *req.AutoSync && !isCheckin
		selectFields = append(selectFields, "auto_sync")
	}
	if req.AutoCheckin != nil {
		merged.AutoCheckin = *req.AutoCheckin && isCheckin
		selectFields = append(selectFields, "auto_checkin")
	}
	if req.RandomCheckin != nil {
		merged.RandomCheckin = *req.RandomCheckin
		selectFields = append(selectFields, "random_checkin")
	}
	if req.CheckinIntervalHours != nil {
		merged.CheckinIntervalHours = *req.CheckinIntervalHours
		selectFields = append(selectFields, "checkin_interval_hours")
	}
	if req.CheckinRandomWindowMinutes != nil {
		merged.CheckinRandomWindowMinutes = *req.CheckinRandomWindowMinutes
		selectFields = append(selectFields, "checkin_random_window_minutes")
	}

	if len(selectFields) > 0 {
		if err := merged.Validate(); err != nil {
			return nil, err
		}
		if err := validateSiteAccountCredentials(&site, &merged, ctx); err != nil {
			return nil, err
		}
		if !site.CheckinUseSubscriptionToken && merged.ProxyMode == model.ProxyUsageModePool && merged.ProxyConfigID != nil {
			if _, err := ProxyURLForConfig(*merged.ProxyConfigID, ctx); err != nil {
				return nil, err
			}
		}
	}
	if req.Name != nil {
		updates.Name = merged.Name
	}
	if req.CredentialType != nil {
		updates.CredentialType = merged.CredentialType
	}
	if req.Username != nil {
		updates.Username = merged.Username
	}
	if req.Password != nil {
		updates.Password = merged.Password
	}
	if req.AccessToken != nil {
		updates.AccessToken = merged.AccessToken
	}
	if req.APIKey != nil {
		updates.APIKey = merged.APIKey
	}
	if req.RefreshToken != nil {
		updates.RefreshToken = merged.RefreshToken
	}
	if req.Cookie != nil {
		updates.Cookie = merged.Cookie
	}
	if req.LinkedAccountIDSet {
		updates.LinkedAccountID = merged.LinkedAccountID
	}
	if req.TokenExpiresAt != nil {
		updates.TokenExpiresAt = merged.TokenExpiresAt
	}
	if req.PlatformUserIDSet {
		updates.PlatformUserID = merged.PlatformUserID
	}
	if req.ProxyMode != nil {
		updates.ProxyMode = merged.ProxyMode
	}
	if req.ProxyConfigIDSet || (req.ProxyMode != nil && *req.ProxyMode != model.ProxyUsageModePool) {
		updates.ProxyConfigID = merged.ProxyConfigID
	}
	if req.Enabled != nil {
		updates.Enabled = merged.Enabled
	}
	if req.AutoSync != nil {
		updates.AutoSync = merged.AutoSync
	}
	if req.AutoCheckin != nil {
		updates.AutoCheckin = merged.AutoCheckin
	}
	if req.RandomCheckin != nil {
		updates.RandomCheckin = merged.RandomCheckin
	}
	if req.CheckinIntervalHours != nil {
		updates.CheckinIntervalHours = merged.CheckinIntervalHours
	}
	if req.CheckinRandomWindowMinutes != nil {
		updates.CheckinRandomWindowMinutes = merged.CheckinRandomWindowMinutes
	}

	if len(selectFields) > 0 {
		query := db.GetDB().WithContext(ctx).
			Model(&model.SiteAccount{}).
			Where("id = ?", req.ID)
		if credentialsWritten {
			query = query.Where("credential_version = ?", account.CredentialVersion)
			updates.CredentialVersion = account.CredentialVersion + 1
			selectFields = append(selectFields, "credential_version")
			// A settings edit may race with rotation of the same one-time token.
			// Preserve its lease unless the login session itself is replaced.
			if !account.HasSameAccessTokenSession(&merged) {
				selectFields = append(selectFields, "token_refresh_lease", "token_refresh_lease_until", "token_refreshed_at")
			}
		}
		written := query.Select(selectFields).Updates(&updates)
		if written.Error != nil {
			return nil, fmt.Errorf("failed to update site account: %w", written.Error)
		}
		if credentialsWritten && written.RowsAffected != 1 {
			return nil, siteCredentialConflictError()
		}
	}
	return SiteAccountGet(req.ID, ctx)
}

func SiteAccountEnabled(id int, enabled bool, ctx context.Context) error {
	return db.GetDB().WithContext(ctx).Model(&model.SiteAccount{}).Where("id = ?", id).Update("enabled", enabled).Error
}

func SiteAccountDel(id int, ctx context.Context) error {
	if err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := unlinkCheckinAccounts(tx, []int{id}); err != nil {
			return err
		}
		if err := tx.Model(&model.SiteAccount{}).Where("checkin_source_account_id = ?", id).Update("checkin_source_account_id", nil).Error; err != nil {
			return err
		}
		// Delete bindings before groups/accounts so FK-constrained databases do not
		// reject removing rows that bindings may still reference.
		if err := tx.Where("site_account_id = ?", id).Delete(&model.SiteChannelBinding{}).Error; err != nil {
			return err
		}
		if err := tx.Where("site_account_id = ?", id).Delete(&model.SiteToken{}).Error; err != nil {
			return err
		}
		if err := tx.Where("site_account_id = ?", id).Delete(&model.SiteUserGroup{}).Error; err != nil {
			return err
		}
		if err := tx.Where("site_account_id = ?", id).Delete(&model.SiteModel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("site_account_id = ?", id).Delete(&model.StatsSiteModelHourly{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.SiteAccount{}, id).Error
	}); err != nil {
		return err
	}
	invalidateSiteBindingCache()
	deleteSiteModelHourlyCacheForAccounts([]int{id})
	return nil
}
