package op

import (
	"fmt"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

var activeCheckinBatchStatuses = []model.SiteCheckinBatchJobStatus{
	model.SiteCheckinBatchJobStatusQueued,
	model.SiteCheckinBatchJobStatusRunning,
}

func listCheckinBatchJobs(conn *gorm.DB, limit int, beforeID int64) (*model.CheckinBatchJobPage, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	query := conn.Order("id DESC").Limit(limit + 1)
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	page := &model.CheckinBatchJobPage{Items: make([]model.CheckinBatchJob, 0, limit+1)}
	if err := query.Find(&page.Items).Error; err != nil {
		return nil, fmt.Errorf("list check-in batch jobs: %w", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextBeforeID = page.Items[limit-1].ID
	}
	return page, nil
}
