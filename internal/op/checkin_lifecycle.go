package op

import (
	"context"
	"reflect"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *CheckinStore) ListSites(ctx context.Context, includeArchived bool) ([]model.CheckinSite, error) {
	query := s.conn.WithContext(ctx)
	if !includeArchived {
		query = query.Where("archived = ?", false)
	}
	sites := []model.CheckinSite{}
	err := query.Order("is_pinned DESC, sort_order ASC, id ASC").Find(&sites).Error
	return sites, err
}

func (s *CheckinStore) ListAccounts(ctx context.Context, siteID int) ([]model.CheckinAccount, error) {
	query := s.conn.WithContext(ctx)
	if siteID != 0 {
		query = query.Where("site_id = ?", siteID)
	}
	accounts := []model.CheckinAccount{}
	err := query.Order("site_id ASC, id ASC").Find(&accounts).Error
	return accounts, err
}

func (s *CheckinStore) PatchSite(ctx context.Context, id int, patch model.CheckinSitePatch) (*model.CheckinSite, error) {
	var result model.CheckinSite
	err := s.conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.CheckinSite
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, id).Error; err != nil {
			return err
		}
		fingerprint := current.ConfigFingerprint()
		result = current
		patch.Apply(&result)
		if err := result.Validate(); err != nil {
			return err
		}
		configChanged := fingerprint != result.ConfigFingerprint()
		if configChanged || (!current.Enabled && result.Enabled) {
			if err := validateCheckinSiteAccounts(tx, result); err != nil {
				return err
			}
		}
		fields := []string{"name", "platform", "base_url", "enabled", "proxy_mode", "proxy_config_id", "custom_header",
			"external_checkin_url", "checkin_mode", "checkin_http_enabled", "checkin_http_method", "checkin_http_path",
			"checkin_http_body", "checkin_http_headers", "checkin_status", "checkin_reward_extractor", "checkin_timezone",
			"checkin_window_start", "checkin_window_end", "is_pinned", "sort_order", "tags"}
		if configChanged {
			result.VerificationStatus, result.VerificationFingerprint, result.VerifiedAt = model.SiteCheckinSupportUnknown, "", nil
			fields = append(fields, "verification_status", "verification_fingerprint", "verified_at")
		}
		if err := tx.Model(&model.CheckinSite{}).Where("id = ?", id).Select(fields).Updates(&result).Error; err != nil {
			return err
		}
		if configChanged || current.Enabled != result.Enabled || current.CheckinMode != result.CheckinMode ||
			current.CheckinTimezone != result.CheckinTimezone || current.CheckinWindowStart != result.CheckinWindowStart || current.CheckinWindowEnd != result.CheckinWindowEnd {
			return clearCheckinSiteSchedule(tx, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func validateCheckinSiteAccounts(tx *gorm.DB, site model.CheckinSite) error {
	var accounts []model.CheckinAccount
	// Configuration writers first lock the site, keeping this dependency set stable.
	if err := tx.Where("site_id = ?", site.ID).Order("id ASC").Find(&accounts).Error; err != nil {
		return err
	}
	return validateCheckinAccounts(tx, site, accounts)
}

func clearCheckinSiteSchedule(tx *gorm.DB, siteID int) error {
	// Never clear pending submission state. The execution layer must recover it
	// before calculating a new submission, even after settings or identity changes.
	return tx.Model(&model.CheckinAccount{}).Where("site_id = ?", siteID).Update("next_auto_checkin_at", nil).Error
}

func (s *CheckinStore) ArchiveSite(ctx context.Context, id int) (*model.CheckinSite, error) {
	return s.setSiteArchived(ctx, id, true)
}

func (s *CheckinStore) RestoreSite(ctx context.Context, id int) (*model.CheckinSite, error) {
	return s.setSiteArchived(ctx, id, false)
}

func (s *CheckinStore) setSiteArchived(ctx context.Context, id int, archived bool) (*model.CheckinSite, error) {
	var site model.CheckinSite
	err := s.conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&site, id).Error; err != nil {
			return err
		}
		if site.Archived == archived {
			return nil
		}
		if !archived && site.Enabled {
			if err := site.Validate(); err != nil {
				return err
			}
			if err := validateCheckinSiteAccounts(tx, site); err != nil {
				return err
			}
		}
		site.Archived, site.ArchivedAt = archived, nil
		if archived {
			now := time.Now()
			site.ArchivedAt = &now
		}
		if err := tx.Model(&model.CheckinSite{}).Where("id = ?", id).
			Updates(map[string]any{"archived": site.Archived, "archived_at": site.ArchivedAt}).Error; err != nil {
			return err
		}
		return clearCheckinSiteSchedule(tx, id)
	})
	if err != nil {
		return nil, err
	}
	return &site, nil
}

