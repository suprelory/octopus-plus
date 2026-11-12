package op

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func SiteList(ctx context.Context) ([]model.Site, error) {
	var sites []model.Site
	if err := db.GetDB().WithContext(ctx).
		Preload("Accounts").
		Preload("Accounts.Tokens").
		Preload("Accounts.UserGroups").
		Preload("Accounts.Models").
		Preload("Accounts.ChannelBindings").
		Where("archived = ?", false).
		Order("is_pinned DESC, sort_order ASC, id ASC").
		Find(&sites).Error; err != nil {
		return nil, err
	}
	for i := range sites {
		normalizeSiteProxyFields(&sites[i])
	}
	return sites, nil
}

func SiteListArchived(ctx context.Context) ([]model.Site, error) {
	var sites []model.Site
	if err := db.GetDB().WithContext(ctx).
		Preload("Accounts").
		Preload("Accounts.Tokens").
		Preload("Accounts.UserGroups").
		Preload("Accounts.Models").
		Preload("Accounts.ChannelBindings").
		Where("archived = ?", true).
		Order("archived_at DESC, id ASC").
		Find(&sites).Error; err != nil {
		return nil, err
	}
	for i := range sites {
		normalizeSiteProxyFields(&sites[i])
	}
	return sites, nil
}

func SiteGet(id int, ctx context.Context) (*model.Site, error) {
	var site model.Site
	if err := db.GetDB().WithContext(ctx).
		Preload("Accounts").
		Preload("Accounts.Tokens").
		Preload("Accounts.UserGroups").
		Preload("Accounts.Models").
		Preload("Accounts.ChannelBindings").
		First(&site, id).Error; err != nil {
		return nil, err
	}
	normalizeSiteProxyFields(&site)
	return &site, nil
}

func normalizeSiteProxyFields(site *model.Site) {
	if site == nil {
		return
	}
	if site.Kind == "" {
		site.Kind = model.SiteKindRelay
	}
	if site.ProxyMode == "" {
		site.ProxyMode = model.ProxyUsageModeDirect
	}
	if site.ProxyMode != model.ProxyUsageModePool {
		site.ProxyConfigID = nil
	}
	for i := range site.Accounts {
		normalizeSiteAccountProxyFields(&site.Accounts[i])
	}
}

// Platform check-ins can use accounts from the linked subscription site;
// custom HTTP check-ins can use its token or query the reward account's balance.
func validateLinkedSite(site *model.Site, ctx context.Context) error {
	if site.LinkedSiteID == nil {
		return nil
	}
	var linked model.Site
	if err := db.GetDB().WithContext(ctx).Select("id", "kind", "platform").First(&linked, *site.LinkedSiteID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("linked site not found")
		}
		return err
	}
	if linked.Kind == model.SiteKindCheckin {
		return fmt.Errorf("a check-in site can only be linked to a subscription site")
	}
	if site.CheckinUseSubscriptionToken && linked.Platform != model.SitePlatformSub2API {
		return fmt.Errorf("subscription token checkin requires a linked Sub2API subscription site")
	}
	return nil
}

func SiteCreate(site *model.Site, ctx context.Context) error {
	if site == nil {
		return fmt.Errorf("site is nil")
	}
	if err := site.Validate(); err != nil {
		return err
	}
	if err := validateLinkedSite(site, ctx); err != nil {
		return err
	}
	// Capability evidence can only originate from an actual upstream execution.
	site.CheckinVerificationStatus = model.SiteCheckinSupportUnknown
	site.CheckinVerificationFingerprint = ""
	site.CheckinVerifiedAt = nil
	if site.ProxyMode == model.ProxyUsageModePool && site.ProxyConfigID != nil {
		if _, err := ProxyURLForConfig(*site.ProxyConfigID, ctx); err != nil {
			return err
		}
	}
	if site.EnabledSet && !site.Enabled {
		err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(site).Error; err != nil {
				return err
			}
			return tx.Model(&model.Site{}).Where("id = ?", site.ID).Update("enabled", false).Error
		})
		site.Enabled = false
		return siteNameConflictError(err, site.Name, site.Kind, 0, ctx)
	}
	err := db.GetDB().WithContext(ctx).Create(site).Error
	return siteNameConflictError(err, site.Name, site.Kind, 0, ctx)
}

