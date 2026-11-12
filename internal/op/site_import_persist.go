package op

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func upsertImportedSite(tx *gorm.DB, input importedSiteInput) (*model.Site, bool, error) {
	normalizedBaseURL := normalizeImportBaseURL(input.BaseURL)
	var siteRecord model.Site
	err := tx.Where("platform = ? AND base_url = ? AND kind = ?", input.Platform, normalizedBaseURL, model.SiteKindRelay).First(&siteRecord).Error
	if err == nil {
		return &siteRecord, false, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, fmt.Errorf("query site failed: %w", err)
	}

	siteRecord = model.Site{
		Name:     uniqueSiteName(tx, firstNonEmptyString(input.Name, normalizedBaseURL), model.SiteKindRelay),
		Platform: input.Platform,
		BaseURL:  normalizedBaseURL,
		Enabled:  true,
	}
	if err := siteRecord.Validate(); err != nil {
		return nil, false, err
	}
	if err := tx.Create(&siteRecord).Error; err != nil {
		return nil, false, fmt.Errorf("create site failed: %w", err)
	}
	return &siteRecord, true, nil
}

func replaceMetAPIAccountData(tx *gorm.DB, accountID int, data metAPIImportAccountData) (int, int, int, int, error) {
	groups := prepareMetAPIImportedGroups(accountID, data.Groups)
	tokens := prepareMetAPIImportedTokens(accountID, data.Tokens)
	models := prepareMetAPIImportedModels(accountID, append(data.Models, data.DisabledModels...))

	if err := tx.Where("site_account_id = ?", accountID).Delete(&model.SiteUserGroup{}).Error; err != nil {
		return 0, 0, 0, 0, err
	}
	if len(groups) > 0 {
		if err := tx.Create(&groups).Error; err != nil {
			return 0, 0, 0, 0, err
		}
	}

	if err := tx.Where("site_account_id = ?", accountID).Delete(&model.SiteToken{}).Error; err != nil {
		return 0, 0, 0, 0, err
	}
	if len(tokens) > 0 {
		if err := tx.Create(&tokens).Error; err != nil {
			return 0, 0, 0, 0, err
		}
	}

	if err := tx.Where("site_account_id = ?", accountID).Delete(&model.SiteModel{}).Error; err != nil {
		return 0, 0, 0, 0, err
	}
	if len(models) > 0 {
		if err := tx.Create(&models).Error; err != nil {
			return 0, 0, 0, 0, err
		}
	}

	disabled := 0
	for _, item := range models {
		if item.Disabled {
			disabled++
		}
	}
	return len(tokens), len(groups), len(models), disabled, nil
}

func prepareMetAPIImportedGroups(accountID int, groups []model.SiteUserGroup) []model.SiteUserGroup {
	seen := make(map[string]model.SiteUserGroup, len(groups))
	for _, group := range groups {
		groupKey := model.NormalizeSiteGroupKey(group.GroupKey)
		seen[groupKey] = model.SiteUserGroup{
			SiteAccountID: accountID,
			GroupKey:      groupKey,
			Name:          model.NormalizeSiteGroupName(groupKey, group.Name),
			RawPayload:    strings.TrimSpace(group.RawPayload),
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]model.SiteUserGroup, 0, len(keys))
	for _, key := range keys {
		result = append(result, seen[key])
	}
	return result
}

