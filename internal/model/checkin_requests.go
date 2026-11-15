package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
)

// CheckinPatchField distinguishes omission from an explicit zero or null. Only
// pointer and slice fields accept null; scalar nulls are invalid requests.
type CheckinPatchField[T any] struct {
	Present bool
	Value   T
}

func (f *CheckinPatchField[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		kind := reflect.TypeFor[T]().Kind()
		if kind != reflect.Pointer && kind != reflect.Slice {
			return errors.New("checkin scalar fields cannot be null")
		}
	}
	var value T
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return errors.New("invalid checkin field value")
	}
	f.Value, f.Present = value, true
	return nil
}

func (f CheckinPatchField[T]) MarshalJSON() ([]byte, error) { return json.Marshal(f.Value) }
func (f CheckinPatchField[T]) IsZero() bool                 { return !f.Present }
func (f CheckinPatchField[T]) Apply(target *T) {
	if f.Present {
		*target = f.Value
	}
}

func decodeCheckinPatch(data []byte, target any) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return errors.New("checkin patch must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		// Do not echo user-supplied secrets or unrecognized field names.
		return errors.New("invalid checkin patch fields")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid checkin patch document")
	}
	return nil
}

type CheckinNetworkPatch struct {
	ProxyMode     CheckinPatchField[ProxyUsageMode] `json:"proxy_mode,omitzero"`
	ProxyConfigID CheckinPatchField[*int]           `json:"proxy_config_id,omitzero"`
}

func (p CheckinNetworkPatch) Apply(n *CheckinNetwork) {
	p.ProxyMode.Apply(&n.ProxyMode)
	p.ProxyConfigID.Apply(&n.ProxyConfigID)
}

// Server-owned verification, archive and execution state is deliberately absent.
type CheckinSitePatch struct {
	Name     CheckinPatchField[string]       `json:"name,omitzero"`
	Platform CheckinPatchField[SitePlatform] `json:"platform,omitzero"`
	BaseURL  CheckinPatchField[string]       `json:"base_url,omitzero"`
	Enabled  CheckinPatchField[bool]         `json:"enabled,omitzero"`
	CheckinNetworkPatch
	CustomHeader           CheckinPatchField[[]CustomHeader]           `json:"custom_header,omitzero"`
	ExternalCheckinURL     CheckinPatchField[*string]                  `json:"external_checkin_url,omitzero"`
	CheckinMode            CheckinPatchField[SiteCheckinMode]          `json:"checkin_mode,omitzero"`
	CheckinHTTPEnabled     CheckinPatchField[bool]                     `json:"checkin_http_enabled,omitzero"`
	CheckinHTTPMethod      CheckinPatchField[string]                   `json:"checkin_http_method,omitzero"`
	CheckinHTTPPath        CheckinPatchField[string]                   `json:"checkin_http_path,omitzero"`
	CheckinHTTPBody        CheckinPatchField[string]                   `json:"checkin_http_body,omitzero"`
	CheckinHTTPHeaders     CheckinPatchField[[]CustomHeader]           `json:"checkin_http_headers,omitzero"`
	CheckinStatus          CheckinPatchField[*SiteCheckinStatusConfig] `json:"checkin_status,omitzero"`
	CheckinRewardExtractor CheckinPatchField[string]                   `json:"checkin_reward_extractor,omitzero"`
	CheckinTimezone        CheckinPatchField[string]                   `json:"checkin_timezone,omitzero"`
	CheckinWindowStart     CheckinPatchField[string]                   `json:"checkin_window_start,omitzero"`
	CheckinWindowEnd       CheckinPatchField[string]                   `json:"checkin_window_end,omitzero"`
	IsPinned               CheckinPatchField[bool]                     `json:"is_pinned,omitzero"`
	SortOrder              CheckinPatchField[int]                      `json:"sort_order,omitzero"`
	Tags                   CheckinPatchField[[]string]                 `json:"tags,omitzero"`
}

func (p *CheckinSitePatch) UnmarshalJSON(data []byte) error {
	type plain CheckinSitePatch
	var decoded plain
	if err := decodeCheckinPatch(data, &decoded); err != nil {
		return err
	}
	*p = CheckinSitePatch(decoded)
	return nil
}

func (p CheckinSitePatch) Apply(s *CheckinSite) {
	p.Name.Apply(&s.Name)
	p.Platform.Apply(&s.Platform)
	p.BaseURL.Apply(&s.BaseURL)
	p.Enabled.Apply(&s.Enabled)
	p.CheckinNetworkPatch.Apply(&s.CheckinNetwork)
	p.CustomHeader.Apply(&s.CustomHeader)
	p.ExternalCheckinURL.Apply(&s.ExternalCheckinURL)
	p.CheckinMode.Apply(&s.CheckinMode)
	p.CheckinHTTPEnabled.Apply(&s.CheckinHTTPEnabled)
	p.CheckinHTTPMethod.Apply(&s.CheckinHTTPMethod)
	p.CheckinHTTPPath.Apply(&s.CheckinHTTPPath)
	p.CheckinHTTPBody.Apply(&s.CheckinHTTPBody)
	p.CheckinHTTPHeaders.Apply(&s.CheckinHTTPHeaders)
	p.CheckinStatus.Apply(&s.CheckinStatus)
	p.CheckinRewardExtractor.Apply(&s.CheckinRewardExtractor)
	p.CheckinTimezone.Apply(&s.CheckinTimezone)
	p.CheckinWindowStart.Apply(&s.CheckinWindowStart)
	p.CheckinWindowEnd.Apply(&s.CheckinWindowEnd)
	p.IsPinned.Apply(&s.IsPinned)
	p.SortOrder.Apply(&s.SortOrder)
	p.Tags.Apply(&s.Tags)
}