// DeleteSite never cascades to accounts, credentials, channels or historical logs.
func (s *CheckinStore) DeleteSite(ctx context.Context, id int) error {
	return s.conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var site model.CheckinSite
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&site, id).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.CheckinAccount{}).Where("site_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrCheckinSiteInUse
		}
		return tx.Delete(&site).Error
	})
}

func lockCheckinAccount(tx *gorm.DB, id int) (model.CheckinSite, model.CheckinAccount, error) {
	var site model.CheckinSite
	var account model.CheckinAccount
	// site_id is immutable. Load it to acquire locks in site -> account order.
	if err := tx.Select("site_id").First(&account, id).Error; err != nil {
		return site, account, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&site, account.SiteID).Error; err != nil {
		return site, account, err
	}
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, id).Error
	return site, account, err
}

func (s *CheckinStore) PatchAccount(ctx context.Context, id int, patch model.CheckinAccountPatch) (*model.CheckinAccount, error) {
	var result model.CheckinAccount
	err := s.conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		site, current, err := lockCheckinAccount(tx, id)
		if err != nil {
			return err
		}
		result = current
		patch.Apply(&result)
		if err := result.Validate(); err != nil {
			return err
		}
		identityChanged := current.CredentialID != result.CredentialID || current.AuthMode != result.AuthMode ||
			!reflect.DeepEqual(current.CheckinNetwork, result.CheckinNetwork) || !reflect.DeepEqual(current.BalanceConfig, result.BalanceConfig)
		if identityChanged || (!current.Enabled && result.Enabled) || (result.Enabled && result.AutoCheckin) {
			if err := validateCheckinAccounts(tx, site, []model.CheckinAccount{result}); err != nil {
				return err
			}
		}
		fields := []string{"name", "credential_id", "auth_mode", "proxy_mode", "proxy_config_id", "enabled", "auto_checkin",
			"random_checkin", "checkin_random_window_minutes", "balance_config"}
		if identityChanged || current.Enabled != result.Enabled || current.AutoCheckin != result.AutoCheckin ||
			current.RandomCheckin != result.RandomCheckin || current.CheckinRandomWindowMinutes != result.CheckinRandomWindowMinutes ||
			site.Archived || !site.Enabled || !result.Enabled || !result.AutoCheckin {
			result.NextAutoCheckinAt = nil
			fields = append(fields, "next_auto_checkin_at")
		}
		// Only configuration columns are written, even when the worker updates its
		// balance/result/pending fields while the editor is open.
		return tx.Model(&model.CheckinAccount{}).Where("id = ?", id).Select(fields).Updates(&result).Error
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// A submitted unfinished workflow is retained for confirmation. Completed
// accounts can be removed without deleting logs or shared credential owners.
func (s *CheckinStore) DeleteAccount(ctx context.Context, id int) error {
	return s.conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, account, err := lockCheckinAccount(tx, id)
		if err != nil {
			return err
		}
		if account.CheckinPending != nil && account.CheckinPending.Submitted && !account.CheckinPending.Finished {
			return ErrCheckinWorkflowPending
		}
		return tx.Delete(&account).Error
	})
}

func (s *CheckinStore) GetCredentialSummary(ctx context.Context, id int) (*model.CheckinCredentialSummary, error) {
	credential, err := s.GetCredential(ctx, id)
	if err != nil {
		return nil, err
	}
	refs, err := s.CredentialReferences(ctx, id)
	if err != nil {
		return nil, err
	}
	summary := credential.Summary(len(refs.PrimaryAccountIDs), len(refs.BalanceAccountIDs))
	return &summary, nil
}

func (s *CheckinStore) ListCredentialSummaries(ctx context.Context) ([]model.CheckinCredentialSummary, error) {
	credentials, err := s.ListCredentials(ctx)
	if err != nil {
		return nil, err
	}
	var accounts []model.CheckinAccount
	if err := s.conn.WithContext(ctx).Select("credential_id", "balance_config").Find(&accounts).Error; err != nil {
		return nil, err
	}
	primary, balance := map[int]int{}, map[int]int{}
	for _, account := range accounts {
		primary[account.CredentialID]++
		if account.BalanceConfig.Mode == model.CheckinBalanceCustom && account.BalanceConfig.CredentialID != nil {
			balance[*account.BalanceConfig.CredentialID]++
		}
	}
	summaries := make([]model.CheckinCredentialSummary, 0, len(credentials))
	for _, credential := range credentials {
		summaries = append(summaries, credential.Summary(primary[credential.ID], balance[credential.ID]))
	}
	return summaries, nil
}
