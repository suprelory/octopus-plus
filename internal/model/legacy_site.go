package model

import "time"

type LegacySiteCredentialType string

// LegacySiteKind separates subscription sites, which are synced and projected into
// channels, from check-in sites, which only run check-ins against their own URL.
type LegacySiteKind string

const (
	LegacySiteKindRelay   LegacySiteKind = "relay"
	LegacySiteKindCheckin LegacySiteKind = "checkin"
)

const (
	LegacySiteCredentialTypeUsernamePassword LegacySiteCredentialType = "username_password"
	LegacySiteCredentialTypeAccessToken      LegacySiteCredentialType = "access_token"
	LegacySiteCredentialTypeAPIKey           LegacySiteCredentialType = "api_key"
	LegacySiteCredentialTypeCookie           LegacySiteCredentialType = "cookie"
	LegacySiteCredentialTypeLinkedAccount    LegacySiteCredentialType = "linked_account"
)

type LegacySiteGroupModelSyncStatus string

// LegacySite preserves the v1 row and JSON field order used in migration checkpoints.
type LegacySite struct {
	ID                             int                      `json:"id" gorm:"primaryKey"`
	Name                           string                   `json:"name" gorm:"not null;uniqueIndex:idx_sites_kind_name,priority:2"`
	Kind                           LegacySiteKind           `json:"kind" gorm:"size:16;not null;default:'relay';uniqueIndex:idx_sites_kind_name,priority:1"`
	LinkedSiteID                   *int                     `json:"linked_site_id"`
	Platform                       SitePlatform             `json:"platform" gorm:"type:varchar(32);not null"`
	BaseURL                        string                   `json:"base_url" gorm:"not null"`
	Enabled                        bool                     `json:"enabled" gorm:"default:true"`
	ProxyMode                      ProxyUsageMode           `json:"proxy_mode" gorm:"type:varchar(16);not null;default:'direct'"`
	ProxyConfigID                  *int                     `json:"proxy_config_id"`
	ExternalCheckinURL             *string                  `json:"external_checkin_url"`
	CheckinMode                    SiteCheckinMode          `json:"checkin_mode" gorm:"size:16;not null;default:'auto'"`
	CheckinVerificationStatus      SiteCheckinSupport       `json:"checkin_verification_status" gorm:"size:16;not null;default:'unknown'"`
	CheckinVerificationFingerprint string                   `json:"checkin_verification_fingerprint" gorm:"size:64"`
	CheckinVerifiedAt              *time.Time               `json:"checkin_verified_at"`
	CheckinHTTPEnabled             bool                     `json:"checkin_http_enabled" gorm:"default:false"`
	CheckinUseSubscriptionToken    bool                     `json:"checkin_use_subscription_token" gorm:"not null;default:false"`
	CheckinHTTPMethod              string                   `json:"checkin_http_method" gorm:"size:8;not null;default:'POST'"`
	CheckinHTTPPath                string                   `json:"checkin_http_path" gorm:"type:text"`
	CheckinHTTPBody                string                   `json:"checkin_http_body" gorm:"type:text"`
	CheckinHTTPHeaders             []CustomHeader           `json:"checkin_http_headers" gorm:"type:text;serializer:json"`
	CheckinRewardExtractor         string                   `json:"checkin_reward_extractor" gorm:"type:text"`
	CheckinStatus                  *SiteCheckinStatusConfig `json:"checkin_status" gorm:"type:text;serializer:json"`
	CheckinTimezone                string                   `json:"checkin_timezone" gorm:"size:64;not null;default:'Asia/Shanghai'"`
	CheckinWindowStart             string                   `json:"checkin_window_start" gorm:"size:5;not null;default:'00:00'"`
	CheckinWindowEnd               string                   `json:"checkin_window_end" gorm:"size:5;not null;default:'23:59'"`
	IsPinned                       bool                     `json:"is_pinned" gorm:"default:false"`
	SortOrder                      int                      `json:"sort_order" gorm:"default:0"`
	GlobalWeight                   float64                  `json:"global_weight" gorm:"default:1"`
	CustomHeader                   []CustomHeader           `json:"custom_header" gorm:"serializer:json"`
	RouteBaseURLs                  []LegacySiteRouteBaseURL `json:"route_base_urls" gorm:"serializer:json"`
	DefaultRouteType               LegacySiteModelRouteType `json:"default_route_type" gorm:"type:varchar(32);not null;default:''"`
	Tags                           []string                 `json:"tags" gorm:"serializer:json"`
	Archived                       bool                     `json:"archived" gorm:"default:false;index"`
	ArchivedAt                     *time.Time               `json:"archived_at"`
	Accounts                       []LegacySiteAccount      `json:"accounts,omitempty" gorm:"foreignKey:SiteID"`
}

