package model

import "time"

// LegacyDBDump preserves the shared wire fields in their original order. Both
// backup import identities and migration checkpoints hash the encoded records.
type LegacyDBDump struct {
	Version      int       `json:"version"`
	ExportedAt   time.Time `json:"exported_at"`
	IncludeLogs  bool      `json:"include_logs"`
	IncludeStats bool      `json:"include_stats"`

	Channels            []Channel                  `json:"channels,omitempty"`
	ChannelKeys         []ChannelKey               `json:"channel_keys,omitempty"`
	ProxyConfigurations []ProxyConfiguration       `json:"proxy_configurations,omitempty"`
	Sites               []LegacySite               `json:"sites,omitempty"`
	SiteAccounts        []LegacySiteAccount        `json:"site_accounts,omitempty"`
	SiteTokens          []LegacySiteToken          `json:"site_tokens,omitempty"`
	SiteUserGroups      []LegacySiteUserGroup      `json:"site_user_groups,omitempty"`
	SiteModels          []LegacySiteModel          `json:"site_models,omitempty"`
	SiteChannelBindings []LegacySiteChannelBinding `json:"site_channel_bindings,omitempty"`
	Groups              []Group                    `json:"groups,omitempty"`
	GroupItems          []GroupItem                `json:"group_items,omitempty"`
	GroupPresets        []GroupPreset              `json:"group_presets,omitempty"`
	LLMInfos            []LLMInfo                  `json:"llm_infos,omitempty"`
	APIKeys             []APIKey                   `json:"api_keys,omitempty"`
	Settings            []Setting                  `json:"settings,omitempty"`

	StatsTotal           []StatsTotal                 `json:"stats_total,omitempty"`
	StatsDaily           []StatsDaily                 `json:"stats_daily,omitempty"`
	StatsHourly          []StatsHourly                `json:"stats_hourly,omitempty"`
	StatsModel           []StatsModel                 `json:"stats_model,omitempty"`
	StatsChannel         []StatsChannel               `json:"stats_channel,omitempty"`
	StatsAPIKey          []StatsAPIKey                `json:"stats_api_key,omitempty"`
	StatsSiteModelHourly []LegacyStatsSiteModelHourly `json:"stats_site_model_hourly,omitempty"`

	RelayLogs            []RelayLog            `json:"relay_logs,omitempty"`
	RelayRequestContents []RelayRequestContent `json:"relay_request_contents,omitempty"`
	RelayAttemptContents []RelayAttemptContent `json:"relay_attempt_contents,omitempty"`
	SiteCheckinLogs      []CheckinLog          `json:"site_checkin_logs,omitempty"`
	SiteCheckinBatchJobs []CheckinBatchJob     `json:"site_checkin_batch_jobs,omitempty"`
}

// LegacyStatsSiteModelHourly is restored into a separate archive by v2.
type LegacyStatsSiteModelHourly struct {
	Hour          int    `json:"hour" gorm:"primaryKey;autoIncrement:false;index:idx_stats_site_model_account_hour,priority:2"`
	SiteAccountID int    `json:"site_account_id" gorm:"primaryKey;index:idx_stats_site_model_lookup;index:idx_stats_site_model_account_hour,priority:1"`
	GroupKey      string `json:"group_key" gorm:"primaryKey;type:varchar(128);index:idx_stats_site_model_lookup"`
	ModelName     string `json:"model_name" gorm:"primaryKey;type:varchar(128);index:idx_stats_site_model_lookup"`
	Date          string `json:"date" gorm:"not null;type:varchar(8)"`
	LastRequestAt int64  `json:"last_request_at" gorm:"not null;default:0"`
	StatsMetrics
}

func (LegacyStatsSiteModelHourly) TableName() string { return "stats_site_model_hourlies" }
