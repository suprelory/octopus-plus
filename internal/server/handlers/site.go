package handlers

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/server/router"
	sitesvc "github.com/suprelory/octopus-plus/internal/site"
	"github.com/suprelory/octopus-plus/internal/sitesync"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/utils/safe"
)

func refreshAccountCheckinScheduleBestEffort(ctx context.Context, accountID int) {
	if err := sitesvc.RefreshAccountCheckinSchedule(ctx, accountID); err != nil {
		log.Warnf("failed to refresh checkin schedule (account=%d): %v", accountID, err)
	}
}

func init() {
	router.NewGroupRouter("/api/v1/site").
		Use(middleware.Auth()).
		AddRoute(router.NewRoute("/list", http.MethodGet).Handle(listSite)).
		AddRoute(router.NewRoute("/archived", http.MethodGet).Handle(listArchivedSites)).
		AddRoute(router.NewRoute("/import/all-api-hub", http.MethodPost).Handle(importAllAPIHub)).
		AddRoute(router.NewRoute("/import/metapi", http.MethodPost).Handle(importMetAPI)).
		AddRoute(router.NewRoute("/account/sync/:id", http.MethodPost).Handle(syncSiteAccount)).
		AddRoute(router.NewRoute("/account/checkin/:id", http.MethodPost).Handle(checkinSiteAccount)).
		AddRoute(router.NewRoute("/sync-all", http.MethodPost).Handle(syncAllSiteAccounts)).
		AddRoute(router.NewRoute("/checkin-all", http.MethodPost).Handle(checkinAllSiteAccounts)).
		AddRoute(router.NewRoute("/checkin-batches", http.MethodGet).Handle(listSiteCheckinBatches)).
		AddRoute(router.NewRoute("/checkin-batches/latest", http.MethodGet).Handle(getLatestSiteCheckinBatch)).
		AddRoute(router.NewRoute("/checkin-batches/:id", http.MethodGet).Handle(getSiteCheckinBatch)).
		AddRoute(router.NewRoute("/checkin-logs", http.MethodGet).Handle(listSiteCheckinLogs)).
		AddRoute(router.NewRoute("/checkin-stats", http.MethodGet).Handle(getSiteCheckinStats)).
		AddRoute(router.NewRoute("/checkin-capabilities", http.MethodGet).Handle(getSiteCheckinCapabilities)).
		AddRoute(router.NewRoute("/last-sync-time", http.MethodGet).Handle(getSiteLastSyncTime)).
		AddRoute(router.NewRoute("/last-checkin-time", http.MethodGet).Handle(getSiteLastCheckinTime)).
		AddRoute(router.NewRoute("/:id/available-models", http.MethodGet).Handle(getSiteAvailableModels))

	router.NewGroupRouter("/api/v1/site").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(router.NewRoute("/create", http.MethodPost).Handle(createSite)).
		AddRoute(router.NewRoute("/update", http.MethodPost).Handle(updateSite)).
		AddRoute(router.NewRoute("/enable", http.MethodPost).Handle(enableSite)).
		AddRoute(router.NewRoute("/detect", http.MethodPost).Handle(detectSitePlatform)).
		AddRoute(router.NewRoute("/checkin-reward/test", http.MethodPost).Handle(testCheckinRewardExtractor)).
		AddRoute(router.NewRoute("/batch", http.MethodPost).Handle(batchSite)).
		AddRoute(router.NewRoute("/batch/edit", http.MethodPost).Handle(batchEditSite)).
		AddRoute(router.NewRoute("/account/create", http.MethodPost).Handle(createSiteAccount)).
		AddRoute(router.NewRoute("/account/update", http.MethodPost).Handle(updateSiteAccount)).
		AddRoute(router.NewRoute("/account/enable", http.MethodPost).Handle(enableSiteAccount)).
		AddRoute(router.NewRoute("/account/manual-sync/preview/:id", http.MethodPost).Handle(previewManualSiteAccountSync)).
		AddRoute(router.NewRoute("/account/manual-sync/:id", http.MethodPost).Handle(applyManualSiteAccountSync))

	router.NewGroupRouter("/api/v1/site").
		Use(middleware.Auth()).
		AddRoute(router.NewRoute("/delete/:id", http.MethodDelete).Handle(deleteSite)).
		AddRoute(router.NewRoute("/archive/:id", http.MethodPost).Handle(archiveSite)).
		AddRoute(router.NewRoute("/restore/:id", http.MethodPost).Handle(restoreSite)).
		AddRoute(router.NewRoute("/account/delete/:id", http.MethodDelete).Handle(deleteSiteAccount))
}

