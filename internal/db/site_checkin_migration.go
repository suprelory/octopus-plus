package db

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SeparateLegacySiteCheckins moves the check-in configuration of subscription
// sites into independent records. It is also used when restoring old backups.
// Copying credentials, remapping history and disabling the old schedule happen
// in one transaction. Cleared source configurations make subsequent runs read-only.
func SeparateLegacySiteCheckins(database *gorm.DB) error {
	return database.Transaction(func(tx *gorm.DB) error {
		var sources []model.Site
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("kind IN ?", []model.SiteKind{model.SiteKindRelay, ""}).
			Preload("Accounts").Order("id ASC").Find(&sources).Error; err != nil {
			return fmt.Errorf("load legacy check-in sites: %w", err)
		}
		if len(sources) == 0 {
			return migrateSiteCheckinCredentials(tx)
		}
		var historySiteIDs []int
		if err := tx.Model(&model.SiteCheckinLog{}).Distinct("site_id").Pluck("site_id", &historySiteIDs).Error; err != nil {
			return err
		}
		hasHistory := make(map[int]bool, len(historySiteIDs))
		for _, id := range historySiteIDs {
			hasHistory[id] = true
		}
		for _, source := range sources {
			if err := renameLegacyCheckinSites(tx, source); err != nil {
				return fmt.Errorf("migrate check-in names for site %d: %w", source.ID, err)
			}
			if !hasLegacyCheckinConfig(source, hasHistory[source.ID]) {
				continue
			}
			if err := separateSiteCheckin(tx, source); err != nil {
				return fmt.Errorf("migrate check-in config for site %d: %w", source.ID, err)
			}
		}
		return migrateSiteCheckinCredentials(tx)
	})
}

func hasLegacyCheckinConfig(site model.Site, hasHistory bool) bool {
	if hasHistory || site.CheckinHTTPEnabled || site.CheckinMode == model.SiteCheckinModeEnabled ||
		(site.ExternalCheckinURL != nil && strings.TrimSpace(*site.ExternalCheckinURL) != "") ||
		site.CheckinVerifiedAt != nil ||
		(site.CheckinVerificationStatus != "" && site.CheckinVerificationStatus != model.SiteCheckinSupportUnknown) {
		return true
	}
	for _, account := range site.Accounts {
		if account.AutoCheckin || account.LastCheckinAt != nil || account.LastCheckinSuccessAt != nil ||
			account.NextAutoCheckinAt != nil || account.CheckinFailureCount > 0 {
			return true
		}
	}
	return false
}

