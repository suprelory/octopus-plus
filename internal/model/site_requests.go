package model

import (
	"encoding/json"
)

type SiteUpdateRequest struct {
	ID                          int                      `json:"id" binding:"required"`
	Name                        *string                  `json:"name,omitempty"`
	Platform                    *SitePlatform            `json:"platform,omitempty"`
	BaseURL                     *string                  `json:"base_url,omitempty"`
	Enabled                     *bool                    `json:"enabled,omitempty"`
	ProxyMode                   *ProxyUsageMode          `json:"proxy_mode,omitempty"`
	ProxyConfigID               *int                     `json:"proxy_config_id,omitempty"`
	ProxyConfigIDSet            bool                     `json:"-"`
	ExternalCheckinURL          *string                  `json:"external_checkin_url,omitempty"`
	ExternalCheckinSet          bool                     `json:"-"`
	LinkedSiteID                *int                     `json:"linked_site_id,omitempty"`
	LinkedSiteIDSet             bool                     `json:"-"`
	CheckinMode                 *SiteCheckinMode         `json:"checkin_mode,omitempty"`
	CheckinHTTPEnabled          *bool                    `json:"checkin_http_enabled,omitempty"`
	CheckinUseSubscriptionToken *bool                    `json:"checkin_use_subscription_token,omitempty"`
	CheckinHTTPMethod           *string                  `json:"checkin_http_method,omitempty"`
	CheckinHTTPPath             *string                  `json:"checkin_http_path,omitempty"`
	CheckinHTTPBody             *string                  `json:"checkin_http_body,omitempty"`
	CheckinHTTPHeaders          *[]CustomHeader          `json:"checkin_http_headers,omitempty"`
	CheckinRewardExtractor      *string                  `json:"checkin_reward_extractor,omitempty"`
	CheckinStatus               *SiteCheckinStatusConfig `json:"checkin_status,omitempty"`
	CheckinStatusSet            bool                     `json:"-"`
	CheckinTimezone             *string                  `json:"checkin_timezone,omitempty"`
	CheckinWindowStart          *string                  `json:"checkin_window_start,omitempty"`
	CheckinWindowEnd            *string                  `json:"checkin_window_end,omitempty"`
	IsPinned                    *bool                    `json:"is_pinned,omitempty"`
	SortOrder                   *int                     `json:"sort_order,omitempty"`
	GlobalWeight                *float64                 `json:"global_weight,omitempty"`
	CustomHeader                *[]CustomHeader          `json:"custom_header,omitempty"`
	RouteBaseURLs               *[]SiteRouteBaseURL      `json:"route_base_urls,omitempty"`
	Tags                        *[]string                `json:"tags,omitempty"`
}

func (r *SiteUpdateRequest) UnmarshalJSON(data []byte) error {
	type alias SiteUpdateRequest
	var aux alias
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*r = SiteUpdateRequest(aux)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	_, r.ProxyConfigIDSet = raw["proxy_config_id"]
	_, r.ExternalCheckinSet = raw["external_checkin_url"]
	_, r.LinkedSiteIDSet = raw["linked_site_id"]
	_, r.CheckinStatusSet = raw["checkin_status"]
	return nil
}

type SiteAccountUpdateRequest struct {
	ID                         int                 `json:"id" binding:"required"`
	Name                       *string             `json:"name,omitempty"`
	CredentialType             *SiteCredentialType `json:"credential_type,omitempty"`
	Username                   *string             `json:"username,omitempty"`
	Password                   *string             `json:"password,omitempty"`
	AccessToken                *string             `json:"access_token,omitempty"`
	APIKey                     *string             `json:"api_key,omitempty"`
	RefreshToken               *string             `json:"refresh_token,omitempty"`
	Cookie                     *string             `json:"cookie,omitempty"`
	LinkedAccountID            *int                `json:"linked_account_id,omitempty"`
	LinkedAccountIDSet         bool                `json:"-"`
	TokenExpiresAt             *int64              `json:"token_expires_at,omitempty"`
	CredentialVersion          *int64              `json:"credential_version,omitempty"`
	PlatformUserID             *int                `json:"platform_user_id,omitempty"`
	PlatformUserIDSet          bool                `json:"-"`
	ProxyMode                  *ProxyUsageMode     `json:"proxy_mode,omitempty"`
	ProxyConfigID              *int                `json:"proxy_config_id,omitempty"`
	ProxyConfigIDSet           bool                `json:"-"`
	Enabled                    *bool               `json:"enabled,omitempty"`
	AutoSync                   *bool               `json:"auto_sync,omitempty"`
	AutoCheckin                *bool               `json:"auto_checkin,omitempty"`
	RandomCheckin              *bool               `json:"random_checkin,omitempty"`
	CheckinIntervalHours       *int                `json:"checkin_interval_hours,omitempty"`
	CheckinRandomWindowMinutes *int                `json:"checkin_random_window_minutes,omitempty"`
}

