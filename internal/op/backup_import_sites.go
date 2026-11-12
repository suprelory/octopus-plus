package op

import (
	"errors"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"gorm.io/gorm"
)

func (s *dbImportState) importSites() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	proxyConfigIDMap := s.proxyIDs
	siteIDMap := s.siteIDs
	// Links may point at sites later in the dump, so remap them after all inserts.
	pendingLinks := make(map[int]int)
	// 4. Sites (dedup by kind+platform+base_url)
	for i := range dump.Sites {
		site := dump.Sites[i]
		oldID := site.ID
		site.ID = 0
		site.Accounts = nil
		if site.Kind == "" {
			site.Kind = model.SiteKindRelay
		}
		if err := site.Kind.Validate(); err != nil {
			return fmt.Errorf("import sites: %w", err)
		}
		linkedOldID := site.LinkedSiteID
		site.LinkedSiteID = nil
		remapProxyConfigID(&site.ProxyMode, &site.ProxyConfigID, proxyConfigIDMap)
		site.RouteBaseURLs = model.NormalizeSiteRouteBaseURLs(site.RouteBaseURLs)

		// Preserve the path in base_url (e.g. https://opencode.ai/zen/v1):
		// native backups already hold full, canonical URLs. Only trim like
		// Site.Normalize so dedup compares against the stored value. (Do not
		// use normalizeImportBaseURL here — it strips the path, which is only
		// correct for third-party imports.)
		site.BaseURL = strings.TrimRight(strings.TrimSpace(site.BaseURL), "/")

		var existing model.Site
		if err := tx.Where("platform = ? AND base_url = ? AND kind = ?", site.Platform, site.BaseURL, site.Kind).First(&existing).Error; err == nil {
			siteIDMap[oldID] = existing.ID
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import sites: %w", err)
		}
		site.Name = uniqueSiteName(tx, site.Name, site.Kind)
		enabled := site.Enabled
		if err := tx.Omit("Accounts").Create(&site).Error; err != nil {
			return fmt.Errorf("import sites: %w", err)
		}
		if err := tx.Model(&site).Update("enabled", enabled).Error; err != nil {
			return fmt.Errorf("import site state: %w", err)
		}
		siteIDMap[oldID] = site.ID
		if site.Kind == model.SiteKindCheckin && linkedOldID != nil {
			pendingLinks[site.ID] = *linkedOldID
		}
		res.RowsAffected["sites"]++
	}
	for siteID, linkedOldID := range pendingLinks {
		linkedID, ok := siteIDMap[linkedOldID]
		if !ok || linkedID == siteID {
			continue
		}
		var linked model.Site
		if err := tx.Select("kind").First(&linked, linkedID).Error; err != nil {
			return fmt.Errorf("import site links: %w", err)
		}
		if linked.Kind != model.SiteKindRelay {
			continue
		}
		if err := tx.Model(&model.Site{}).Where("id = ?", siteID).Update("linked_site_id", linkedID).Error; err != nil {
			return fmt.Errorf("import site links: %w", err)
		}
	}
	return nil
}