func (s *LegacySite) IsCheckinOnly() bool {
	return s.Kind == LegacySiteKindCheckin
}

type LegacySiteAccount struct {
	ID     int `json:"id" gorm:"primaryKey"`
	SiteID int `json:"site_id" gorm:"index;not null"`
	// Migration provenance keeps incremental legacy imports from duplicating
	// independently renamed or edited check-in accounts.
	CheckinSourceAccountID *int                     `json:"checkin_source_account_id,omitempty" gorm:"index"`
	LinkedAccountID        *int                     `json:"linked_account_id" gorm:"index"`
	Name                   string                   `json:"name" gorm:"not null"`
	CredentialType         LegacySiteCredentialType `json:"credential_type" gorm:"type:varchar(32);not null"`
	Username               string                   `json:"username"`
	Password               string                   `json:"password"`
	AccessToken            string                   `json:"access_token"`
	APIKey                 string                   `json:"api_key"`
	RefreshToken           string                   `json:"refresh_token"`
	Cookie                 string                   `json:"cookie" gorm:"type:text"`
	TokenExpiresAt         int64                    `json:"token_expires_at" gorm:"default:0"`
	CredentialVersion      int64                    `json:"credential_version" gorm:"not null;default:0"`
	// Local receipt time bounds proactive refresh for opaque or short-lived tokens.
	TokenRefreshedAt int64 `json:"-" gorm:"not null;default:0"`
	// Refresh leases are runtime coordination state and must not be exported or restored.
	TokenRefreshLease          string                     `json:"-" gorm:"size:64;not null;default:''"`
	TokenRefreshLeaseUntil     int64                      `json:"-" gorm:"not null;default:0"`
	PlatformUserID             *int                       `json:"platform_user_id"`
	ProxyMode                  ProxyUsageMode             `json:"proxy_mode" gorm:"type:varchar(16);not null;default:'inherit'"`
	ProxyConfigID              *int                       `json:"proxy_config_id"`
	Enabled                    bool                       `json:"enabled" gorm:"default:true"`
	AutoSync                   bool                       `json:"auto_sync" gorm:"default:true"`
	AutoCheckin                bool                       `json:"auto_checkin" gorm:"default:true"`
	RandomCheckin              bool                       `json:"random_checkin" gorm:"default:false"`
	CheckinRandomWindowMinutes int                        `json:"checkin_random_window_minutes" gorm:"default:120"`
	Balance                    float64                    `json:"balance" gorm:"default:0"`
	BalanceUsed                float64                    `json:"balance_used" gorm:"default:0"`
	TodayIncome                float64                    `json:"today_income" gorm:"default:0"`
	NextAutoCheckinAt          *time.Time                 `json:"next_auto_checkin_at"`
	LastSyncAt                 *time.Time                 `json:"last_sync_at"`
	LastCheckinAt              *time.Time                 `json:"last_checkin_at"`
	LastCheckinSuccessAt       *time.Time                 `json:"last_checkin_success_at"`
	CheckinFailureCount        int                        `json:"checkin_failure_count" gorm:"default:0"`
	CheckinPending             *SiteCheckinPending        `json:"checkin_pending,omitempty" gorm:"type:text;serializer:json"`
	LastSyncStatus             SiteExecutionStatus        `json:"last_sync_status" gorm:"type:varchar(16);default:'idle'"`
	LastCheckinStatus          SiteExecutionStatus        `json:"last_checkin_status" gorm:"type:varchar(16);default:'idle'"`
	LastSyncMessage            string                     `json:"last_sync_message"`
	LastCheckinMessage         string                     `json:"last_checkin_message"`
	Tokens                     []LegacySiteToken          `json:"tokens,omitempty" gorm:"foreignKey:SiteAccountID"`
	UserGroups                 []LegacySiteUserGroup      `json:"user_groups,omitempty" gorm:"foreignKey:SiteAccountID"`
	Models                     []LegacySiteModel          `json:"models,omitempty" gorm:"foreignKey:SiteAccountID"`
	ChannelBindings            []LegacySiteChannelBinding `json:"channel_bindings,omitempty" gorm:"foreignKey:SiteAccountID"`
}