func separateSiteCheckin(tx *gorm.DB, source model.Site) error {
	// Incremental imports can bring new accounts or history for a site already
	// migrated. Reuse its destination without overwriting independently edited
	// credentials, settings or schedules.
	var target model.Site
	sourceAccountIDs := make([]int, 0, len(source.Accounts))
	for _, account := range source.Accounts {
		sourceAccountIDs = append(sourceAccountIDs, account.ID)
	}
	destinations := tx.Model(&model.SiteAccount{}).Select("site_id").Where("checkin_source_account_id IN ?", sourceAccountIDs)
	err := tx.Where("kind = ? AND id IN (?)", model.SiteKindCheckin, destinations).Order("id ASC").First(&target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = tx.Where("kind = ? AND linked_site_id = ? AND platform = ? AND base_url = ?",
			model.SiteKindCheckin, source.ID, source.Platform, source.BaseURL).Order("id ASC").First(&target).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		target, err = createLegacyCheckinSite(tx, source)
	}
	if err != nil {
		return err
	}
	for _, account := range source.Accounts {
		var copied model.SiteAccount
		err := tx.Where("site_id = ? AND checkin_source_account_id = ?", target.ID, account.ID).Order("id ASC").First(&copied).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			copied = account
			copied.ID = 0
			copied.SiteID = target.ID
			copied.CheckinSourceAccountID = &account.ID
			copied.AutoSync = false
			copied.LastSyncAt = nil
			copied.LastSyncStatus = model.SiteExecutionStatusIdle
			copied.LastSyncMessage = ""
			copied.Tokens, copied.UserGroups, copied.Models, copied.ChannelBindings = nil, nil, nil, nil
			if err := tx.Omit(clause.Associations).Create(&copied).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.SiteAccount{}).Where("id = ?", copied.ID).Updates(map[string]any{
				"enabled": account.Enabled, "auto_sync": false, "auto_checkin": account.AutoCheckin,
				"checkin_random_window_minutes": account.CheckinRandomWindowMinutes,
			}).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := tx.Model(&model.SiteCheckinLog{}).
			Where("site_id = ? AND account_id = ?", source.ID, account.ID).
			Updates(map[string]any{"site_id": target.ID, "account_id": copied.ID}).Error; err != nil {
			return err
		}
	}
	// Logs for deleted accounts also belong to the migrated site. Snapshot
	// names, rewards, timestamps, log IDs and batch membership remain intact.
	if err := tx.Model(&model.SiteCheckinLog{}).Where("site_id = ?", source.ID).Update("site_id", target.ID).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.SiteAccount{}).Where("site_id = ?", source.ID).Updates(map[string]any{
		"auto_checkin": false, "next_auto_checkin_at": nil,
		"last_checkin_at": nil, "last_checkin_success_at": nil,
		"last_checkin_status":  model.SiteExecutionStatusIdle,
		"last_checkin_message": "", "checkin_failure_count": 0,
	}).Error; err != nil {
		return err
	}
	return tx.Model(&model.Site{}).Where("id = ?", source.ID).Updates(map[string]any{
		"checkin_mode": model.SiteCheckinModeDisabled, "external_checkin_url": nil,
		"checkin_http_enabled": false, "checkin_http_method": "POST",
		"checkin_http_path": "", "checkin_http_body": "", "checkin_http_headers": nil,
		"checkin_verification_status":      model.SiteCheckinSupportUnknown,
		"checkin_verification_fingerprint": "", "checkin_verified_at": nil,
	}).Error
}

func createLegacyCheckinSite(tx *gorm.DB, source model.Site) (model.Site, error) {
	target := source
	target.ID = 0
	target.Kind = model.SiteKindCheckin
	target.LinkedSiteID = &source.ID
	target.Accounts = nil
	target.RouteBaseURLs = nil
	target.DefaultRouteType = ""
	target.GlobalWeight = 1
	// The external URL was only a browser link. Preserve the old automatic
	// request destination; never send copied credentials to that other host.
	var err error
	target.Name, err = availableCheckinSiteName(tx, source.Name, 0)
	if err != nil {
		return target, err
	}
	if err := tx.Omit(clause.Associations).Create(&target).Error; err != nil {
		return target, err
	}
	// GORM applies default:true when creating a struct with a false boolean.
	if err := tx.Model(&model.Site{}).Where("id = ?", target.ID).Update("enabled", source.Enabled).Error; err != nil {
		return target, err
	}
	return target, nil
}

func availableCheckinSiteName(tx *gorm.DB, baseName string, excludeID int) (string, error) {
	candidate := baseName
	for suffix := 2; ; suffix++ {
		var count int64
		if err := tx.Model(&model.Site{}).
			Where("kind = ? AND name = ? AND id <> ?", model.SiteKindCheckin, candidate, excludeID).
			Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s (%d)", baseName, suffix)
	}
}

func renameLegacyCheckinSites(tx *gorm.DB, source model.Site) error {
	var targets []model.Site
	if err := tx.Select("id", "name").Where("kind = ? AND linked_site_id = ?", model.SiteKindCheckin, source.ID).
		Order("id ASC").Find(&targets).Error; err != nil {
		return err
	}
	legacyName := source.Name + " · 签到"
	for _, target := range targets {
		if target.Name != legacyName {
			// Also recognize the numeric suffix used for old name collisions.
			suffix, ok := strings.CutPrefix(target.Name, legacyName+" (")
			if !ok {
				continue
			}
			suffix, ok = strings.CutSuffix(suffix, ")")
			number, err := strconv.Atoi(suffix)
			if !ok || err != nil || number < 2 || strconv.Itoa(number) != suffix {
				continue
			}
		}
		name, err := availableCheckinSiteName(tx, source.Name, target.ID)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.Site{}).Where("id = ? AND name = ?", target.ID, target.Name).Update("name", name).Error; err != nil {
			return err
		}
	}
	return nil
}