func (s *dbImportState) importAccounts() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	proxyConfigIDMap := s.proxyIDs
	siteIDMap := s.siteIDs
	accountIDMap := s.accountIDs
	pendingSources := make(map[int]int)
	pendingLinkedAccounts := make(map[int]int)
	type pendingWorkflow struct {
		site    model.Site
		account model.SiteAccount
	}
	pendingWorkflows := make(map[int]pendingWorkflow)
	// 5. SiteAccounts (remap site_id, dedup by site_id+name)
	for i := range dump.SiteAccounts {
		account := dump.SiteAccounts[i]
		var expectedWorkflow *pendingWorkflow
		if account.CheckinPending != nil {
			for _, originalSite := range dump.Sites {
				if originalSite.ID != account.SiteID || account.CheckinPending.Fingerprint != originalSite.CheckinWorkflowFingerprint(&account) {
					continue
				}
				expectedSite, expectedAccount := originalSite, account
				remapProxyConfigID(&expectedSite.ProxyMode, &expectedSite.ProxyConfigID, proxyConfigIDMap)
				remapProxyConfigID(&expectedAccount.ProxyMode, &expectedAccount.ProxyConfigID, proxyConfigIDMap)
				if expectedSite.LinkedSiteID != nil {
					if mapped, ok := siteIDMap[*expectedSite.LinkedSiteID]; ok {
						expectedSite.LinkedSiteID = &mapped
					} else {
						expectedSite.LinkedSiteID = nil
					}
				}
				expectedWorkflow = &pendingWorkflow{site: expectedSite, account: expectedAccount}
				break
			}
		}
		oldID := account.ID
		account.ID = 0
		sourceAccountID := account.CheckinSourceAccountID
		account.CheckinSourceAccountID = nil
		linkedAccountID := account.LinkedAccountID
		account.LinkedAccountID = nil
		account.Tokens = nil
		account.UserGroups = nil
		account.Models = nil
		account.ChannelBindings = nil
		remapProxyConfigID(&account.ProxyMode, &account.ProxyConfigID, proxyConfigIDMap)

		if newSiteID, ok := siteIDMap[account.SiteID]; ok {
			account.SiteID = newSiteID
		}

		var existing model.SiteAccount
		if err := tx.Where("site_id = ? AND name = ?", account.SiteID, strings.TrimSpace(account.Name)).First(&existing).Error; err == nil {
			accountIDMap[oldID] = existing.ID
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import site_accounts: %w", err)
		}
		var site model.Site
		if err := tx.Select("kind").First(&site, account.SiteID).Error; err != nil {
			return fmt.Errorf("import account site: %w", err)
		}
		states := map[string]any{
			"enabled": account.Enabled, "auto_sync": account.AutoSync && !site.IsCheckinOnly(),
			"auto_checkin":                  account.AutoCheckin,
			"checkin_random_window_minutes": account.CheckinRandomWindowMinutes,
		}
		if err := tx.Omit("Tokens", "UserGroups", "Models", "ChannelBindings").Create(&account).Error; err != nil {
			return fmt.Errorf("import site_accounts: %w", err)
		}
		if err := tx.Model(&account).Updates(states).Error; err != nil {
			return fmt.Errorf("import account state: %w", err)
		}
		accountIDMap[oldID] = account.ID
		if site.IsCheckinOnly() && sourceAccountID != nil {
			pendingSources[account.ID] = *sourceAccountID
		}
		if site.IsCheckinOnly() && linkedAccountID != nil {
			pendingLinkedAccounts[account.ID] = *linkedAccountID
		}
		res.RowsAffected["site_accounts"]++
		if expectedWorkflow != nil {
			pendingWorkflows[account.ID] = *expectedWorkflow
		}
	}
	for accountID, oldSourceID := range pendingSources {
		if sourceID, ok := accountIDMap[oldSourceID]; ok && sourceID != accountID {
			if err := tx.Model(&model.SiteAccount{}).Where("id = ?", accountID).Update("checkin_source_account_id", sourceID).Error; err != nil {
				return fmt.Errorf("import check-in account references: %w", err)
			}
		}
	}
	for accountID, oldLinkedID := range pendingLinkedAccounts {
		if linkedID, ok := accountIDMap[oldLinkedID]; ok && linkedID != accountID {
			if err := tx.Model(&model.SiteAccount{}).Where("id = ?", accountID).Update("linked_account_id", linkedID).Error; err != nil {
				return fmt.Errorf("import linked checkin accounts: %w", err)
			}
		}
	}
	for accountID, expected := range pendingWorkflows {
		if expected.site.CheckinUseSubscriptionToken && expected.account.LinkedAccountID != nil {
			linkedID, ok := accountIDMap[*expected.account.LinkedAccountID]
			if !ok {
				continue
			}
			expected.account.LinkedAccountID = &linkedID
		}
		expectedFingerprint := expected.site.CheckinWorkflowFingerprint(&expected.account)
		var account model.SiteAccount
		if err := tx.First(&account, accountID).Error; err != nil {
			return err
		}
		var site model.Site
		if err := tx.First(&site, account.SiteID).Error; err != nil {
			return err
		}
		if site.CheckinWorkflowFingerprint(&account) != expectedFingerprint {
			continue
		}
		account.CheckinPending.Fingerprint = expectedFingerprint
		if err := tx.Model(&account).Select("checkin_pending").Updates(&account).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *dbImportState) importTokens() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	accountIDMap := s.accountIDs
	// 6. SiteTokens (remap site_account_id, dedup by site_account_id+token+group_key)
	for i := range dump.SiteTokens {
		token := dump.SiteTokens[i]
		token.ID = 0
		if newID, ok := accountIDMap[token.SiteAccountID]; ok {
			token.SiteAccountID = newID
		}
		var existing model.SiteToken
		if err := tx.Where("site_account_id = ? AND token = ? AND group_key = ?", token.SiteAccountID, token.Token, token.GroupKey).First(&existing).Error; err == nil {
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import site_tokens: %w", err)
		}
		if err := tx.Create(&token).Error; err != nil {
			return fmt.Errorf("import site_tokens: %w", err)
		}
		res.RowsAffected["site_tokens"]++
	}
	return nil
}

