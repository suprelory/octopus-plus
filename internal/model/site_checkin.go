package model

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
type SiteCheckinLog = CheckinLog

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
