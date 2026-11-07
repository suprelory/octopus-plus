package sitesync

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/utils/safe"
	"github.com/suprelory/octopus-plus/internal/utils/snowflake"
)

var (
	checkinBatchTriggerMu sync.Mutex
)

func StartCheckinBatch(ctx context.Context) (*model.SiteCheckinBatchJob, error) {
	return startCheckinBatch(ctx, nil)
}

func StartSelectedCheckinBatch(ctx context.Context, siteIDs []int) (*model.SiteCheckinBatchJob, error) {
	// Normalize a private copy so duplicate/reordered selections identify the
	// same task, and callers cannot change a running batch's scope.
	selected := slices.Clone(siteIDs)
	slices.Sort(selected)
	selected = slices.Compact(selected)
	if len(selected) == 0 || selected[0] <= 0 {
		return nil, apperror.New(CodeSiteCheckinInvalidSelection, "请选择有效的签到站点").WithStatus(http.StatusBadRequest)
	}
	var count int64
	if err := db.GetDB().WithContext(ctx).Model(&model.Site{}).
		Where("id IN ? AND kind = ? AND archived = ?", selected, model.SiteKindCheckin, false).
		Count(&count).Error; err != nil {
		return nil, fmt.Errorf("validate checkin batch sites: %w", err)
	}
	if count != int64(len(selected)) {
		return nil, apperror.New(CodeSiteCheckinInvalidSelection, "所选站点已不可用或不是签到站点，请刷新后重试").WithStatus(http.StatusBadRequest)
	}
	return startCheckinBatch(ctx, selected)
}

func startCheckinBatch(ctx context.Context, siteIDs []int) (*model.SiteCheckinBatchJob, error) {
	checkinBatchTriggerMu.Lock()
	defer checkinBatchTriggerMu.Unlock()

	active, err := op.SiteCheckinBatchJobActive(ctx)
	if err != nil {
		return nil, err
	}
	if active != nil {
		if slices.Equal(active.SiteIDs, siteIDs) {
			return active, nil
		}
		return nil, apperror.New(CodeSiteCheckinBatchActive, "已有其他范围的签到任务正在执行，请等待完成后再试").
			WithStatus(http.StatusConflict)
	}

	now := time.Now().UTC()
	job := &model.SiteCheckinBatchJob{
		ID: snowflake.GenerateID(), Status: model.SiteCheckinBatchJobStatusQueued,
		Trigger: string(SiteBatchTriggerManual), StartedAt: now, UpdatedAt: now,
		SiteIDs: slices.Clone(siteIDs),
	}
	if err := op.SiteCheckinBatchJobCreate(ctx, job); err != nil {
		return nil, err
	}

	safe.Go(fmt.Sprintf("site-checkin-batch:%d", job.ID), func() {
		runCheckinBatchJob(job.ID, slices.Clone(siteIDs))
	})
	return job, nil
}

func runCheckinBatchJob(taskID int64, siteIDs []int) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	if err := updateCheckinBatchJob(taskID, map[string]any{
		"status": model.SiteCheckinBatchJobStatusRunning,
	}); err != nil {
		log.Warnf("site checkin batch %d failed to enter running state: %v", taskID, err)
		_ = updateCheckinBatchJob(taskID, map[string]any{
			"status":        model.SiteCheckinBatchJobStatusFailed,
			"error_message": "batch could not be started",
			"finished_at":   time.Now().UTC(),
		})
		return
	}

	completed := false
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Errorf("site checkin batch %d panicked: %v", taskID, recovered)
			_ = updateCheckinBatchJob(taskID, map[string]any{
				"status":        model.SiteCheckinBatchJobStatusFailed,
				"error_message": "batch failed unexpectedly",
				"finished_at":   time.Now().UTC(),
			})
			return
		}
		if !completed {
			_ = updateCheckinBatchJob(taskID, map[string]any{
				"status":        model.SiteCheckinBatchJobStatusFailed,
				"error_message": "batch stopped before it could report a result",
				"finished_at":   time.Now().UTC(),
			})
		}
	}()

	summary := CheckinAllWithOptions(ctx, SiteBatchOptions{
		Trigger: SiteBatchTriggerManual,
		TaskID:  taskID,
		SiteIDs: siteIDs,
		OnProgress: func(progress SiteBatchProgress) {
			persistCheckinBatchProgress(progress)
		},
	})

	status := model.SiteCheckinBatchJobStatusCompleted
	if summary.Canceled {
		status = model.SiteCheckinBatchJobStatusCanceled
	} else if summary.ErrorMessage != "" {
		status = model.SiteCheckinBatchJobStatusFailed
	} else if summary.Failed > 0 || summary.Partial > 0 {
		status = model.SiteCheckinBatchJobStatusCompletedWithErrors
	}
	updates := map[string]any{
		"status":        status,
		"total":         summary.Total,
		"attempted":     summary.Attempted,
		"success":       summary.Success,
		"partial":       summary.Partial,
		"failed":        summary.Failed,
		"skipped":       summary.Skipped,
		"warnings":      summary.Warnings,
		"canceled":      summary.Canceled,
		"cancel_reason": string(summary.CancelReason),
		"duration_ms":   summary.Duration.Milliseconds(),
		"error_message": summary.ErrorMessage,
		"finished_at":   time.Now().UTC(),
	}
	if err := updateCheckinBatchJob(taskID, updates); err != nil {
		log.Warnf("site checkin batch %d final update failed: %v", taskID, err)
		return
	}
	completed = true
}

func persistCheckinBatchProgress(progress SiteBatchProgress) {
	if progress.TaskID <= 0 {
		return
	}
	updates := map[string]any{
		"total":                progress.Total,
		"attempted":            progress.Attempted,
		"success":              progress.Success,
		"partial":              progress.Partial,
		"failed":               progress.Failed,
		"skipped":              progress.Skipped,
		"warnings":             progress.Warnings,
		"canceled":             progress.Canceled,
		"cancel_reason":        string(progress.CancelReason),
		"current_site_id":      progress.CurrentSiteID,
		"current_site_name":    progress.CurrentSiteName,
		"current_account_id":   progress.CurrentAccountID,
		"current_account_name": progress.CurrentAccountName,
	}
	if err := updateCheckinBatchJob(progress.TaskID, updates); err != nil {
		log.Warnf("site checkin batch %d progress update failed: %v", progress.TaskID, err)
	}
}

func updateCheckinBatchJob(taskID int64, updates map[string]any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return op.SiteCheckinBatchJobUpdate(ctx, taskID, updates)
}
