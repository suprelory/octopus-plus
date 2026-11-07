package sitesync

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

type syncSnapshot struct {
	accessToken  string
	groups       []model.SiteUserGroup
	tokens       []model.SiteToken
	models       []model.SiteModel
	groupResults []siteGroupSyncResult
	status       model.SiteExecutionStatus
	balance      float64
	balanceUsed  float64
	todayIncome  float64
	message      string
}

type siteBatchAccount struct {
	site    *model.Site
	account *model.SiteAccount
}

func snapshotAccessToken(snapshot *syncSnapshot) string {
	if snapshot == nil {
		return ""
	}
	return snapshot.accessToken
}

func markAccountSyncFailure(ctx context.Context, accountID int, syncErr error, accessToken string) {
	if syncErr == nil {
		return
	}
	message := sanitizeSiteStatusMessage(syncErr)
	if message == "" {
		message = "同步失败"
	}
	if updateErr := updateAccountSyncState(ctx, accountID, model.SiteExecutionStatusFailed, message, accessToken); updateErr != nil {
		log.Warnf("failed to update site account sync state (account=%d): %v", accountID, updateErr)
	}
}

func SyncAccount(ctx context.Context, accountID int) (*model.SiteSyncResult, error) {
	return SyncAccountWithTrigger(ctx, accountID, "manual")
}

func runAccountSync(ctx context.Context, accountID int, diagnostic *syncLogContext) (*model.SiteSyncResult, error) {
	siteRecord, account, err := loadSyncableSiteAccount(ctx, accountID)
	if diagnostic != nil {
		diagnostic.site, diagnostic.account = siteRecord, account
	}
	if err != nil {
		return nil, sanitizeSiteError(err)
	}

	snapshot, syncErr := syncAccountState(ctx, siteRecord, account)
	if diagnostic != nil {
		diagnostic.snapshot = snapshot
	}
	if snapshot == nil && syncErr != nil {
		message := sanitizeSiteStatusMessage(syncErr)
		markAccountSyncFailure(ctx, account.ID, syncErr, "")
		if staleErr := MarkAccountProjectionStale(ctx, account.ID, message); staleErr != nil {
			log.Warnf("failed to mark site account projection stale (account=%d): %v", account.ID, staleErr)
		}
		return nil, sanitizeSiteError(syncErr)
	}

	if err := persistSyncSnapshot(ctx, account.ID, snapshot); err != nil {
		markAccountSyncFailure(ctx, account.ID, err, snapshotAccessToken(snapshot))
		return nil, sanitizeSiteError(err)
	}

	channelIDs, err := ProjectAccount(ctx, account.ID)
	if err != nil {
		markAccountSyncFailure(ctx, account.ID, err, snapshotAccessToken(snapshot))
		return nil, sanitizeSiteError(err)
	}

	modelNames := make([]string, 0, len(snapshot.models))
	for _, item := range snapshot.models {
		modelNames = append(modelNames, item.ModelName)
	}
	slices.Sort(modelNames)

	result := &model.SiteSyncResult{
		AccountID:       account.ID,
		SiteID:          siteRecord.ID,
		Status:          snapshot.status,
		ChannelCount:    len(channelIDs),
		GroupCount:      len(snapshot.groups),
		TokenCount:      len(snapshot.tokens),
		ModelCount:      len(snapshot.models),
		ManagedChannels: channelIDs,
		Models:          modelNames,
		GroupResults:    exportSiteSyncGroupResults(snapshot.groupResults),
		Message:         sanitizeSiteStatusText(snapshot.message),
	}
	if syncErr != nil {
		return result, sanitizeSiteError(syncErr)
	}
	return result, nil
}

// 全量同步/签到的上次执行时间（含定时与手动触发），仅内存记录，重启后清零
var (
	lastBatchTimeMu    sync.RWMutex
	lastSyncAllTime    time.Time
	lastCheckinAllTime time.Time
)

func markLastSyncAllTime() {
	lastBatchTimeMu.Lock()
	lastSyncAllTime = time.Now()
	lastBatchTimeMu.Unlock()
}

func markLastCheckinAllTime() {
	lastBatchTimeMu.Lock()
	lastCheckinAllTime = time.Now()
	lastBatchTimeMu.Unlock()
}

func LastSyncAllTime() time.Time {
	lastBatchTimeMu.RLock()
	defer lastBatchTimeMu.RUnlock()
	return lastSyncAllTime
}

func LastCheckinAllTime() time.Time {
	lastBatchTimeMu.RLock()
	defer lastBatchTimeMu.RUnlock()
	return lastCheckinAllTime
}

func SyncAll(ctx context.Context) {
	SyncAllWithOptions(ctx, SiteBatchOptions{Trigger: SiteBatchTriggerScheduled})
}