type CheckinAccountPatch struct {
	Name         CheckinPatchField[string]          `json:"name,omitzero"`
	CredentialID CheckinPatchField[int]             `json:"credential_id,omitzero"`
	AuthMode     CheckinPatchField[CheckinAuthMode] `json:"auth_mode,omitzero"`
	CheckinNetworkPatch
	Enabled                    CheckinPatchField[bool] `json:"enabled,omitzero"`
	AutoCheckin                CheckinPatchField[bool] `json:"auto_checkin,omitzero"`
	RandomCheckin              CheckinPatchField[bool] `json:"random_checkin,omitzero"`
	CheckinRandomWindowMinutes CheckinPatchField[int]  `json:"checkin_random_window_minutes,omitzero"`
	// A supplied balance_config replaces the complete configuration; null disables it.
	BalanceConfig CheckinPatchField[*CheckinBalanceConfig] `json:"balance_config,omitzero"`
}

func (p *CheckinAccountPatch) UnmarshalJSON(data []byte) error {
	type plain CheckinAccountPatch
	var decoded plain
	if err := decodeCheckinPatch(data, &decoded); err != nil {
		return err
	}
	*p = CheckinAccountPatch(decoded)
	return nil
}

func (p CheckinAccountPatch) Apply(a *CheckinAccount) {
	p.Name.Apply(&a.Name)
	p.CredentialID.Apply(&a.CredentialID)
	p.AuthMode.Apply(&a.AuthMode)
	p.CheckinNetworkPatch.Apply(&a.CheckinNetwork)
	p.Enabled.Apply(&a.Enabled)
	p.AutoCheckin.Apply(&a.AutoCheckin)
	p.RandomCheckin.Apply(&a.RandomCheckin)
	p.CheckinRandomWindowMinutes.Apply(&a.CheckinRandomWindowMinutes)
	if p.BalanceConfig.Present {
		a.BalanceConfig = CheckinBalanceConfig{Mode: CheckinBalanceDisabled}
		if p.BalanceConfig.Value != nil {
			a.BalanceConfig = *p.BalanceConfig.Value
		}
	}
}

// Secrets are write-only inputs. Responses must use CheckinCredentialSummary.
type CheckinCredentialPatch struct {
	ExpectedVersion CheckinPatchField[int64]                 `json:"expected_version,omitzero"`
	Name            CheckinPatchField[string]                `json:"name,omitzero"`
	Type            CheckinPatchField[CheckinCredentialType] `json:"type,omitzero"`
	Platform        CheckinPatchField[SitePlatform]          `json:"platform,omitzero"`
	AuthBaseURL     CheckinPatchField[string]                `json:"auth_base_url,omitzero"`
	CheckinNetworkPatch
	AuthHeaders    CheckinPatchField[[]CustomHeader] `json:"auth_headers,omitzero"`
	Username       CheckinPatchField[string]         `json:"username,omitzero"`
	Password       CheckinPatchField[string]         `json:"password,omitzero"`
	AccessToken    CheckinPatchField[string]         `json:"access_token,omitzero"`
	APIKey         CheckinPatchField[string]         `json:"api_key,omitzero"`
	RefreshToken   CheckinPatchField[string]         `json:"refresh_token,omitzero"`
	Cookie         CheckinPatchField[string]         `json:"cookie,omitzero"`
	PlatformUserID CheckinPatchField[*int]           `json:"platform_user_id,omitzero"`
	TokenExpiresAt CheckinPatchField[int64]          `json:"token_expires_at,omitzero"`
}

func (p *CheckinCredentialPatch) UnmarshalJSON(data []byte) error {
	type plain CheckinCredentialPatch
	var decoded plain
	if err := decodeCheckinPatch(data, &decoded); err != nil {
		return err
	}
	*p = CheckinCredentialPatch(decoded)
	return nil
}

func (p CheckinCredentialPatch) Apply(c *CheckinCredential) {
	p.Name.Apply(&c.Name)
	p.Type.Apply(&c.Type)
	p.Platform.Apply(&c.Platform)
	p.AuthBaseURL.Apply(&c.AuthBaseURL)
	p.CheckinNetworkPatch.Apply(&c.CheckinNetwork)
	p.AuthHeaders.Apply(&c.AuthHeaders)
	p.Username.Apply(&c.Username)
	p.Password.Apply(&c.Password)
	p.AccessToken.Apply(&c.AccessToken)
	p.APIKey.Apply(&c.APIKey)
	p.RefreshToken.Apply(&c.RefreshToken)
	p.Cookie.Apply(&c.Cookie)
	p.PlatformUserID.Apply(&c.PlatformUserID)
	p.TokenExpiresAt.Apply(&c.TokenExpiresAt)
}