func prepareMetAPIImportedTokens(accountID int, tokens []model.SiteToken) []model.SiteToken {
	seen := make(map[string]model.SiteToken, len(tokens))
	for _, token := range tokens {
		tokenValue := strings.TrimSpace(token.Token)
		if tokenValue == "" {
			continue
		}
		groupKey := model.NormalizeSiteGroupKey(token.GroupKey)
		valueStatus := model.NormalizeSiteTokenValueStatus(token.ValueStatus, tokenValue)
		key := groupKey + "\x00" + model.NormalizeComparableSiteTokenValue(tokenValue)
		if key == groupKey+"\x00" {
			key = groupKey + "\x00" + tokenValue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = model.SiteToken{
			SiteAccountID: accountID,
			Name:          firstNonEmptyString(token.Name, groupKey),
			Token:         tokenValue,
			ValueStatus:   valueStatus,
			GroupKey:      groupKey,
			GroupName:     model.NormalizeSiteGroupName(groupKey, token.GroupName),
			Enabled:       token.Enabled && valueStatus == model.SiteTokenValueStatusReady,
			Source:        firstNonEmptyString(token.Source, "metapi"),
			IsDefault:     token.IsDefault && valueStatus == model.SiteTokenValueStatusReady,
			LastSyncAt:    token.LastSyncAt,
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]model.SiteToken, 0, len(keys))
	for _, key := range keys {
		result = append(result, seen[key])
	}
	return result
}

func prepareMetAPIImportedModels(accountID int, models []model.SiteModel) []model.SiteModel {
	seen := make(map[string]model.SiteModel, len(models))
	for _, item := range models {
		groupKey := model.NormalizeSiteGroupKey(item.GroupKey)
		modelName := strings.TrimSpace(item.ModelName)
		if modelName == "" {
			continue
		}
		key := groupKey + "\x00" + modelName
		current, exists := seen[key]
		if exists && current.Disabled && !item.Disabled {
			continue
		}
		item.SiteAccountID = accountID
		item.GroupKey = groupKey
		item.ModelName = modelName
		item.Source = firstNonEmptyString(item.Source, "metapi")
		if strings.TrimSpace(string(item.RouteType)) == "" {
			item.RouteType = model.InferSiteModelRouteType(modelName)
		} else {
			item.RouteType = model.NormalizeSiteModelRouteType(item.RouteType)
		}
		item.RouteSource = model.NormalizeSiteModelRouteSource(item.RouteSource, item.ManualOverride)
		seen[key] = item
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]model.SiteModel, 0, len(keys))
	for _, key := range keys {
		result = append(result, seen[key])
	}
	return result
}

func uniqueSiteName(tx *gorm.DB, baseName string, kind model.SiteKind) string {
	baseName = strings.TrimSpace(baseName)
	if baseName == "" {
		baseName = "imported-site"
	}
	candidate := baseName
	index := 2
	for {
		var count int64
		if err := tx.Model(&model.Site{}).Where("name = ? AND kind = ?", candidate, kind).Count(&count).Error; err != nil {
			return candidate
		}
		if count == 0 {
			return candidate
		}
		candidate = fmt.Sprintf("%s (%d)", baseName, index)
		index++
	}
}