func listSite(c *gin.Context) {
	sites, err := op.SiteList(c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, sites)
}

func importAllAPIHub(c *gin.Context) {
	body, err := readImportPayload(c)
	if err != nil {
		resp.ErrorWithAppError(c, http.StatusBadRequest, err)
		return
	}

	result, syncAccountIDs, err := op.SiteImportAllAPIHub(c.Request.Context(), body)
	if err != nil {
		resp.ErrorWithAppError(c, http.StatusBadRequest, err)
		return
	}
	middleware.AuditFields(c, "created_sites", result.CreatedSites, "created_accounts", result.CreatedAccounts,
		"updated_accounts", result.UpdatedAccounts, "skipped_accounts", result.SkippedAccounts, "warnings", len(result.Warnings))

	if len(syncAccountIDs) > 0 {
		ids := append([]int(nil), syncAccountIDs...)
		safe.Go("site-import-sync", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			sitesvc.SyncAccountsWithOptions(ctx, ids, sitesync.SiteBatchOptions{Trigger: sitesync.SiteBatchTriggerImport})
		})
	}

	resp.Success(c, result)
}

func importMetAPI(c *gin.Context) {
	body, err := readImportPayload(c)
	if err != nil {
		resp.ErrorWithAppError(c, http.StatusBadRequest, err)
		return
	}

	result, err := op.SiteImportMetAPI(c.Request.Context(), body)
	if err != nil {
		resp.ErrorWithAppError(c, http.StatusBadRequest, err)
		return
	}
	middleware.AuditFields(c, "created_sites", result.CreatedSites, "created_accounts", result.CreatedAccounts,
		"updated_accounts", result.UpdatedAccounts, "skipped_accounts", result.SkippedAccounts,
		"imported_models", result.ImportedModels, "imported_tokens", result.ImportedTokens, "warnings", len(result.Warnings))

	resp.Success(c, result)
}

func readImportPayload(c *gin.Context) ([]byte, error) {
	contentType := c.GetHeader("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			return nil, apperror.Wrap(op.CodeSiteImportEmptyPayload, "site import empty payload", err).WithStatus(http.StatusBadRequest)
		}
		file, err := fileHeader.Open()
		if err != nil {
			return nil, apperror.Wrap(op.CodeSiteImportEmptyPayload, "site import empty payload", err).WithStatus(http.StatusBadRequest)
		}
		defer file.Close()
		return io.ReadAll(file)
	}
	return io.ReadAll(c.Request.Body)
}

func createSite(c *gin.Context) {
	var site model.Site
	if err := c.ShouldBindJSON(&site); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if err := site.Validate(); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	middleware.AuditChanges(c, 0, &site)
	if err := op.SiteCreate(&site, c.Request.Context()); err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, site)
}

func updateSite(c *gin.Context) {
	var req model.SiteUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	middleware.AuditChanges(c, req.ID, &req)
	site, err := op.SiteUpdate(&req, c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	if err := sitesvc.RefreshSiteCheckinSchedules(c.Request.Context(), site.ID); err != nil {
		log.Warnf("failed to refresh site checkin schedules (site=%d): %v", site.ID, err)
	}
	siteID := site.ID
	safe.Go("site-update-project", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := sitesvc.ProjectSite(ctx, siteID); err != nil {
			log.Warnf("background ProjectSite failed (site=%d): %v", siteID, err)
		}
	})
	resp.Success(c, site)
}