func SyncAllWithOptions(ctx context.Context, opts SiteBatchOptions) SiteBatchSummary {
	trigger := normalizedSiteBatchTrigger(opts.Trigger)
	sites, err := op.SiteList(ctx)
	if err != nil {
		log.Warnw("sitesync.sync.list_failed", "trigger", string(trigger), "reason", string(siteBatchReason(err)), "message", sanitizeSiteStatusMessage(err))
		return SiteBatchSummary{Phase: SiteBatchPhaseSync, Trigger: trigger}
	}
	defer markLastSyncAllTime()
	return syncBatchAccounts(ctx, eligibleSyncAccounts(sites), opts)
}

func SyncAccountsWithOptions(ctx context.Context, accountIDs []int, opts SiteBatchOptions) SiteBatchSummary {
	items := make([]siteBatchAccount, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		siteRecord, account, err := loadSiteAccount(ctx, accountID)
		if err != nil {
			log.Warnw("sitesync.sync.account_load_failed", "account_id", accountID, "trigger", string(opts.Trigger), "error", log.SafeError(sanitizeSiteError(err)))
			continue
		}
		if siteRecord == nil || account == nil || !siteRecord.Enabled || !account.Enabled || siteRecord.IsCheckinOnly() {
			continue
		}
		items = append(items, siteBatchAccount{site: siteRecord, account: account})
	}
	return syncBatchAccounts(ctx, items, opts)
}

func syncBatchAccounts(ctx context.Context, items []siteBatchAccount, opts SiteBatchOptions) SiteBatchSummary {
	// Site batch logging intentionally aggregates account-level business failures.
	// Individual account messages are stored on the account status; console logs
	// stay aggregated to avoid leaking upstream HTML and overwhelming operators.
	summary := newSiteBatchSummary(SiteBatchPhaseSync, opts, len(items))
	summary.emitProgress()
	defer summary.emitLog()
	for i := 0; i < len(items); i++ {
		item := items[i]
		summary.setCurrent(item)
		if !waitSiteBatchInterval(ctx, 500*time.Millisecond) {
			summary.markCanceled(ctx.Err())
			recordBatchCanceledSkips(summary, items[i:])
			return *summary
		}
		result, err := runAccountSync(ctx, item.account.ID, nil)
		summary.clearCurrent()
		if err != nil {
			summary.recordFailure(item.site.ID, item.site.Platform, item.account.ID, err)
			if IsCloudflareProtectionError(err) || siteBatchReason(err) == SiteBatchReasonCloudflareProtection {
				i = recordCloudflareSkipsAndWait(ctx, summary, items, i, CloudflareRetryAfter(err))
			}
			continue
		}
		summary.recordResult(item.site.ID, item.site.Platform, item.account.ID, result.Status, result.Message)
	}
	return *summary
}

func CheckinAll(ctx context.Context) {
	CheckinAllWithOptions(ctx, SiteBatchOptions{Trigger: SiteBatchTriggerScheduled})
}

func CheckinAllWithOptions(ctx context.Context, opts SiteBatchOptions) SiteBatchSummary {
	trigger := normalizedSiteBatchTrigger(opts.Trigger)
	if opts.TaskID > 0 {
		ctx = withSiteBatchTaskID(ctx, opts.TaskID)
	}
	sites, err := op.SiteList(ctx)
	if err != nil {
		message := sanitizeSiteStatusMessage(err)
		log.Warnw("sitesync.checkin.list_failed", "trigger", string(trigger), "reason", string(siteBatchReason(err)), "message", message)
		return SiteBatchSummary{Phase: SiteBatchPhaseCheckin, Trigger: trigger, ErrorMessage: message}
	}
	if len(opts.SiteIDs) == 0 {
		defer markLastCheckinAllTime()
	} else {
		selected := make(map[int]struct{}, len(opts.SiteIDs))
		for _, siteID := range opts.SiteIDs {
			selected[siteID] = struct{}{}
		}
		sites = slices.DeleteFunc(sites, func(site model.Site) bool {
			_, included := selected[site.ID]
			return !included
		})
	}
	items := eligibleCheckinAccounts(sites)
	summary := newSiteBatchSummary(SiteBatchPhaseCheckin, opts, len(items))
	summary.emitProgress()
	defer summary.emitLog()
	now := time.Now()
	for i := 0; i < len(items); i++ {
		item := items[i]
		summary.setCurrent(item)
		summary.emitProgress()
		if trigger == SiteBatchTriggerScheduled {
			nextAt, scheduleErr := ensureAccountCheckinSchedule(ctx, item.site, item.account, now)
			if scheduleErr != nil {
				summary.clearCurrent()
				summary.emitProgress()
				summary.recordFailure(item.site.ID, item.site.Platform, item.account.ID, sanitizeSiteError(scheduleErr))
				continue
			}
			if nextAt == nil || now.Before(*nextAt) {
				summary.clearCurrent()
				summary.emitProgress()
				summary.recordSkip(item.site.ID, item.site.Platform, SiteBatchReasonScheduledLater, 1)
				continue
			}
		}
		if !waitSiteBatchInterval(ctx, 500*time.Millisecond) {
			summary.clearCurrent()
			summary.markCanceled(ctx.Err())
			recordBatchCanceledSkips(summary, items[i:])
			return *summary
		}
		result, err := checkinAccountWithTrigger(ctx, item.account.ID, trigger)
		summary.clearCurrent()
		if err != nil {
			summary.recordFailure(item.site.ID, item.site.Platform, item.account.ID, err)
			if IsCloudflareProtectionError(err) || siteBatchReason(err) == SiteBatchReasonCloudflareProtection {
				i = recordCloudflareSkipsAndWait(ctx, summary, items, i, CloudflareRetryAfter(err))
			}
			continue
		}
		if result.Status == model.SiteExecutionStatusSkipped {
			summary.recordSkip(item.site.ID, item.site.Platform, SiteBatchReason(result.Reason), 1)
			continue
		}
		summary.recordResult(item.site.ID, item.site.Platform, item.account.ID, result.Status, result.Message)
	}
	return *summary
}