func SiteUpdate(req *model.SiteUpdateRequest, ctx context.Context) (*model.Site, error) {
	if req == nil {
		return nil, fmt.Errorf("site update request is nil")
	}
	var site model.Site
	if err := db.GetDB().WithContext(ctx).First(&site, req.ID).Error; err != nil {
		return nil, fmt.Errorf("site not found")
	}

	merged := site
	var selectFields []string
	updates := model.Site{ID: req.ID}

	if req.Name != nil {
		merged.Name = *req.Name
		selectFields = append(selectFields, "name")
	}
	if req.Platform != nil {
		merged.Platform = *req.Platform
		selectFields = append(selectFields, "platform")
	}
	if req.BaseURL != nil {
		merged.BaseURL = *req.BaseURL
		selectFields = append(selectFields, "base_url")
	}
	if req.Enabled != nil {
		merged.Enabled = *req.Enabled
		selectFields = append(selectFields, "enabled")
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
	if req.ExternalCheckinSet {
		merged.ExternalCheckinURL = req.ExternalCheckinURL
		selectFields = append(selectFields, "external_checkin_url")
	}
	if req.LinkedSiteIDSet {
		merged.LinkedSiteID = req.LinkedSiteID
		selectFields = append(selectFields, "linked_site_id")
	}
	if req.CheckinHTTPEnabled != nil {
		merged.CheckinHTTPEnabled = *req.CheckinHTTPEnabled
		selectFields = append(selectFields, "checkin_http_enabled")
	}
	if req.CheckinUseSubscriptionToken != nil {
		merged.CheckinUseSubscriptionToken = *req.CheckinUseSubscriptionToken
		selectFields = append(selectFields, "checkin_use_subscription_token")
	}
	if req.CheckinMode != nil {
		merged.CheckinMode = *req.CheckinMode
		selectFields = append(selectFields, "checkin_mode")
	}
	if req.CheckinHTTPMethod != nil {
		merged.CheckinHTTPMethod = *req.CheckinHTTPMethod
		selectFields = append(selectFields, "checkin_http_method")
	}
	if req.CheckinHTTPPath != nil {
		merged.CheckinHTTPPath = *req.CheckinHTTPPath
		selectFields = append(selectFields, "checkin_http_path")
	}
	if req.CheckinHTTPBody != nil {
		merged.CheckinHTTPBody = *req.CheckinHTTPBody
		selectFields = append(selectFields, "checkin_http_body")
	}
	if req.CheckinHTTPHeaders != nil {
		merged.CheckinHTTPHeaders = *req.CheckinHTTPHeaders
		selectFields = append(selectFields, "checkin_http_headers")
	}
	if req.CheckinRewardExtractor != nil {
		merged.CheckinRewardExtractor = *req.CheckinRewardExtractor
		selectFields = append(selectFields, "checkin_reward_extractor")
	}
	if req.CheckinStatusSet {
		merged.CheckinStatus = req.CheckinStatus
		selectFields = append(selectFields, "checkin_status")
	}
	if req.CheckinTimezone != nil {
		merged.CheckinTimezone = *req.CheckinTimezone
		selectFields = append(selectFields, "checkin_timezone")
	}
	if req.CheckinWindowStart != nil {
		merged.CheckinWindowStart = *req.CheckinWindowStart
		selectFields = append(selectFields, "checkin_window_start")
	}
	if req.CheckinWindowEnd != nil {
		merged.CheckinWindowEnd = *req.CheckinWindowEnd
		selectFields = append(selectFields, "checkin_window_end")
	}
	if req.IsPinned != nil {
		merged.IsPinned = *req.IsPinned
		selectFields = append(selectFields, "is_pinned")
	}
	if req.SortOrder != nil {
		merged.SortOrder = *req.SortOrder
		selectFields = append(selectFields, "sort_order")
	}
	if req.GlobalWeight != nil {
		merged.GlobalWeight = *req.GlobalWeight
		selectFields = append(selectFields, "global_weight")
	}
	if req.CustomHeader != nil {
		merged.CustomHeader = *req.CustomHeader
		selectFields = append(selectFields, "custom_header")
	}
	if req.RouteBaseURLs != nil {
		merged.RouteBaseURLs = *req.RouteBaseURLs
		selectFields = append(selectFields, "route_base_urls")
	}
	if req.Tags != nil {
		merged.Tags = *req.Tags
		selectFields = append(selectFields, "tags")
	}
	if len(selectFields) > 0 {
		if err := merged.Validate(); err != nil {
			return nil, err
		}
		if req.LinkedSiteIDSet || merged.CheckinUseSubscriptionToken {
			if err := validateLinkedSite(&merged, ctx); err != nil {
				return nil, err
			}
		}
		if merged.ProxyMode == model.ProxyUsageModePool && merged.ProxyConfigID != nil {
			if _, err := ProxyURLForConfig(*merged.ProxyConfigID, ctx); err != nil {
				return nil, err
			}
		}
	}
	if req.Name != nil {
		updates.Name = merged.Name
	}
	if req.Platform != nil {
		updates.Platform = merged.Platform
	}
	if req.BaseURL != nil {
		updates.BaseURL = merged.BaseURL
	}
	if req.Enabled != nil {
		updates.Enabled = merged.Enabled
	}
	if req.ProxyMode != nil {
		updates.ProxyMode = merged.ProxyMode
	}
	if req.ProxyConfigIDSet || (req.ProxyMode != nil && *req.ProxyMode != model.ProxyUsageModePool) {
		updates.ProxyConfigID = merged.ProxyConfigID
	}
	if req.ExternalCheckinSet {
		updates.ExternalCheckinURL = merged.ExternalCheckinURL
	}
	if req.LinkedSiteIDSet {
		updates.LinkedSiteID = merged.LinkedSiteID
	}
	if req.CheckinHTTPEnabled != nil {
		updates.CheckinHTTPEnabled = merged.CheckinHTTPEnabled
	}
	if req.CheckinUseSubscriptionToken != nil {
		updates.CheckinUseSubscriptionToken = merged.CheckinUseSubscriptionToken
	}
	if req.CheckinMode != nil {
		updates.CheckinMode = merged.CheckinMode
	}
	if req.CheckinHTTPMethod != nil {
		updates.CheckinHTTPMethod = merged.CheckinHTTPMethod
	}
	if req.CheckinHTTPPath != nil {
		updates.CheckinHTTPPath = merged.CheckinHTTPPath
	}
	if req.CheckinHTTPBody != nil {
		updates.CheckinHTTPBody = merged.CheckinHTTPBody
	}
	if req.CheckinHTTPHeaders != nil {
		updates.CheckinHTTPHeaders = merged.CheckinHTTPHeaders
	}
	if req.CheckinRewardExtractor != nil {
		updates.CheckinRewardExtractor = merged.CheckinRewardExtractor
	}
	if req.CheckinStatusSet {
		updates.CheckinStatus = merged.CheckinStatus
	}
	if req.CheckinTimezone != nil {
		updates.CheckinTimezone = merged.CheckinTimezone
	}
	if req.CheckinWindowStart != nil {
		updates.CheckinWindowStart = merged.CheckinWindowStart
	}
	if req.CheckinWindowEnd != nil {
		updates.CheckinWindowEnd = merged.CheckinWindowEnd
	}
	if req.IsPinned != nil {
		updates.IsPinned = merged.IsPinned
	}
	if req.SortOrder != nil {
		updates.SortOrder = merged.SortOrder
	}
	if req.GlobalWeight != nil {
		updates.GlobalWeight = merged.GlobalWeight
	}
	if req.CustomHeader != nil {
		updates.CustomHeader = merged.CustomHeader
	}
	if req.RouteBaseURLs != nil {
		updates.RouteBaseURLs = merged.RouteBaseURLs
	}
	if req.Tags != nil {
		updates.Tags = merged.Tags
	}
	if len(selectFields) > 0 {
		if site.CheckinConfigFingerprint() != merged.CheckinConfigFingerprint() {
			updates.CheckinVerificationStatus = model.SiteCheckinSupportUnknown
			selectFields = append(selectFields, "checkin_verification_status", "checkin_verification_fingerprint", "checkin_verified_at")
		}
		if err := db.GetDB().WithContext(ctx).
			Model(&model.Site{}).
			Where("id = ?", req.ID).
			Select(selectFields).
			Updates(&updates).Error; err != nil {
			return nil, fmt.Errorf("failed to update site: %w", siteNameConflictError(err, merged.Name, merged.Kind, req.ID, ctx))
		}
	}
	return SiteGet(req.ID, ctx)
}

// mergeHeaders 将 upserts 合并进 existing：按 header key 大小写不敏感匹配，命中则仅
// 更新值并保留已存的原始 key 大小写，未命中则追加；随后按 deleteKeys（大小写不敏感）
// 删除，delete 在 upsert 之后执行（优先）。输出顺序稳定，空白 key 跳过。
func mergeHeaders(existing, upserts []model.CustomHeader, deleteKeys []string) []model.CustomHeader {
	order := make([]string, 0, len(existing)+len(upserts))
	byLower := make(map[string]model.CustomHeader, len(existing)+len(upserts))

	put := func(key, value string) {
		k := strings.TrimSpace(key)
		if k == "" {
			return
		}
		lk := strings.ToLower(k)
		if cur, ok := byLower[lk]; ok {
			cur.HeaderValue = value
			byLower[lk] = cur
			return
		}
		order = append(order, lk)
		byLower[lk] = model.CustomHeader{HeaderKey: k, HeaderValue: value}
	}

	for _, h := range existing {
		put(h.HeaderKey, h.HeaderValue)
	}
	for _, u := range upserts {
		put(u.HeaderKey, strings.TrimSpace(u.HeaderValue))
	}
	for _, dk := range deleteKeys {
		lk := strings.ToLower(strings.TrimSpace(dk))
		if lk == "" {
			continue
		}
		delete(byLower, lk)
	}

	out := make([]model.CustomHeader, 0, len(order))
	for _, lk := range order {
		if h, ok := byLower[lk]; ok {
			out = append(out, h)
		}
	}
	return out
}

func SiteEnabled(id int, enabled bool, ctx context.Context) error {
	return db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Site{}).Where("id = ?", id).Update("enabled", enabled).Error; err != nil {
			return err
		}
		return tx.Model(&model.SiteAccount{}).Where("site_id = ?", id).Update("enabled", enabled).Error
	})
}