func enableSite(c *gin.Context) {
	var request struct {
		ID      int  `json:"id"`
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.InvalidJSON(c)
		return
	}
	middleware.AuditFields(c, "resource_id", request.ID, "enabled", request.Enabled)
	if err := op.SiteEnabled(request.ID, request.Enabled, c.Request.Context()); err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	if err := sitesvc.RefreshSiteCheckinSchedules(c.Request.Context(), request.ID); err != nil {
		log.Warnf("failed to refresh site checkin schedules (site=%d): %v", request.ID, err)
	}
	siteID := request.ID
	safe.Go("site-enable-project", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := sitesvc.ProjectSite(ctx, siteID); err != nil {
			log.Warnf("background ProjectSite failed (site=%d): %v", siteID, err)
		}
	})
	resp.Success(c, nil)
}

func deleteSite(c *gin.Context) {
	idNum, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.InvalidParam(c)
		return
	}
	if err := sitesvc.DeleteSite(c.Request.Context(), idNum); err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, nil)
}

func archiveSite(c *gin.Context) {
	idNum, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.InvalidParam(c)
		return
	}
	if err := sitesvc.ArchiveSite(c.Request.Context(), idNum); err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, nil)
}

func restoreSite(c *gin.Context) {
	idNum, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.InvalidParam(c)
		return
	}
	if err := sitesvc.RestoreSite(c.Request.Context(), idNum); err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	if err := sitesvc.RefreshSiteCheckinSchedules(c.Request.Context(), idNum); err != nil {
		log.Warnf("failed to refresh site checkin schedules (site=%d): %v", idNum, err)
	}
	resp.Success(c, nil)
}

func listArchivedSites(c *gin.Context) {
	sites, err := sitesvc.ListArchivedSites(c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, sites)
}

func createSiteAccount(c *gin.Context) {
	var account model.SiteAccount
	if err := c.ShouldBindJSON(&account); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if err := account.Validate(); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	middleware.AuditChanges(c, 0, &account)
	middleware.AuditFields(c, "site_id", account.SiteID)
	if err := op.SiteAccountCreate(&account, c.Request.Context()); err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	refreshAccountCheckinScheduleBestEffort(c.Request.Context(), account.ID)
	createdAccount, err := op.SiteAccountGet(account.ID, c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	if account.Enabled && account.AutoSync {
		accountID := account.ID
		safe.Go("site-account-create-sync", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			sitesvc.SyncAccountWithTrigger(ctx, accountID, "account_create")
		})
	}
	resp.Success(c, createdAccount)
}

func updateSiteAccount(c *gin.Context) {
	var req model.SiteAccountUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	middleware.AuditChanges(c, req.ID, &req)
	account, err := op.SiteAccountUpdate(&req, c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	refreshAccountCheckinScheduleBestEffort(c.Request.Context(), account.ID)
	account, err = op.SiteAccountGet(account.ID, c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	accountID := account.ID
	autoSync := account.AutoSync
	safe.Go("site-account-update-project-sync", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if _, err := sitesvc.ProjectAccount(ctx, accountID); err != nil {
			log.Warnf("background ProjectAccount failed (account=%d): %v", accountID, err)
		}
		if autoSync {
			sitesvc.SyncAccountWithTrigger(ctx, accountID, "account_update")
		}
	})
	resp.Success(c, account)
}

func enableSiteAccount(c *gin.Context) {
	var request struct {
		ID      int  `json:"id"`
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.InvalidJSON(c)
		return
	}
	middleware.AuditFields(c, "resource_id", request.ID, "enabled", request.Enabled)
	if err := op.SiteAccountEnabled(request.ID, request.Enabled, c.Request.Context()); err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	refreshAccountCheckinScheduleBestEffort(c.Request.Context(), request.ID)
	accountID := request.ID
	safe.Go("site-account-enable-project", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if _, err := sitesvc.ProjectAccount(ctx, accountID); err != nil {
			log.Warnf("background ProjectAccount failed (account=%d): %v", accountID, err)
		}
	})
	resp.Success(c, nil)
}

