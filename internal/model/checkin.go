package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// CheckinSite owns execution settings only. It has no subscription, model,
// token-list, group or channel associations.
type CheckinSite struct {
	ID                      int          `json:"id" gorm:"primaryKey"`
	Name                    string       `json:"name" gorm:"size:191;not null;uniqueIndex"`
	Platform                SitePlatform `json:"platform" gorm:"size:32;not null"`
	BaseURL                 string       `json:"base_url" gorm:"type:text;not null"`
	Enabled                 bool         `json:"enabled" gorm:"not null"`
	Archived                bool         `json:"archived" gorm:"not null;index"`
	ArchivedAt              *time.Time   `json:"archived_at,omitempty"`
	CheckinNetwork          `gorm:"embedded"`
	CustomHeader            []CustomHeader           `json:"custom_header" gorm:"type:text;serializer:json"`
	ExternalCheckinURL      *string                  `json:"external_checkin_url,omitempty" gorm:"type:text"`
	CheckinMode             SiteCheckinMode          `json:"checkin_mode" gorm:"size:16;not null"`
	CheckinHTTPEnabled      bool                     `json:"checkin_http_enabled" gorm:"not null"`
	CheckinHTTPMethod       string                   `json:"checkin_http_method" gorm:"size:8"`
	CheckinHTTPPath         string                   `json:"checkin_http_path" gorm:"type:text"`
	CheckinHTTPBody         string                   `json:"checkin_http_body" gorm:"type:text"`
	CheckinHTTPHeaders      []CustomHeader           `json:"checkin_http_headers" gorm:"type:text;serializer:json"`
	CheckinStatus           *SiteCheckinStatusConfig `json:"checkin_status,omitempty" gorm:"type:text;serializer:json"`
	CheckinRewardExtractor  string                   `json:"checkin_reward_extractor" gorm:"type:text"`
	CheckinTimezone         string                   `json:"checkin_timezone" gorm:"size:64;not null"`
	CheckinWindowStart      string                   `json:"checkin_window_start" gorm:"size:5;not null"`
	CheckinWindowEnd        string                   `json:"checkin_window_end" gorm:"size:5;not null"`
	VerificationStatus      SiteCheckinSupport       `json:"verification_status" gorm:"size:16"`
	VerificationFingerprint string                   `json:"-" gorm:"size:64"`
	VerifiedAt              *time.Time               `json:"verified_at,omitempty"`
	IsPinned                bool                     `json:"is_pinned"`
	SortOrder               int                      `json:"sort_order"`
	Tags                    []string                 `json:"tags" gorm:"type:text;serializer:json"`
}

type CheckinNetwork struct {
	ProxyMode     ProxyUsageMode `json:"proxy_mode" gorm:"size:16;not null"`
	ProxyConfigID *int           `json:"proxy_config_id,omitempty" gorm:"index"`
}

type CheckinAuthMode string

const (
	CheckinAuthPlatform CheckinAuthMode = "platform"
	CheckinAuthCookie   CheckinAuthMode = "cookie"
	CheckinAuthBearer   CheckinAuthMode = "bearer"
)

type CheckinBalanceMode string

const (
	CheckinBalanceDisabled CheckinBalanceMode = "disabled"
	CheckinBalancePrimary  CheckinBalanceMode = "primary"
	CheckinBalanceCustom   CheckinBalanceMode = "custom"
)

type CheckinBalanceConfig struct {
	Mode         CheckinBalanceMode `json:"mode"`
	Platform     SitePlatform       `json:"platform,omitempty"`
	BaseURL      string             `json:"base_url,omitempty"`
	CredentialID *int               `json:"credential_id,omitempty"`
	CheckinNetwork
	Headers []CustomHeader `json:"headers,omitempty"`
}