func (s *dbImportState) importUserGroups() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	accountIDMap := s.accountIDs
	userGroupIDMap := s.userGroupIDs
	// 7. SiteUserGroups (remap site_account_id, dedup by uniqueIndex)
	for i := range dump.SiteUserGroups {
		group := dump.SiteUserGroups[i]
		oldID := group.ID
		group.ID = 0
		if newID, ok := accountIDMap[group.SiteAccountID]; ok {
			group.SiteAccountID = newID
		}
		var existing model.SiteUserGroup
		if err := tx.Where("site_account_id = ? AND group_key = ?", group.SiteAccountID, group.GroupKey).First(&existing).Error; err == nil {
			userGroupIDMap[oldID] = existing.ID
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import site_user_groups: %w", err)
		}
		if err := tx.Create(&group).Error; err != nil {
			return fmt.Errorf("import site_user_groups: %w", err)
		}
		userGroupIDMap[oldID] = group.ID
		res.RowsAffected["site_user_groups"]++
	}
	return nil
}

func (s *dbImportState) importModels() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	accountIDMap := s.accountIDs
	// 8. SiteModels (remap site_account_id, dedup by uniqueIndex)
	for i := range dump.SiteModels {
		m := dump.SiteModels[i]
		m.ID = 0
		if newID, ok := accountIDMap[m.SiteAccountID]; ok {
			m.SiteAccountID = newID
		}
		var existing model.SiteModel
		if err := tx.Where("site_account_id = ? AND group_key = ? AND model_name = ?", m.SiteAccountID, m.GroupKey, m.ModelName).First(&existing).Error; err == nil {
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import site_models: %w", err)
		}
		if err := tx.Create(&m).Error; err != nil {
			return fmt.Errorf("import site_models: %w", err)
		}
		res.RowsAffected["site_models"]++
	}
	return nil
}

func (s *dbImportState) importBindings() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	siteIDMap := s.siteIDs
	accountIDMap := s.accountIDs
	userGroupIDMap := s.userGroupIDs
	resolveImportedChannel := s.resolveChannel
	// 9. SiteChannelBindings (remap all FKs, dedup by both unique constraints)
	for i := range dump.SiteChannelBindings {
		binding := dump.SiteChannelBindings[i]
		resolved, err := resolveImportedChannel(binding.ChannelID)
		if err != nil {
			return fmt.Errorf("import site_channel_bindings: resolve channel: %w", err)
		}
		if !resolved.Supported {
			log.Warnw("skip missing or unsupported site channel binding during import", "channel_id", binding.ChannelID)
			continue
		}
		binding.ID = 0
		if newID, ok := siteIDMap[binding.SiteID]; ok {
			binding.SiteID = newID
		}
		if newID, ok := accountIDMap[binding.SiteAccountID]; ok {
			binding.SiteAccountID = newID
		}
		if binding.SiteUserGroupID != nil {
			if newID, ok := userGroupIDMap[*binding.SiteUserGroupID]; ok {
				binding.SiteUserGroupID = &newID
			}
		}
		binding.ChannelID = resolved.ID

		var existing model.SiteChannelBinding
		if err := tx.Where("site_account_id = ? AND group_key = ?", binding.SiteAccountID, binding.GroupKey).First(&existing).Error; err == nil {
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import site_channel_bindings: %w", err)
		}
		if err := tx.Where("channel_id = ?", binding.ChannelID).First(&existing).Error; err == nil {
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("import site_channel_bindings: %w", err)
		}
		if err := tx.Create(&binding).Error; err != nil {
			return fmt.Errorf("import site_channel_bindings: %w", err)
		}
		res.RowsAffected["site_channel_bindings"]++
	}
	return nil
}
