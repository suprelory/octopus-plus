package sitesync

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
)

const (
	ManualSyncModeReplace = "replace"

	ManualSyncFormatResponses = "responses"
	ManualSyncFormatSnapshot  = "snapshot"

	manualSyncSource = "manual_import"
)

type ManualSyncRequest struct {
	Mode               string                    `json:"mode"`
	Format             string                    `json:"format"`
	TokenResponse      json.RawMessage           `json:"token_response,omitempty"`
	GroupResponses     []json.RawMessage         `json:"group_responses,omitempty"`
	ModelResponses     []ManualSyncModelResponse `json:"model_responses,omitempty"`
	AccountResponse    json.RawMessage           `json:"account_response,omitempty"`
	Snapshot           *ManualSyncSnapshotInput  `json:"snapshot,omitempty"`
	PreviewFingerprint string                    `json:"preview_fingerprint,omitempty"`
}

type ManualSyncModelResponse struct {
	GroupKey string          `json:"group_key"`
	Response json.RawMessage `json:"response"`
}

type ManualSyncSnapshotInput struct {
	AccessToken *string                            `json:"access_token,omitempty"`
	Tokens      *[]ManualSyncTokenInput            `json:"tokens,omitempty"`
	Groups      *[]ManualSyncGroupInput            `json:"groups,omitempty"`
	Models      *map[string][]ManualSyncModelInput `json:"models,omitempty"`
	Balance     *float64                           `json:"balance,omitempty"`
	BalanceUsed *float64                           `json:"balance_used,omitempty"`
	TodayIncome *float64                           `json:"today_income,omitempty"`
}

type ManualSyncTokenInput struct {
	Name      string `json:"name"`
	Token     string `json:"token"`
	GroupKey  string `json:"group_key"`
	GroupName string `json:"group_name"`
	Enabled   *bool  `json:"enabled,omitempty"`
	IsDefault *bool  `json:"is_default,omitempty"`
}

type ManualSyncGroupInput struct {
	GroupKey string `json:"group_key"`
	Name     string `json:"name"`
}

type ManualSyncModelInput struct {
	ModelName string                   `json:"model_name"`
	RouteType model.SiteModelRouteType `json:"route_type,omitempty"`
}

func (m *ManualSyncModelInput) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		m.ModelName = strings.TrimSpace(name)
		m.RouteType = ""
		return nil
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.ModelName = firstNonEmptyString(
		jsonString(raw["model_name"]),
		jsonString(raw["modelName"]),
		jsonString(raw["model"]),
		jsonString(raw["id"]),
		jsonString(raw["name"]),
	)
	m.RouteType = model.SiteModelRouteType(firstNonEmptyString(
		jsonString(raw["route_type"]),
		jsonString(raw["routeType"]),
	))
	return nil
}

type ManualSyncPreview struct {
	AccountID            int                      `json:"account_id"`
	SiteID               int                      `json:"site_id"`
	Mode                 string                   `json:"mode"`
	Format               string                   `json:"format"`
	ImportedTokenCount   int                      `json:"imported_token_count"`
	ImportedGroupCount   int                      `json:"imported_group_count"`
	ImportedModelCount   int                      `json:"imported_model_count"`
	TokenCount           int                      `json:"token_count"`
	UsableTokenCount     int                      `json:"usable_token_count"`
	MaskedTokenCount     int                      `json:"masked_token_count"`
	GroupCount           int                      `json:"group_count"`
	ModelCount           int                      `json:"model_count"`
	ChannelCountEstimate int                      `json:"channel_count_estimate"`
	BalanceProvided      bool                     `json:"balance_provided"`
	Balance              float64                  `json:"balance"`
	BalanceUsedProvided  bool                     `json:"balance_used_provided"`
	BalanceUsed          float64                  `json:"balance_used"`
	TodayIncomeProvided  bool                     `json:"today_income_provided"`
	TodayIncome          float64                  `json:"today_income"`
	Groups               []ManualSyncPreviewGroup `json:"groups"`
	Warnings             []string                 `json:"warnings"`
	CanApply             bool                     `json:"can_apply"`
	PreviewFingerprint   string                   `json:"preview_fingerprint"`
}

type ManualSyncPreviewGroup struct {
	GroupKey         string   `json:"group_key"`
	GroupName        string   `json:"group_name"`
	TokenCount       int      `json:"token_count"`
	UsableTokenCount int      `json:"usable_token_count"`
	MaskedTokenCount int      `json:"masked_token_count"`
	ModelCount       int      `json:"model_count"`
	ModelAction      string   `json:"model_action"`
	RouteTypes       []string `json:"route_types"`
	WillProject      bool     `json:"will_project"`
}

type ManualSyncApplyResult struct {
	Preview    ManualSyncPreview    `json:"preview"`
	SyncResult model.SiteSyncResult `json:"sync_result"`
}

type manualSyncValidationError struct {
	message string
}

func (e *manualSyncValidationError) Error() string {
	return e.message
}

func manualSyncInvalid(format string, args ...any) error {
	return &manualSyncValidationError{message: fmt.Sprintf(format, args...)}
}

func IsManualSyncValidationError(err error) bool {
	var target *manualSyncValidationError
	return errors.As(err, &target)
}

type manualSyncSections struct {
	tokensProvided  bool
	groupsProvided  bool
	accountProvided bool
	tokens          []model.SiteToken
	groups          []model.SiteUserGroup
	models          map[string][]model.SiteModel
	balance         *float64
	balanceUsed     *float64
	todayIncome     *float64
	accessToken     *string
	warnings        []string
}

type manualSyncPlan struct {
	snapshot    *syncSnapshot
	preview     ManualSyncPreview
	finalTokens []model.SiteToken
	finalGroups []model.SiteUserGroup
	finalModels []model.SiteModel
}