func SiteDel(id int, ctx context.Context) error {
	var affectedAccountIDs []int
	if err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var accountIDs []int
		if err := tx.Model(&model.SiteAccount{}).Where("site_id = ?", id).Pluck("id", &accountIDs).Error; err != nil {
			return err
		}
		affectedAccountIDs = accountIDs
		if len(accountIDs) > 0 {
			if err := unlinkCheckinAccounts(tx, accountIDs); err != nil {
				return err
			}
			if err := tx.Model(&model.SiteAccount{}).Where("checkin_source_account_id IN ?", accountIDs).Update("checkin_source_account_id", nil).Error; err != nil {
				return err
			}
			// Delete bindings before groups/accounts so FK-constrained databases do not
			// reject removing rows that bindings may still reference.
			if err := tx.Where("site_account_id IN ?", accountIDs).Delete(&model.SiteChannelBinding{}).Error; err != nil {
				return err
			}
			if err := tx.Where("site_account_id IN ?", accountIDs).Delete(&model.SiteToken{}).Error; err != nil {
				return err
			}
			if err := tx.Where("site_account_id IN ?", accountIDs).Delete(&model.SiteUserGroup{}).Error; err != nil {
				return err
			}
			if err := tx.Where("site_account_id IN ?", accountIDs).Delete(&model.SiteModel{}).Error; err != nil {
				return err
			}
			if err := tx.Where("site_account_id IN ?", accountIDs).Delete(&model.StatsSiteModelHourly{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", accountIDs).Delete(&model.SiteAccount{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&model.Site{}).Where("linked_site_id = ?", id).Update("linked_site_id", nil).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Site{}, id).Error
	}); err != nil {
		return err
	}
	if len(affectedAccountIDs) > 0 {
		invalidateSiteBindingCache()
		deleteSiteModelHourlyCacheForAccounts(affectedAccountIDs)
	}
	return nil
}

func SiteArchive(id int, ctx context.Context) error {
	now := time.Now()
	return db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Site{}).Where("id = ?", id).Updates(map[string]any{
			"archived":    true,
			"archived_at": &now,
			"enabled":     false,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.SiteAccount{}).Where("site_id = ?", id).Update("enabled", false).Error
	})
}

func SiteRestore(id int, ctx context.Context) error {
	return db.GetDB().WithContext(ctx).Model(&model.Site{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"archived":    false,
			"archived_at": gorm.Expr("NULL"),
		}).Error
}