func deleteSiteAccount(c *gin.Context) {
	idNum, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.InvalidParam(c)
		return
	}
	if err := sitesvc.DeleteSiteAccount(c.Request.Context(), idNum); err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, nil)
}

func syncSiteAccount(c *gin.Context) {
	idNum, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.InvalidParam(c)
		return
	}
	result, err := sitesvc.SyncAccount(c.Request.Context(), idNum)
	if result != nil {
		middleware.AuditResult(c, result.Status == model.SiteExecutionStatusSuccess,
			"execution_status", string(result.Status), "site_id", result.SiteID)
	}
	if err != nil {
		if result != nil {
			resp.Success(c, result)
			return
		}
		resp.ErrorWithAppError(c, http.StatusInternalServerError, err)
		return
	}
	resp.Success(c, result)
}

func previewManualSiteAccountSync(c *gin.Context) {
	idNum, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.InvalidParam(c)
		return
	}
	var req sitesync.ManualSyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	result, err := sitesvc.PreviewManualSync(c.Request.Context(), idNum, req)
	if err != nil {
		status := http.StatusInternalServerError
		if sitesync.IsManualSyncValidationError(err) {
			status = http.StatusBadRequest
		}
		resp.ErrorWithAppError(c, status, err)
		return
	}
	resp.Success(c, result)
}

func applyManualSiteAccountSync(c *gin.Context) {
	idNum, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.InvalidParam(c)
		return
	}
	var req sitesync.ManualSyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	result, err := sitesvc.ApplyManualSync(c.Request.Context(), idNum, req)
	if err != nil {
		status := http.StatusInternalServerError
		if sitesync.IsManualSyncValidationError(err) {
			status = http.StatusBadRequest
		}
		resp.ErrorWithAppError(c, status, err)
		return
	}
	resp.Success(c, result)
}

func checkinSiteAccount(c *gin.Context) {
	idNum, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.InvalidParam(c)
		return
	}
	result, err := sitesvc.CheckinAccount(c.Request.Context(), idNum)
	if result != nil {
		middleware.AuditResult(c, result.Status != model.SiteExecutionStatusFailed,
			"execution_status", string(result.Status), "site_id", result.SiteID)
	}
	if err != nil {
		if result != nil {
			resp.Success(c, result)
			return
		}
		resp.ErrorWithAppError(c, http.StatusInternalServerError, err)
		return
	}
	resp.Success(c, result)
}

func syncAllSiteAccounts(c *gin.Context) {
	safe.Go("site-sync-all", func() {
		sitesvc.SyncAllWithOptions(context.Background(), sitesync.SiteBatchOptions{Trigger: sitesync.SiteBatchTriggerManual})
	})
	resp.Success(c, nil)
}

func checkinAllSiteAccounts(c *gin.Context) {
	job, err := sitesvc.StartCheckinBatch(c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, job)
}

func getSiteCheckinBatch(c *gin.Context) {
	taskID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || taskID <= 0 {
		resp.InvalidParam(c)
		return
	}
	job, err := sitesvc.GetCheckinBatch(c.Request.Context(), taskID)
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	if job == nil {
		resp.NotFound(c)
		return
	}
	resp.Success(c, job)
}

func getLatestSiteCheckinBatch(c *gin.Context) {
	job, err := sitesvc.LatestCheckinBatch(c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, job)
}

func listSiteCheckinBatches(c *gin.Context) {
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 || value > 100 {
			resp.InvalidParam(c)
			return
		}
		limit = value
	}
	var beforeID int64
	if raw := c.Query("before_id"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			resp.InvalidParam(c)
			return
		}
		beforeID = value
	}
	page, err := sitesvc.ListCheckinBatches(c.Request.Context(), limit, beforeID)
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, page)
}

