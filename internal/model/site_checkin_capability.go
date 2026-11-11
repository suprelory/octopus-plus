package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

type SiteCheckinMode string
type SiteCheckinSupport string
type SiteCheckinAdapter string

const (
	SiteCheckinModeAuto     SiteCheckinMode = "auto"
	SiteCheckinModeEnabled  SiteCheckinMode = "enabled"
	SiteCheckinModeDisabled SiteCheckinMode = "disabled"

	SiteCheckinSupportUnknown     SiteCheckinSupport = "unknown"
	SiteCheckinSupportSupported   SiteCheckinSupport = "supported"
	SiteCheckinSupportUnsupported SiteCheckinSupport = "unsupported"

	SiteCheckinAdapterManagement SiteCheckinAdapter = "management"
	SiteCheckinAdapterAnyRouter  SiteCheckinAdapter = "anyrouter"
	SiteCheckinAdapterHTTP       SiteCheckinAdapter = "http"
)

type SiteCheckinDefaults struct {
	Enabled    bool               `json:"enabled"`
	HasBuiltin bool               `json:"has_builtin"`
	Adapter    SiteCheckinAdapter `json:"-"`
}

// Defaults are preferences, not a verdict about a particular deployment.
// DoneHub retains its conservative default but can use the management adapter
// when enabled for a site or explicitly verified through a manual check-in.
func PlatformCheckinDefaults(platform SitePlatform) SiteCheckinDefaults {
	switch platform {
	case SitePlatformNewAPI, SitePlatformOneAPI, SitePlatformOneHub:
		return SiteCheckinDefaults{Enabled: true, HasBuiltin: true, Adapter: SiteCheckinAdapterManagement}
	case SitePlatformAnyRouter:
		return SiteCheckinDefaults{Enabled: true, HasBuiltin: true, Adapter: SiteCheckinAdapterAnyRouter}
	case SitePlatformDoneHub:
		return SiteCheckinDefaults{HasBuiltin: true, Adapter: SiteCheckinAdapterManagement}
	default:
		return SiteCheckinDefaults{}
	}
}

func AllPlatformCheckinDefaults() map[SitePlatform]SiteCheckinDefaults {
	result := make(map[SitePlatform]SiteCheckinDefaults)
	for _, platform := range []SitePlatform{SitePlatformNewAPI, SitePlatformOneAPI, SitePlatformOneHub, SitePlatformAnyRouter, SitePlatformDoneHub, SitePlatformSub2API, SitePlatformAPI} {
		result[platform] = PlatformCheckinDefaults(platform)
	}
	return result
}

type SiteCheckinCapability struct {
	Enabled        bool               `json:"enabled"`
	CanVerify      bool               `json:"can_verify"`
	DefaultEnabled bool               `json:"default_enabled"`
	Source         string             `json:"source"`
	Support        SiteCheckinSupport `json:"support"`
	VerifiedAt     *time.Time         `json:"verified_at,omitempty"`
}

func (s *Site) CheckinAdapter() SiteCheckinAdapter {
	if s.CheckinHTTPEnabled {
		return SiteCheckinAdapterHTTP
	}
	return PlatformCheckinDefaults(s.Platform).Adapter
}

func (s *Site) ResolveCheckinCapability() SiteCheckinCapability {
	defaults := PlatformCheckinDefaults(s.Platform)
	capability := SiteCheckinCapability{
		Enabled: defaults.Enabled, DefaultEnabled: defaults.Enabled,
		CanVerify: s.CheckinAdapter() != "", Source: "platform_default", Support: SiteCheckinSupportUnknown,
	}
	if s.CheckinVerifiedAt != nil && s.CheckinVerificationFingerprint == s.CheckinConfigFingerprint() &&
		(s.CheckinVerificationStatus == SiteCheckinSupportSupported || s.CheckinVerificationStatus == SiteCheckinSupportUnsupported) {
		capability.Support, capability.VerifiedAt = s.CheckinVerificationStatus, s.CheckinVerifiedAt
		capability.Enabled = capability.Support == SiteCheckinSupportSupported
		capability.Source = "verified"
	} else if s.CheckinHTTPEnabled {
		capability.Enabled, capability.Source = true, "custom_http"
	}
	switch s.CheckinMode {
	case SiteCheckinModeDisabled:
		capability.Enabled, capability.CanVerify, capability.Source = false, false, "disabled"
	case SiteCheckinModeEnabled:
		capability.Enabled, capability.Source = capability.CanVerify, "instance"
	}
	capability.Enabled = capability.Enabled && capability.CanVerify
	return capability
}

// Bind evidence to the exact endpoint configuration. A changed URL, method,
// body, headers or proxy must never reuse a previous deployment's verdict.
func (s *Site) CheckinConfigFingerprint() string {
	config := struct {
		Platform               SitePlatform
		BaseURL                string
		HTTP                   bool
		Method, Path, Body     string
		Headers, CustomHeaders []CustomHeader
		ProxyMode              ProxyUsageMode
		ProxyID                *int
		LinkedSiteID           *int                     `json:",omitempty"`
		Status                 *SiteCheckinStatusConfig `json:",omitempty"`
	}{s.Platform, s.BaseURL, s.CheckinHTTPEnabled, s.CheckinHTTPMethod, s.CheckinHTTPPath, s.CheckinHTTPBody, s.CheckinHTTPHeaders, s.CustomHeader, s.ProxyMode, s.ProxyConfigID, s.LinkedSiteID, s.CheckinStatus}
	payload, _ := json.Marshal(config)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// The API and exports expose the resolved capability without trusting a
// client-supplied decision or duplicating the platform defaults in the UI.
func (s Site) MarshalJSON() ([]byte, error) {
	type siteAlias Site
	return json.Marshal(struct {
		siteAlias
		Capability SiteCheckinCapability `json:"checkin_capability"`
	}{siteAlias(s), s.ResolveCheckinCapability()})
}
