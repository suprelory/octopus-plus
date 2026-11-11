package model

import "time"

const (
	SiteCheckinReasonCheckedIn        = "checked_in"
	SiteCheckinReasonAlreadyCheckedIn = "already_checked_in"
	SiteCheckinReasonAlreadyRunning   = "already_running"
	SiteCheckinReasonUnsupported      = "unsupported_checkin"
	SiteCheckinReasonDisabled         = "checkin_disabled"
	SiteCheckinReasonDefaultDisabled  = "checkin_default_disabled"
	SiteCheckinReasonNotConfigured    = "checkin_not_configured"
	SiteCheckinReasonSiteDisabled     = "site_disabled"
	SiteCheckinReasonAccountDisabled  = "account_disabled"
	SiteCheckinReasonSiteArchived     = "site_archived"
	SiteCheckinReasonUnconfirmed      = "unconfirmed_checkin"
)

// SiteCheckinLog stores one outcome per execution, separate from the account's
// latest status. A missing reward may be filled once after the balance refresh.
// Names are snapshots so history remains readable after account edits.
// No credential or upstream response body belongs in this table.
type SiteCheckinLog struct {
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

type SiteCheckinLogPage struct {
	Items        []SiteCheckinLog `json:"items"`
	NextBeforeID int64            `json:"next_before_id,omitempty"`
}

// SiteCheckinStats is an aggregate view over persisted check-in logs. Reward
// values are stored as strings for compatibility with upstream responses, so
// invalid values are reported separately instead of being silently counted.
type SiteCheckinStats struct {
	TodayReward        float64                `json:"today_reward"`
	Recent7DaysReward  float64                `json:"recent_7_days_reward"`
	Recent30DaysReward float64                `json:"recent_30_days_reward"`
	TotalReward        float64                `json:"total_reward"`
	TotalCount         int                    `json:"total_count"`
	SuccessCount       int                    `json:"success_count"`
	FailedCount        int                    `json:"failed_count"`
	SkippedCount       int                    `json:"skipped_count"`
	PendingCount       int                    `json:"pending_count"`
	InvalidRewardCount int                    `json:"invalid_reward_count"`
	UnknownRewardCount int                    `json:"unknown_reward_count"`
	Timezone           string                 `json:"timezone"`
	BySite             []SiteCheckinSiteStats `json:"by_site"`
}

type SiteCheckinSiteStats struct {
	SiteID       int     `json:"site_id"`
	SiteName     string  `json:"site_name"`
	Reward       float64 `json:"reward"`
	TotalCount   int     `json:"total_count"`
	SuccessCount int     `json:"success_count"`
	FailedCount  int     `json:"failed_count"`
	SkippedCount int     `json:"skipped_count"`
	PendingCount int     `json:"pending_count"`
}