type LegacySiteUserGroup struct {
	ID                      int                            `json:"id" gorm:"primaryKey"`
	SiteAccountID           int                            `json:"site_account_id" gorm:"uniqueIndex:idx_site_account_group;not null"`
	GroupKey                string                         `json:"group_key" gorm:"size:128;uniqueIndex:idx_site_account_group;not null"`
	Name                    string                         `json:"name"`
	RawPayload              string                         `json:"raw_payload"`
	ProjectionDisabled      bool                           `json:"projection_disabled" gorm:"default:false"`
	ProjectionSuspended     bool                           `json:"projection_suspended" gorm:"default:false;index"`
	ProjectionSuspendReason string                         `json:"projection_suspend_reason"`
	ProjectionSuspendedAt   *time.Time                     `json:"projection_suspended_at"`
	ModelSyncStatus         LegacySiteGroupModelSyncStatus `json:"model_sync_status" gorm:"type:varchar(32);not null;default:'idle';index"`
	ModelSyncMessage        string                         `json:"model_sync_message"`
	ModelSyncAuthoritative  bool                           `json:"model_sync_authoritative" gorm:"default:false"`
	ModelSyncModelCount     int                            `json:"model_sync_model_count" gorm:"default:0"`
	LastModelSyncAt         *time.Time                     `json:"last_model_sync_at"`
	LastModelSyncSuccessAt  *time.Time                     `json:"last_model_sync_success_at"`
	ModelSyncFailureCount   int                            `json:"model_sync_failure_count" gorm:"default:0"`
}

type LegacySiteModel struct {
	ID              int                        `json:"id" gorm:"primaryKey"`
	SiteAccountID   int                        `json:"site_account_id" gorm:"uniqueIndex:idx_site_account_group_model;not null"`
	GroupKey        string                     `json:"group_key" gorm:"size:128;uniqueIndex:idx_site_account_group_model;not null;default:'default'"`
	ModelName       string                     `json:"model_name" gorm:"size:191;uniqueIndex:idx_site_account_group_model;not null"`
	Source          string                     `json:"source"`
	RouteType       LegacySiteModelRouteType   `json:"route_type" gorm:"type:varchar(32);not null;default:'openai_chat';index"`
	RouteSource     LegacySiteModelRouteSource `json:"route_source" gorm:"type:varchar(32);not null;default:'sync_inferred'"`
	ManualOverride  bool                       `json:"manual_override" gorm:"default:false"`
	RouteRawPayload string                     `json:"route_raw_payload"`
	RouteUpdatedAt  *time.Time                 `json:"route_updated_at"`
	Disabled        bool                       `json:"disabled" gorm:"default:false;index"`
}

type LegacySiteChannelBinding struct {
	ID              int    `json:"id" gorm:"primaryKey"`
	SiteID          int    `json:"site_id" gorm:"index;not null"`
	SiteAccountID   int    `json:"site_account_id" gorm:"uniqueIndex:idx_site_account_channel_group;not null"`
	SiteUserGroupID *int   `json:"site_user_group_id"`
	GroupKey        string `json:"group_key" gorm:"size:128;uniqueIndex:idx_site_account_channel_group;not null"`
	ChannelID       int    `json:"channel_id" gorm:"uniqueIndex;not null"`
}

type LegacySiteModelRouteType string

type LegacySiteModelRouteSource string

// LegacySiteRouteBaseURL records the v1 projected channel URL override for a protocol.
type LegacySiteRouteBaseURL struct {
	RouteType LegacySiteModelRouteType `json:"route_type"`
	BaseURL   string                   `json:"base_url"`
}

type LegacySiteTokenValueStatus string

type LegacySiteToken struct {
	ID            int                        `json:"id" gorm:"primaryKey"`
	SiteAccountID int                        `json:"site_account_id" gorm:"index;not null"`
	Name          string                     `json:"name"`
	Token         string                     `json:"token" gorm:"not null"`
	ValueStatus   LegacySiteTokenValueStatus `json:"value_status" gorm:"type:varchar(32);not null;default:'ready'"`
	GroupKey      string                     `json:"group_key" gorm:"size:128;index"`
	GroupName     string                     `json:"group_name"`
	Enabled       bool                       `json:"enabled" gorm:"default:true"`
	Source        string                     `json:"source"`
	IsDefault     bool                       `json:"is_default" gorm:"default:false"`
	LastSyncAt    *time.Time                 `json:"last_sync_at"`
}

func (LegacySite) TableName() string               { return "sites" }
func (LegacySiteAccount) TableName() string        { return "site_accounts" }
func (LegacySiteToken) TableName() string          { return "site_tokens" }
func (LegacySiteUserGroup) TableName() string      { return "site_user_groups" }
func (LegacySiteModel) TableName() string          { return "site_models" }
func (LegacySiteChannelBinding) TableName() string { return "site_channel_bindings" }
