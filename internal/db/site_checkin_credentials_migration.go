package db

import (
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func ensureSiteCheckinCredentialColumns(database *gorm.DB) error {
	for _, column := range []string{"Cookie", "LinkedAccountID"} {
		if !database.Migrator().HasColumn(&model.SiteAccount{}, column) {
			if err := database.Migrator().AddColumn(&model.SiteAccount{}, column); err != nil {
				return fmt.Errorf("add checkin account column %s: %w", column, err)
			}
		}
	}
	if !database.Migrator().HasIndex(&model.SiteAccount{}, "LinkedAccountID") {
		return database.Migrator().CreateIndex(&model.SiteAccount{}, "LinkedAccountID")
	}
	return nil
}

// Upgrade only legacy credential types. Explicit Cookie/account bindings, and
// deleted bindings, must remain untouched on subsequent starts or imports.
func migrateSiteCheckinCredentials(tx *gorm.DB) error {
	var sites []model.Site
	if err := tx.Where("kind = ?", model.SiteKindCheckin).Preload("Accounts").Find(&sites).Error; err != nil {
		return err
	}
	for _, site := range sites {
		for _, account := range site.Accounts {
			if account.CredentialType == model.SiteCredentialTypeCookie || account.CredentialType == model.SiteCredentialTypeLinkedAccount {
				continue
			}
			updates := map[string]any{}
			if site.CheckinHTTPEnabled {
				cookie := legacyCheckinCookie(site, account)
				if cookie == "" {
					continue
				}
				updates["cookie"], updates["credential_type"] = cookie, model.SiteCredentialTypeCookie
			} else if account.LinkedAccountID == nil && account.CheckinSourceAccountID != nil && site.LinkedSiteID != nil {
				var source model.SiteAccount
				if err := tx.Where("id = ? AND site_id = ?", *account.CheckinSourceAccountID, *site.LinkedSiteID).First(&source).Error; err != nil {
					if err == gorm.ErrRecordNotFound {
						continue
					}
					return err
				}
				var sourceSite model.Site
				if err := tx.First(&sourceSite, source.SiteID).Error; err != nil {
					return err
				}
				if sourceSite.IsCheckinOnly() || sourceSite.Platform != site.Platform || sourceSite.BaseURL != site.BaseURL ||
					(source.CredentialType != model.SiteCredentialTypeAccessToken && source.CredentialType != model.SiteCredentialTypeUsernamePassword) {
					continue
				}
				updates["linked_account_id"], updates["credential_type"] = source.ID, model.SiteCredentialTypeLinkedAccount
			}
			if len(updates) > 0 {
				if err := tx.Model(&model.SiteAccount{}).Where("id = ?", account.ID).Updates(updates).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func legacyCheckinCookie(site model.Site, account model.SiteAccount) string {
	if account.Cookie != "" {
		return account.Cookie
	}
	cookie := ""
	for _, header := range site.CustomHeader {
		if strings.EqualFold(strings.TrimSpace(header.HeaderKey), "Cookie") {
			cookie = strings.TrimSpace(header.HeaderValue)
		}
	}
	if account.CredentialType != model.SiteCredentialTypeAPIKey && strings.Contains(account.AccessToken, "=") && !strings.HasPrefix(strings.ToLower(account.AccessToken), "bearer ") {
		cookie = account.AccessToken
	}
	for _, header := range site.CheckinHTTPHeaders {
		if strings.EqualFold(strings.TrimSpace(header.HeaderKey), "Cookie") {
			cookie = strings.ReplaceAll(header.HeaderValue, "{{access_token}}", account.AccessToken)
		}
	}
	if !strings.Contains(cookie, "=") || strings.Contains(cookie, "{{") || model.ValidateSiteCheckinCookie(cookie) != nil {
		return ""
	}
	return strings.TrimSpace(cookie)
}
