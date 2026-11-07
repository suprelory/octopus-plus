package op

import (
	"context"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

// Linked check-ins resolve credentials at execution time so account edits and
// refreshed sessions do not leave a second, stale credential copy in use.
func SiteCheckinLinkedAccount(site *model.Site, account *model.SiteAccount, ctx context.Context) (*model.SiteAccount, error) {
	if !site.IsCheckinOnly() || site.CheckinHTTPEnabled || site.LinkedSiteID == nil || account.LinkedAccountID == nil {
		return nil, fmt.Errorf("select a linked subscription site and account for platform checkin")
	}
	sourceSite, source, err := SiteCheckinBalanceAccount(site, account, ctx)
	if err != nil {
		return nil, err
	}
	if site.Platform != sourceSite.Platform || strings.TrimRight(site.BaseURL, "/") != strings.TrimRight(sourceSite.BaseURL, "/") {
		return nil, fmt.Errorf("platform checkin must use the linked subscription site's platform and URL; update the checkin site settings")
	}
	if source.CredentialType != model.SiteCredentialTypeAccessToken && source.CredentialType != model.SiteCredentialTypeUsernamePassword {
		return nil, fmt.Errorf("platform checkin requires a subscription account with an access token or username and password")
	}
	return source, nil
}

// Balance queries use the subscription site's own address, platform and proxy.
// An external check-in may run on a completely different site with its own cookie.
func SiteCheckinBalanceAccount(site *model.Site, account *model.SiteAccount, ctx context.Context) (*model.Site, *model.SiteAccount, error) {
	if site == nil || account == nil || !site.IsCheckinOnly() || site.LinkedSiteID == nil || account.LinkedAccountID == nil || *account.LinkedAccountID <= 0 {
		return nil, nil, fmt.Errorf("select a linked subscription site and account for balance queries")
	}
	var sourceSite model.Site
	if err := db.GetDB().WithContext(ctx).First(&sourceSite, *site.LinkedSiteID).Error; err != nil {
		return nil, nil, fmt.Errorf("linked subscription site is unavailable")
	}
	if sourceSite.IsCheckinOnly() {
		return nil, nil, fmt.Errorf("linked account must belong to a subscription site")
	}
	var source model.SiteAccount
	if err := db.GetDB().WithContext(ctx).Where("id = ? AND site_id = ?", *account.LinkedAccountID, sourceSite.ID).First(&source).Error; err != nil {
		return nil, nil, fmt.Errorf("linked subscription account is unavailable or belongs to another site")
	}
	if site.CheckinHTTPEnabled {
		switch sourceSite.Platform {
		case model.SitePlatformNewAPI, model.SitePlatformAnyRouter, model.SitePlatformOneAPI, model.SitePlatformOneHub, model.SitePlatformDoneHub:
			if source.CredentialType != model.SiteCredentialTypeAccessToken && source.CredentialType != model.SiteCredentialTypeUsernamePassword {
				return nil, nil, fmt.Errorf("balance queries require a subscription account with an access token or username and password")
			}
		case model.SitePlatformSub2API:
			if source.CredentialType != model.SiteCredentialTypeAccessToken && source.CredentialType != model.SiteCredentialTypeAPIKey {
				return nil, nil, fmt.Errorf("Sub2API balance queries require a subscription account with an access token or API key")
			}
		default:
			return nil, nil, fmt.Errorf("the linked subscription platform does not support balance queries")
		}
	}
	return &sourceSite, &source, nil
}

func validateSiteAccountCredentials(site *model.Site, account *model.SiteAccount, ctx context.Context) error {
	if !site.IsCheckinOnly() {
		if account.CredentialType == model.SiteCredentialTypeCookie || account.CredentialType == model.SiteCredentialTypeLinkedAccount || account.LinkedAccountID != nil {
			return fmt.Errorf("cookie and linked account credentials are only available for checkin sites")
		}
		return nil
	}
	if site.CheckinHTTPEnabled {
		if err := model.ValidateSiteCheckinCookie(account.Cookie); err != nil {
			return err
		}
		if account.LinkedAccountID != nil {
			_, _, err := SiteCheckinBalanceAccount(site, account, ctx)
			return err
		}
		return nil
	}
	if site.LinkedSiteID != nil || account.CredentialType == model.SiteCredentialTypeLinkedAccount || account.LinkedAccountID != nil {
		_, err := SiteCheckinLinkedAccount(site, account, ctx)
		return err
	}
	if account.CredentialType == model.SiteCredentialTypeCookie {
		return fmt.Errorf("configure custom HTTP checkin to use a checkin site cookie")
	}
	return nil
}

func unlinkCheckinAccounts(tx *gorm.DB, accountIDs []int) error {
	return tx.Model(&model.SiteAccount{}).Where("linked_account_id IN ?", accountIDs).Updates(map[string]any{
		"linked_account_id": nil, "next_auto_checkin_at": nil,
		"credential_type": gorm.Expr("CASE WHEN credential_type = ? THEN credential_type ELSE ? END", model.SiteCredentialTypeCookie, model.SiteCredentialTypeLinkedAccount),
	}).Error
}
