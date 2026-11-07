package site

import (
	"context"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/sitesync"
)

func SyncAccount(ctx context.Context, accountID int) (*model.SiteSyncResult, error) {
	return sitesync.SyncAccount(ctx, accountID)
}

func SyncAccountWithTrigger(ctx context.Context, accountID int, trigger string) (*model.SiteSyncResult, error) {
	return sitesync.SyncAccountWithTrigger(ctx, accountID, trigger)
}

func PreviewManualSync(ctx context.Context, accountID int, req sitesync.ManualSyncRequest) (*sitesync.ManualSyncPreview, error) {
	return sitesync.PreviewManualSync(ctx, accountID, req)
}

func ApplyManualSync(ctx context.Context, accountID int, req sitesync.ManualSyncRequest) (*sitesync.ManualSyncApplyResult, error) {
	return sitesync.ApplyManualSync(ctx, accountID, req)
}

func CheckinAccount(ctx context.Context, accountID int) (*model.SiteCheckinResult, error) {
	return sitesync.CheckinAccount(ctx, accountID)
}

func ProjectAccount(ctx context.Context, accountID int) ([]int, error) {
	return sitesync.ProjectAccount(ctx, accountID)
}

func ProjectSite(ctx context.Context, siteID int) error {
	return sitesync.ProjectSite(ctx, siteID)
}

func SyncAll(ctx context.Context) {
	sitesync.SyncAll(ctx)
}

func SyncAllWithOptions(ctx context.Context, opts sitesync.SiteBatchOptions) sitesync.SiteBatchSummary {
	return sitesync.SyncAllWithOptions(ctx, opts)
}

func SyncAccountsWithOptions(ctx context.Context, accountIDs []int, opts sitesync.SiteBatchOptions) sitesync.SiteBatchSummary {
	return sitesync.SyncAccountsWithOptions(ctx, accountIDs, opts)
}

func CheckinAll(ctx context.Context) {
	sitesync.CheckinAll(ctx)
}

func StartCheckinBatch(ctx context.Context) (*model.SiteCheckinBatchJob, error) {
	return sitesync.StartCheckinBatch(ctx)
}

func StartSelectedCheckinBatch(ctx context.Context, siteIDs []int) (*model.SiteCheckinBatchJob, error) {
	return sitesync.StartSelectedCheckinBatch(ctx, siteIDs)
}

func GetCheckinBatch(ctx context.Context, taskID int64) (*model.SiteCheckinBatchJob, error) {
	return op.SiteCheckinBatchJobGet(ctx, taskID)
}

func LatestCheckinBatch(ctx context.Context) (*model.SiteCheckinBatchJob, error) {
	return op.SiteCheckinBatchJobLatest(ctx)
}

func ListCheckinBatches(ctx context.Context, limit int, beforeID int64) (*model.SiteCheckinBatchJobPage, error) {
	return op.SiteCheckinBatchJobList(ctx, limit, beforeID)
}

func LastSyncAllTime() time.Time {
	return sitesync.LastSyncAllTime()
}

func LastCheckinAllTime() time.Time {
	return sitesync.LastCheckinAllTime()
}

func RefreshAccountCheckinSchedule(ctx context.Context, accountID int) error {
	return sitesync.RefreshAccountCheckinSchedule(ctx, accountID)
}

func RefreshSiteCheckinSchedules(ctx context.Context, siteID int) error {
	return sitesync.RefreshSiteCheckinSchedules(ctx, siteID)
}

func DeleteSite(ctx context.Context, siteID int) error {
	return sitesync.DeleteSite(ctx, siteID)
}

func ArchiveSite(ctx context.Context, siteID int) error {
	return sitesync.ArchiveSite(ctx, siteID)
}

func RestoreSite(ctx context.Context, siteID int) error {
	return sitesync.RestoreSite(ctx, siteID)
}

func ListArchivedSites(ctx context.Context) ([]model.Site, error) {
	return sitesync.ListArchivedSites(ctx)
}

func DeleteSiteAccount(ctx context.Context, accountID int) error {
	return sitesync.DeleteSiteAccount(ctx, accountID)
}

func DetectPlatform(ctx context.Context, rawURL string) (model.SitePlatform, model.SiteModelRouteType, error) {
	return sitesync.DetectPlatform(ctx, rawURL)
}

func CreateAccountToken(ctx context.Context, accountID int, req model.SiteChannelKeyCreateRequest) (*model.SiteSyncResult, error) {
	return sitesync.CreateAccountToken(ctx, accountID, req)
}