func upsertImportedAccount(tx *gorm.DB, siteRecord *model.Site, input importedAccountInput) (*model.SiteAccount, bool, bool, error) {
	accountRecord, err := findImportedAccount(tx, siteRecord.ID, input)
	if err != nil {
		return nil, false, false, err
	}

	proxyMode, proxyConfigID, err := importedAccountProxyMode(tx, input.AccountProxy)
	if err != nil {
		return nil, false, false, err
	}

	if accountRecord == nil {
		created := model.SiteAccount{
			SiteID:                     siteRecord.ID,
			Name:                       strings.TrimSpace(input.Name),
			CredentialType:             input.CredentialType,
			Username:                   strings.TrimSpace(input.Username),
			Password:                   strings.TrimSpace(input.Password),
			AccessToken:                strings.TrimSpace(input.AccessToken),
			APIKey:                     strings.TrimSpace(input.APIKey),
			RefreshToken:               strings.TrimSpace(input.RefreshToken),
			TokenExpiresAt:             input.TokenExpiresAt,
			PlatformUserID:             input.PlatformUserID,
			ProxyMode:                  proxyMode,
			ProxyConfigID:              proxyConfigID,
			Enabled:                    input.Enabled,
			AutoSync:                   input.AutoSync,
			AutoCheckin:                input.AutoCheckin,
			RandomCheckin:              false,
			CheckinRandomWindowMinutes: 120,
			Balance:                    input.Balance,
			BalanceUsed:                input.BalanceUsed,
		}
		if err := created.Validate(); err != nil {
			return nil, false, false, err
		}
		if err := tx.Model(&model.SiteAccount{}).Create(map[string]any{
			"site_id":                       created.SiteID,
			"name":                          created.Name,
			"credential_type":               created.CredentialType,
			"username":                      created.Username,
			"password":                      created.Password,
			"access_token":                  created.AccessToken,
			"api_key":                       created.APIKey,
			"refresh_token":                 created.RefreshToken,
			"token_expires_at":              created.TokenExpiresAt,
			"platform_user_id":              created.PlatformUserID,
			"proxy_mode":                    created.ProxyMode,
			"proxy_config_id":               created.ProxyConfigID,
			"enabled":                       created.Enabled,
			"auto_sync":                     created.AutoSync,
			"auto_checkin":                  created.AutoCheckin,
			"random_checkin":                created.RandomCheckin,
			"checkin_random_window_minutes": created.CheckinRandomWindowMinutes,
			"balance":                       created.Balance,
			"balance_used":                  created.BalanceUsed,
			"last_sync_status":              model.SiteExecutionStatusIdle,
			"last_checkin_status":           model.SiteExecutionStatusIdle,
		}).Error; err != nil {
			return nil, false, false, fmt.Errorf("create site account failed: %w", err)
		}
		accountRecord, err = findImportedAccount(tx, siteRecord.ID, input)
		if err != nil {
			return nil, false, false, err
		}
		if accountRecord == nil {
			return nil, false, false, fmt.Errorf("created site account could not be reloaded")
		}
		return accountRecord, true, false, nil
	}

	merged := *accountRecord
	merged.Name = strings.TrimSpace(input.Name)
	merged.CredentialType = input.CredentialType
	merged.Username = strings.TrimSpace(input.Username)
	merged.Password = strings.TrimSpace(input.Password)
	merged.AccessToken = strings.TrimSpace(input.AccessToken)
	merged.APIKey = strings.TrimSpace(input.APIKey)
	merged.RefreshToken = strings.TrimSpace(input.RefreshToken)
	merged.TokenExpiresAt = input.TokenExpiresAt
	merged.PlatformUserID = input.PlatformUserID
	merged.ProxyMode = proxyMode
	merged.ProxyConfigID = proxyConfigID
	merged.AutoCheckin = input.AutoCheckin
	if err := merged.Validate(); err != nil {
		return nil, false, false, err
	}

	updates := map[string]any{
		"name":               merged.Name,
		"credential_version": accountRecord.CredentialVersion + 1,
		"credential_type":    merged.CredentialType,
		"username":           merged.Username,
		"password":           merged.Password,
		"access_token":       merged.AccessToken,
		"api_key":            merged.APIKey,
		"refresh_token":      merged.RefreshToken,
		"token_expires_at":   merged.TokenExpiresAt,
		"platform_user_id":   merged.PlatformUserID,
		"proxy_mode":         merged.ProxyMode,
		"proxy_config_id":    merged.ProxyConfigID,
		"auto_checkin":       merged.AutoCheckin,
	}
	sessionReplaced := !accountRecord.HasSameAccessTokenSession(&merged)
	if sessionReplaced {
		updates["token_refresh_lease"], updates["token_refresh_lease_until"] = "", 0
		updates["token_refreshed_at"] = int64(0)
	}
	written := tx.Model(&model.SiteAccount{}).Where("id = ? AND credential_version = ?", accountRecord.ID, accountRecord.CredentialVersion).Updates(updates)
	if written.Error != nil {
		return nil, false, false, fmt.Errorf("update site account failed: %w", written.Error)
	}
	if written.RowsAffected != 1 {
		return nil, false, false, siteCredentialConflictError()
	}
	accountRecord.CredentialVersion++
	if sessionReplaced {
		accountRecord.TokenRefreshLease, accountRecord.TokenRefreshLeaseUntil = "", 0
		accountRecord.TokenRefreshedAt = 0
	}
	accountRecord.Name = merged.Name
	accountRecord.CredentialType = merged.CredentialType
	accountRecord.Username = merged.Username
	accountRecord.Password = merged.Password
	accountRecord.AccessToken = merged.AccessToken
	accountRecord.APIKey = merged.APIKey
	accountRecord.RefreshToken = merged.RefreshToken
	accountRecord.TokenExpiresAt = merged.TokenExpiresAt
	accountRecord.PlatformUserID = merged.PlatformUserID
	accountRecord.ProxyMode = merged.ProxyMode
	accountRecord.ProxyConfigID = merged.ProxyConfigID
	accountRecord.AutoCheckin = merged.AutoCheckin
	return accountRecord, false, true, nil
}

