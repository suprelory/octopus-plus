package model

import "time"

type CheckinTrigger string

const (
	CheckinTriggerManual    CheckinTrigger = "manual"
	CheckinTriggerScheduled CheckinTrigger = "scheduled"
	CheckinTriggerBatch     CheckinTrigger = "batch"
)

// CheckinLog reuses the existing history table. Its names are snapshots; deleting
// configuration must not remove history. Never persist response bodies or secrets.
type CheckinLog struct {
	ID          int64               `json:"id" gorm:"primaryKey;autoIncrement:false;index:idx_site_checkin_site_id,priority:2,sort:desc;index:idx_site_checkin_account_id,priority:2,sort:desc"`
	SiteID      int                 `json:"site_id" gorm:"index:idx_site_checkin_site_id,priority:1;not null"`
	AccountID   int                 `json:"account_id" gorm:"index:idx_site_checkin_account_id,priority:1;not null"`
	SiteName    string              `json:"site_name"`
	AccountName string              `json:"account_name"`
	Platform    SitePlatform        `json:"platform" gorm:"size:32;not null"`
	Source      string              `json:"source" gorm:"size:16;not null"`
	Status      SiteExecutionStatus `json:"status" gorm:"size:16;not null"`
	Reason      string              `json:"reason" gorm:"size:64;not null"`
	Message     string              `json:"message"`
	Reward      string              `json:"reward"`
	BatchJobID  int64               `json:"batch_job_id,omitempty" gorm:"index:idx_site_checkin_batch_job_id"`
	DurationMs  int64               `json:"duration_ms"`
	StartedAt   time.Time           `json:"started_at"`
	FinishedAt  time.Time           `json:"finished_at" gorm:"index"`
}

func (CheckinLog) TableName() string { return "site_checkin_logs" }

type CheckinOutcome struct {
	Status  SiteExecutionStatus `json:"status"`
	Reason  string              `json:"reason"`
	Message string              `json:"message"`
	Reward  string              `json:"reward,omitempty"`
}

type CheckinResult struct {
	CheckinOutcome
	LogID     int64  `json:"log_id,string"`
	SiteID    int    `json:"site_id"`
	AccountID int    `json:"account_id"`
	Date      string `json:"date"`
	Submitted bool   `json:"submitted"`
	Replayed  bool   `json:"replayed"`
}
