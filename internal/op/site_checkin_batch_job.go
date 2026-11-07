package op

import (
	"context"
	"errors"
	"fmt"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

var activeSiteCheckinBatchStatuses = []model.SiteCheckinBatchJobStatus{
	model.SiteCheckinBatchJobStatusQueued,
	model.SiteCheckinBatchJobStatusRunning,
}

func SiteCheckinBatchJobCreate(ctx context.Context, job *model.SiteCheckinBatchJob) error {
	if err := db.GetDB().WithContext(ctx).Create(job).Error; err != nil {
		return fmt.Errorf("create site checkin batch job: %w", err)
	}
	return nil
}

func SiteCheckinBatchJobUpdate(ctx context.Context, id int64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	updates["updated_at"] = gorm.Expr("CURRENT_TIMESTAMP")
	result := db.GetDB().WithContext(ctx).
		Model(&model.SiteCheckinBatchJob{}).
		Where("id = ?", id).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("update site checkin batch job progress: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func SiteCheckinBatchJobGet(ctx context.Context, id int64) (*model.SiteCheckinBatchJob, error) {
	var job model.SiteCheckinBatchJob
	err := db.GetDB().WithContext(ctx).First(&job, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get site checkin batch job: %w", err)
	}
	return &job, nil
}

func SiteCheckinBatchJobLatest(ctx context.Context) (*model.SiteCheckinBatchJob, error) {
	var job model.SiteCheckinBatchJob
	err := db.GetDB().WithContext(ctx).Order("started_at DESC, id DESC").First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get latest site checkin batch job: %w", err)
	}
	return &job, nil
}

func SiteCheckinBatchJobList(ctx context.Context, limit int, beforeID int64) (*model.SiteCheckinBatchJobPage, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	query := db.GetDB().WithContext(ctx).Order("id DESC").Limit(limit + 1)
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	page := &model.SiteCheckinBatchJobPage{Items: make([]model.SiteCheckinBatchJob, 0, limit+1)}
	if err := query.Find(&page.Items).Error; err != nil {
		return nil, fmt.Errorf("list site checkin batch jobs: %w", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextBeforeID = page.Items[limit-1].ID
	}
	return page, nil
}

func SiteCheckinBatchJobActive(ctx context.Context) (*model.SiteCheckinBatchJob, error) {
	var job model.SiteCheckinBatchJob
	err := db.GetDB().WithContext(ctx).
		Where("status IN ?", activeSiteCheckinBatchStatuses).
		Order("started_at DESC, id DESC").
		First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active site checkin batch job: %w", err)
	}
	return &job, nil
}