func importedAccountProxyMode(tx *gorm.DB, rawProxy *string) (model.ProxyUsageMode, *int, error) {
	if rawProxy == nil || strings.TrimSpace(*rawProxy) == "" {
		return model.ProxyUsageModeInherit, nil, nil
	}
	normalized, err := model.NormalizeProxyURL(*rawProxy)
	if err != nil {
		return model.ProxyUsageModeInherit, nil, fmt.Errorf("invalid imported account proxy: %w", err)
	}
	var existing model.ProxyConfiguration
	if err := tx.Where("url = ?", normalized).First(&existing).Error; err == nil {
		if !existing.Enabled {
			if err := tx.Model(&existing).Update("enabled", true).Error; err != nil {
				return model.ProxyUsageModeInherit, nil, fmt.Errorf("enable imported proxy configuration failed: %w", err)
			}
		}
		return model.ProxyUsageModePool, &existing.ID, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ProxyUsageModeInherit, nil, err
	}
	item := model.ProxyConfiguration{
		Name:    uniqueProxyConfigurationName(tx, "Imported Proxy"),
		URL:     normalized,
		Enabled: true,
		Remark:  "由站点导入代理配置生成",
	}
	if err := tx.Create(&item).Error; err != nil {
		return model.ProxyUsageModeInherit, nil, fmt.Errorf("create imported proxy configuration failed: %w", err)
	}
	return model.ProxyUsageModePool, &item.ID, nil
}

func uniqueProxyConfigurationName(tx *gorm.DB, baseName string) string {
	baseName = strings.TrimSpace(baseName)
	if baseName == "" {
		baseName = "Imported Proxy"
	}
	candidate := baseName
	index := 2
	for {
		var count int64
		if err := tx.Model(&model.ProxyConfiguration{}).Where("name = ?", candidate).Count(&count).Error; err != nil {
			return candidate
		}
		if count == 0 {
			return candidate
		}
		candidate = fmt.Sprintf("%s %d", baseName, index)
		index++
	}
}

func findImportedAccount(tx *gorm.DB, siteID int, input importedAccountInput) (*model.SiteAccount, error) {
	findByQuery := func(query string, args ...any) (*model.SiteAccount, error) {
		var accountRecord model.SiteAccount
		err := tx.Where(query, args...).First(&accountRecord).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &accountRecord, nil
	}

	switch input.CredentialType {
	case model.SiteCredentialTypeUsernamePassword:
		if input.Username != "" {
			record, err := findByQuery("site_id = ? AND credential_type = ? AND username = ?", siteID, input.CredentialType, strings.TrimSpace(input.Username))
			if record != nil || err != nil {
				return record, err
			}
		}
	case model.SiteCredentialTypeAccessToken:
		if input.AccessToken != "" {
			record, err := findByQuery("site_id = ? AND credential_type = ? AND access_token = ?", siteID, input.CredentialType, strings.TrimSpace(input.AccessToken))
			if record != nil || err != nil {
				return record, err
			}
		}
	case model.SiteCredentialTypeAPIKey:
		if input.APIKey != "" {
			record, err := findByQuery("site_id = ? AND credential_type = ? AND api_key = ?", siteID, input.CredentialType, strings.TrimSpace(input.APIKey))
			if record != nil || err != nil {
				return record, err
			}
		}
	}

	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, nil
	}

	var matches []model.SiteAccount
	if err := tx.Where("site_id = ? AND name = ?", siteID, name).Find(&matches).Error; err != nil {
		return nil, fmt.Errorf("query site account by name failed: %w", err)
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}
	return nil, nil
}
