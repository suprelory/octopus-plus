package model

import "time"

type SiteCheckinBatchJobStatus string

const (
	SiteCheckinBatchJobStatusQueued              SiteCheckinBatchJobStatus = "queued"
	SiteCheckinBatchJobStatusRunning             SiteCheckinBatchJobStatus = "running"
	SiteCheckinBatchJobStatusCompleted           SiteCheckinBatchJobStatus = "completed"
	SiteCheckinBatchJobStatusCompletedWithErrors SiteCheckinBatchJobStatus = "completed_with_errors"
	SiteCheckinBatchJobStatusCanceled            SiteCheckinBatchJobStatus = "canceled"
	SiteCheckinBatchJobStatusFailed              SiteCheckinBatchJobStatus = "failed"
	SiteCheckinBatchJobStatusInterrupted         SiteCheckinBatchJobStatus = "interrupted"
)

// SiteCheckinBatchJob persists a manually triggered check-in batch. An empty
// SiteIDs list denotes all eligible sites; selected batches retain their scope.
type SiteCheckinBatchJob struct {
	ID                 int64                     `json:"id,string" gorm:"primaryKey;autoIncrement:false"`
	Status             SiteCheckinBatchJobStatus `json:"status" gorm:"size:32;not null;index:idx_site_checkin_batch_status"`
	Trigger            string                    `json:"trigger" gorm:"size:16;not null"`
	SiteIDs            []int                     `json:"site_ids,omitempty" gorm:"type:text;serializer:json"`
	Total              int                       `json:"total" gorm:"not null;default:0"`
	Attempted          int                       `json:"attempted" gorm:"not null;default:0"`
	Success            int                       `json:"success" gorm:"not null;default:0"`
	Partial            int                       `json:"partial" gorm:"not null;default:0"`
	Pending            int                       `json:"pending" gorm:"not null;default:0"`
	Failed             int                       `json:"failed" gorm:"not null;default:0"`
	Skipped            int                       `json:"skipped" gorm:"not null;default:0"`
	Warnings           int                       `json:"warnings" gorm:"not null;default:0"`
	Canceled           bool                      `json:"canceled" gorm:"not null;default:false"`
	CancelReason       string                    `json:"cancel_reason,omitempty" gorm:"size:64"`
	CurrentSiteID      int                       `json:"current_site_id,omitempty" gorm:"not null;default:0"`
	CurrentSiteName    string                    `json:"current_site_name,omitempty"`
	CurrentAccountID   int                       `json:"current_account_id,omitempty" gorm:"not null;default:0"`
	CurrentAccountName string                    `json:"current_account_name,omitempty"`
	ErrorMessage       string                    `json:"error_message,omitempty"`
	DurationMs         int64                     `json:"duration_ms" gorm:"not null;default:0"`
	StartedAt          time.Time                 `json:"started_at" gorm:"index"`
	UpdatedAt          time.Time                 `json:"updated_at"`
	FinishedAt         *time.Time                `json:"finished_at,omitempty" gorm:"index"`
}

type SiteCheckinBatchJobPage struct {
	Items        []SiteCheckinBatchJob `json:"items"`
	NextBeforeID int64                 `json:"next_before_id,omitempty"`
}