func getSiteLastSyncTime(c *gin.Context) {
	resp.Success(c, sitesvc.LastSyncAllTime())
}

func getSiteLastCheckinTime(c *gin.Context) {
	resp.Success(c, sitesvc.LastCheckinAllTime())
}

func detectSitePlatform(c *gin.Context) {
	var request struct {
		URL string `json:"url" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.InvalidJSON(c)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	platform, defaultRouteType, err := sitesvc.DetectPlatform(ctx, request.URL)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	result := gin.H{"platform": platform}
	if defaultRouteType != "" {
		result["default_route_type"] = defaultRouteType
	}
	resp.Success(c, result)
}

func batchSite(c *gin.Context) {
	var req model.SiteBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	validActions := map[string]bool{
		"enable": true, "disable": true, "delete": true, "checkin": true,
	}
	if !validActions[req.Action] {
		resp.Error(c, http.StatusBadRequest, "invalid action")
		return
	}
	if len(req.IDs) == 0 {
		resp.Error(c, http.StatusBadRequest, "ids is required")
		return
	}

	middleware.AuditFields(c, "action", req.Action, "requested_count", len(req.IDs))
	if req.Action == "checkin" {
		job, err := sitesvc.StartSelectedCheckinBatch(c.Request.Context(), req.IDs)
		if err != nil {
			resp.InternalErrorWithLog(c, err)
			return
		}
		resp.Success(c, job)
		return
	}
	result, affected, err := op.SiteBatchApply(&req, sitesvc.DeleteSite, c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	projectSitesAsync(affected)
	auditSiteBatchResult(c, result)
	resp.Success(c, result)
}

// projectSitesAsync 在后台逐个刷新站点投影，供批量操作成功后调用。
func projectSitesAsync(ids []int) {
	for _, id := range ids {
		siteID := id
		safe.Go("site-batch-project", func() {
			projCtx, projCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer projCancel()
			if err := sitesvc.ProjectSite(projCtx, siteID); err != nil {
				log.Warnf("background ProjectSite failed (site=%d): %v", siteID, err)
			}
		})
	}
}

func batchEditSite(c *gin.Context) {
	var req model.SiteBatchEditRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if len(req.IDs) == 0 {
		resp.Error(c, http.StatusBadRequest, "ids is required")
		return
	}
	req.AddTags = model.NormalizeSiteTags(req.AddTags)
	req.RemoveTags = model.NormalizeSiteTags(req.RemoveTags)
	if err := model.ValidateSiteTags(req.AddTags); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.AddTags) == 0 && len(req.RemoveTags) == 0 &&
		len(req.Upserts) == 0 && len(req.DeleteKeys) == 0 {
		resp.Error(c, http.StatusBadRequest, "nothing to edit")
		return
	}

	middleware.AuditChanges(c, 0, &req)
	middleware.AuditFields(c, "requested_count", len(req.IDs))
	result, affected, err := op.SiteBatchEdit(&req, c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	projectSitesAsync(affected)
	auditSiteBatchResult(c, result)
	resp.Success(c, result)
}

func auditSiteBatchResult(c *gin.Context, result *model.SiteBatchResult) {
	middleware.AuditResult(c, len(result.FailedItems) == 0,
		"succeeded", len(result.SuccessIDs), "failed", len(result.FailedItems))
	if len(result.FailedItems) > 0 {
		middleware.AuditFields(c, "failed_resource_id", result.FailedItems[0].ID, "reason", result.FailedItems[0].Message)
	}
}

func getSiteAvailableModels(c *gin.Context) {
	idNum, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.InvalidParam(c)
		return
	}
	models, err := op.SiteAvailableModels(idNum, c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, gin.H{"site_id": idNum, "models": models})
}