type CheckinAccount struct {
	ID                         int             `json:"id" gorm:"primaryKey"`
	SiteID                     int             `json:"site_id" gorm:"not null;uniqueIndex:idx_checkin_account_name,priority:1"`
	Name                       string          `json:"name" gorm:"size:191;not null;uniqueIndex:idx_checkin_account_name,priority:2"`
	CredentialID               int             `json:"credential_id" gorm:"not null;index"`
	AuthMode                   CheckinAuthMode `json:"auth_mode" gorm:"size:16;not null"`
	CheckinNetwork             `gorm:"embedded"`
	Enabled                    bool                 `json:"enabled" gorm:"not null"`
	AutoCheckin                bool                 `json:"auto_checkin" gorm:"not null"`
	RandomCheckin              bool                 `json:"random_checkin" gorm:"not null"`
	CheckinRandomWindowMinutes int                  `json:"checkin_random_window_minutes"`
	BalanceConfig              CheckinBalanceConfig `json:"balance_config" gorm:"type:text;serializer:json"`
	Balance                    float64              `json:"balance"`
	BalanceUsed                float64              `json:"balance_used"`
	TodayIncome                float64              `json:"today_income"`
	NextAutoCheckinAt          *time.Time           `json:"next_auto_checkin_at,omitempty" gorm:"index"`
	LastCheckinAt              *time.Time           `json:"last_checkin_at,omitempty"`
	LastCheckinSuccessAt       *time.Time           `json:"last_checkin_success_at,omitempty"`
	CheckinFailureCount        int                  `json:"checkin_failure_count"`
	LastCheckinStatus          SiteExecutionStatus  `json:"last_checkin_status" gorm:"size:16"`
	LastCheckinMessage         string               `json:"last_checkin_message" gorm:"type:text"`
	CheckinPending             *SiteCheckinPending  `json:"checkin_pending,omitempty" gorm:"type:text;serializer:json"`
}

type CheckinCredentialType string

const (
	CheckinCredentialPassword    CheckinCredentialType = "username_password"
	CheckinCredentialAccessToken CheckinCredentialType = "access_token"
	CheckinCredentialAPIKey      CheckinCredentialType = "api_key"
	CheckinCredentialCookie      CheckinCredentialType = "cookie"
)

// Secret-bearing fields cannot be serialized into ordinary API responses.
// Backup v2 will use a separate, explicit export DTO.
type CheckinCredential struct {
	ID                     int                   `json:"id" gorm:"primaryKey"`
	Name                   string                `json:"name" gorm:"size:191;not null"`
	Type                   CheckinCredentialType `json:"type" gorm:"size:32;not null"`
	Platform               SitePlatform          `json:"platform" gorm:"size:32;not null"`
	AuthBaseURL            string                `json:"auth_base_url" gorm:"type:text;not null"`
	CheckinNetwork         `gorm:"embedded"`
	AuthHeaders            []CustomHeader `json:"-" gorm:"type:text;serializer:json"`
	Username               string         `json:"username"`
	Password               string         `json:"-" gorm:"type:text"`
	AccessToken            string         `json:"-" gorm:"type:text"`
	APIKey                 string         `json:"-" gorm:"type:text"`
	RefreshToken           string         `json:"-" gorm:"type:text"`
	Cookie                 string         `json:"-" gorm:"type:text"`
	PlatformUserID         *int           `json:"platform_user_id,omitempty"`
	TokenExpiresAt         int64          `json:"token_expires_at"`
	TokenRefreshedAt       int64          `json:"-"`
	CredentialVersion      int64          `json:"credential_version" gorm:"not null"`
	IdentityRevision       int64          `json:"identity_revision" gorm:"not null"`
	TokenRefreshLease      string         `json:"-" gorm:"size:64;not null"`
	TokenRefreshLeaseUntil int64          `json:"-" gorm:"not null"`
}

// ConfigFingerprint excludes cosmetic/availability settings and login tokens.
func (s CheckinSite) ConfigFingerprint() string {
	return checkinFingerprint([]any{s.Platform, s.BaseURL, s.CheckinNetwork, s.CustomHeader,
		s.CheckinHTTPEnabled, s.CheckinHTTPMethod, s.CheckinHTTPPath, s.CheckinHTTPBody,
		s.CheckinHTTPHeaders, s.CheckinStatus, s.CheckinRewardExtractor})
}

func (s CheckinSite) WorkflowFingerprint(account CheckinAccount, credential CheckinCredential) string {
	network := account.CheckinNetwork
	if network.ProxyMode == "" || network.ProxyMode == ProxyUsageModeInherit {
		network = s.CheckinNetwork
	}
	return checkinFingerprint([]any{s.ConfigFingerprint(), s.CheckinTimezone, account.AuthMode,
		account.CredentialID, credential.ID, credential.IdentityRevision, network})
}

func checkinFingerprint(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
