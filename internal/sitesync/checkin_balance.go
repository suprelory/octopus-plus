package sitesync

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

type linkedCheckinBalance struct {
	site    *model.Site
	account *model.SiteAccount
	token   string
	before  siteBalanceFetchResult
}

// Failure to query the destination account must not prevent an external sign-in.
func prepareLinkedCheckinBalance(ctx context.Context, site *model.Site, account *model.SiteAccount) *linkedCheckinBalance {
	return resolveLinkedCheckinBalance(ctx, site, account, true)
}

func resolveLinkedCheckinBalance(ctx context.Context, site *model.Site, account *model.SiteAccount, captureBefore bool) *linkedCheckinBalance {
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var sourceSite *model.Site
	var source *model.SiteAccount
	var err error
	if site.CheckinUseSubscriptionToken {
		sourceSite, source, err = op.SiteCheckinSubscriptionTokenAccount(site, account, queryCtx)
	} else {
		sourceSite, source, err = op.SiteCheckinBalanceAccount(site, account, queryCtx)
	}
	if err != nil {
		logSiteDataWarning(site, account, "", "linked_balance", "account_unavailable", err)
		return nil
	}
	var token string
	switch sourceSite.Platform {
	case model.SitePlatformAnyRouter:
		token, err = resolveAnyRouterManagedAccessToken(queryCtx, sourceSite, source)
	case model.SitePlatformNewAPI, model.SitePlatformOneAPI, model.SitePlatformOneHub, model.SitePlatformDoneHub:
		token, err = resolveManagedAccessToken(queryCtx, sourceSite, source)
	case model.SitePlatformSub2API:
		if site.CheckinUseSubscriptionToken {
			token, err = ensureFreshSub2APIAccessToken(queryCtx, sourceSite, source, false)
		} else {
			token = resolveDirectToken(source)
		}
	default:
		return nil
	}
	if err != nil || token == "" {
		logSiteDataWarning(sourceSite, source, token, "linked_balance", "credentials_unavailable", err)
		return nil
	}
	if token != source.AccessToken && source.CredentialType == model.SiteCredentialTypeUsernamePassword {
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		saveErr := db.GetDB().WithContext(persistCtx).Model(&model.SiteAccount{}).
			Where("id = ? AND site_id = ? AND access_token = ?", source.ID, sourceSite.ID, source.AccessToken).
			Update("access_token", token).Error
		persistCancel()
		if saveErr != nil {
			logSiteDataWarning(sourceSite, source, token, "linked_balance", "token_save_failed", saveErr)
		}
	}
	balance := &linkedCheckinBalance{site: sourceSite, account: source, token: token}
	if captureBefore {
		balance.before = fetchAccountBalanceBeforeCheckin(queryCtx, sourceSite, source, token)
	}
	return balance
}

func mirrorCheckinBalance(ctx context.Context, site *model.Site, account *model.SiteAccount, balance siteBalanceFetchResult) {
	if !balance.ok {
		return
	}
	updates := map[string]any{"balance": balance.balance}
	if balance.usedKnown {
		updates["balance_used"] = balance.balanceUsed
	}
	if balance.incomeKnown {
		updates["today_income"] = balance.todayIncome
	}
	query := db.GetDB().WithContext(ctx).Model(&model.SiteAccount{}).Where("id = ?", account.ID)
	if account.LinkedAccountID != nil {
		query = query.Where("linked_account_id = ?", *account.LinkedAccountID)
	}
	if err := query.Updates(updates).Error; err != nil {
		logSiteDataWarning(site, account, "", "linked_balance", "save_failed", err)
	}
}

// Capture a fresh balance with the resolved check-in credentials. Cached account
// balances may predate unrelated spending or deposits and cannot be a baseline.
// Income-log requests are unnecessary here and would delay the actual check-in.
func fetchAccountBalanceBeforeCheckin(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string) siteBalanceFetchResult {
	snapshotCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return fetchSiteAccountBalanceResult(snapshotCtx, siteRecord, account, accessToken, firstManagedPlatformUserID(account), false)
}

// Keep the committed check-in outcome even if refreshing or saving its reward
// fails. Only fill a missing reward once, before consumers receive the result.
func fillCheckinRewardFromBalance(ctx context.Context, result *model.SiteCheckinResult, before, after siteBalanceFetchResult) {
	if result == nil || result.SkipBalanceReward || result.Status != model.SiteExecutionStatusSuccess || result.Reason != model.SiteCheckinReasonCheckedIn ||
		strings.TrimSpace(result.Reward) != "" || !before.ok || !after.ok {
		return
	}
	delta := after.balance - before.balance
	if math.IsNaN(delta) || math.IsInf(delta, 0) || delta < 0 {
		return
	}
	// Platform balances are expressed in USD. Match the six decimal places used
	// by reward statistics without exposing floating-point subtraction noise.
	reward := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(delta, 'f', 6, 64), "0"), ".")
	if reward == "-0" {
		reward = "0"
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	saved := db.GetDB().WithContext(persistCtx).Model(&model.SiteCheckinLog{}).
		Where("id = ? AND status = ? AND reason = ? AND reward = ?", result.LogID, model.SiteExecutionStatusSuccess, model.SiteCheckinReasonCheckedIn, "").
		Update("reward", reward)
	if saved.Error != nil {
		log.Warnf("could not save balance-based checkin reward for account %d: %v", result.AccountID, saved.Error)
		return
	}
	if saved.RowsAffected > 0 {
		result.Reward = reward
	}
}