func (r *SiteAccountUpdateRequest) UnmarshalJSON(data []byte) error {
	type alias SiteAccountUpdateRequest
	var aux alias
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*r = SiteAccountUpdateRequest(aux)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	_, r.PlatformUserIDSet = raw["platform_user_id"]
	_, r.LinkedAccountIDSet = raw["linked_account_id"]
	_, r.ProxyConfigIDSet = raw["proxy_config_id"]
	return nil
}

type SiteSyncResult struct {
	AccountID       int                   `json:"account_id"`
	SiteID          int                   `json:"site_id"`
	Status          SiteExecutionStatus   `json:"status"`
	ChannelCount    int                   `json:"channel_count"`
	GroupCount      int                   `json:"group_count"`
	TokenCount      int                   `json:"token_count"`
	ModelCount      int                   `json:"model_count"`
	ManagedChannels []int                 `json:"managed_channels,omitempty"`
	Models          []string              `json:"models,omitempty"`
	GroupResults    []SiteSyncGroupResult `json:"group_results,omitempty"`
	Message         string                `json:"message"`
}

type SiteSyncGroupResult struct {
	GroupKey                string `json:"group_key"`
	GroupName               string `json:"group_name"`
	HasKey                  bool   `json:"has_key"`
	Status                  string `json:"status"`
	Authoritative           bool   `json:"authoritative"`
	ModelCount              int    `json:"model_count"`
	Message                 string `json:"message,omitempty"`
	ProjectionSuspended     bool   `json:"projection_suspended"`
	ProjectionSuspendReason string `json:"projection_suspend_reason,omitempty"`
}

type SiteCheckinResult struct {
	LogID              int64                  `json:"log_id,omitempty"`
	AccountID          int                    `json:"account_id"`
	SiteID             int                    `json:"site_id"`
	Status             SiteExecutionStatus    `json:"status"`
	Reason             string                 `json:"reason"`
	Message            string                 `json:"message"`
	Reward             string                 `json:"reward,omitempty"`
	Capability         *SiteCheckinCapability `json:"checkin_capability,omitempty"`
	CapabilityEvidence SiteCheckinSupport     `json:"-"`
	Workflow           *SiteCheckinPending    `json:"-"`
	Replayed           bool                   `json:"-"`
	SkipBalanceReward  bool                   `json:"-"`
}

type SiteBatchRequest struct {
	IDs    []int  `json:"ids" binding:"required"`
	Action string `json:"action" binding:"required"`
}

type SiteBatchResult struct {
	SuccessIDs  []int              `json:"success_ids"`
	FailedItems []SiteBatchFailure `json:"failed_items"`
}

type SiteBatchFailure struct {
	ID      int    `json:"id"`
	Message string `json:"message"`
}

// SiteBatchEditRequest 批量编辑：对一批站点统一应用标签与 custom_header 修改补丁。
// 标签先添加后移除（同名时移除优先）。
// Upserts 按 header key 大小写不敏感 upsert（命中改值并保留已存的原始 key 大小写）；
// DeleteKeys 按 key 大小写不敏感删除；同一 key 同时出现时 DeleteKeys 优先（最终删除）。
type SiteBatchEditRequest struct {
	IDs        []int          `json:"ids" binding:"required"`
	AddTags    []string       `json:"add_tags"`
	RemoveTags []string       `json:"remove_tags"`
	Upserts    []CustomHeader `json:"upserts"`
	DeleteKeys []string       `json:"delete_keys"`
}