func eligibleSyncAccounts(sites []model.Site) []siteBatchAccount {
	items := make([]siteBatchAccount, 0)
	for siteIndex := range sites {
		siteRecord := &sites[siteIndex]
		if !siteRecord.Enabled || siteRecord.IsCheckinOnly() {
			continue
		}
		for accountIndex := range siteRecord.Accounts {
			account := &siteRecord.Accounts[accountIndex]
			if !account.Enabled || !account.AutoSync {
				continue
			}
			items = append(items, siteBatchAccount{site: siteRecord, account: account})
		}
	}
	return items
}

func eligibleCheckinAccounts(sites []model.Site) []siteBatchAccount {
	items := make([]siteBatchAccount, 0)
	for siteIndex := range sites {
		siteRecord := &sites[siteIndex]
		if !siteRecord.Enabled || siteRecord.Archived || !siteRecord.IsCheckinOnly() || !siteRecord.ResolveCheckinCapability().Enabled {
			continue
		}
		for accountIndex := range siteRecord.Accounts {
			account := &siteRecord.Accounts[accountIndex]
			if !account.Enabled || !account.AutoCheckin {
				continue
			}
			items = append(items, siteBatchAccount{site: siteRecord, account: account})
		}
	}
	return items
}

func recordCloudflareSkipsAndWait(ctx context.Context, summary *SiteBatchSummary, items []siteBatchAccount, currentIndex int, retryAfter time.Duration) int {
	current := items[currentIndex]
	lastSkipped := currentIndex
	for j := currentIndex + 1; j < len(items); j++ {
		if items[j].site.ID != current.site.ID {
			break
		}
		summary.recordSkip(items[j].site.ID, items[j].site.Platform, SiteBatchReasonCloudflareProtection, 1)
		lastSkipped = j
	}
	waitSiteCloudflareRetryAfter(ctx, retryAfter)
	return lastSkipped
}

func recordBatchCanceledSkips(summary *SiteBatchSummary, items []siteBatchAccount) {
	for _, item := range items {
		summary.recordSkip(item.site.ID, item.site.Platform, SiteBatchReasonBatchCanceled, 1)
	}
}

func waitSiteBatchInterval(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func waitSiteCloudflareRetryAfter(ctx context.Context, retryAfter time.Duration) {
	waitSiteBatchInterval(ctx, retryAfter)
}

func DeleteSite(ctx context.Context, siteID int) error {
	siteRecord, err := op.SiteGet(siteID, ctx)
	if err != nil {
		return err
	}
	for _, account := range siteRecord.Accounts {
		if err := deleteManagedChannelsByAccount(ctx, account.ID); err != nil {
			return err
		}
	}
	return op.SiteDel(siteID, ctx)
}

func ArchiveSite(ctx context.Context, siteID int) error {
	siteRecord, err := op.SiteGet(siteID, ctx)
	if err != nil {
		return err
	}
	for _, account := range siteRecord.Accounts {
		if err := deleteManagedChannelsByAccount(ctx, account.ID); err != nil {
			return err
		}
	}
	return op.SiteArchive(siteID, ctx)
}

func RestoreSite(ctx context.Context, siteID int) error {
	return op.SiteRestore(siteID, ctx)
}

func ListArchivedSites(ctx context.Context) ([]model.Site, error) {
	return op.SiteListArchived(ctx)
}

func DeleteSiteAccount(ctx context.Context, accountID int) error {
	if err := deleteManagedChannelsByAccount(ctx, accountID); err != nil {
		return err
	}
	return op.SiteAccountDel(accountID, ctx)
}
