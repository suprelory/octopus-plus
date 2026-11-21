package model

import "time"

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

	SiteCheckinAdapterManagement  SiteCheckinAdapter = "management"
	SiteCheckinAdapterAnyRouter   SiteCheckinAdapter = "anyrouter"
	SiteCheckinAdapterAgentRouter SiteCheckinAdapter = "agentrouter"
	SiteCheckinAdapterHTTP        SiteCheckinAdapter = "http"
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
	case SitePlatformAgentRouter:
		return SiteCheckinDefaults{Enabled: true, HasBuiltin: true, Adapter: SiteCheckinAdapterAgentRouter}
	case SitePlatformDoneHub:
		return SiteCheckinDefaults{HasBuiltin: true, Adapter: SiteCheckinAdapterManagement}
	default:
		return SiteCheckinDefaults{}
	}
}

func AllPlatformCheckinDefaults() map[SitePlatform]SiteCheckinDefaults {
	result := make(map[SitePlatform]SiteCheckinDefaults)
	for _, platform := range []SitePlatform{SitePlatformNewAPI, SitePlatformOneAPI, SitePlatformOneHub, SitePlatformAnyRouter, SitePlatformAgentRouter, SitePlatformDoneHub, SitePlatformSub2API, SitePlatformAPI} {
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

func resolveCheckinCapability(platform SitePlatform, mode SiteCheckinMode, adapter SiteCheckinAdapter, status SiteCheckinSupport, verifiedAt *time.Time) SiteCheckinCapability {
	defaults := PlatformCheckinDefaults(platform)
	capability := SiteCheckinCapability{
		Enabled: defaults.Enabled, DefaultEnabled: defaults.Enabled,
		CanVerify: adapter != "", Source: "platform_default", Support: SiteCheckinSupportUnknown,
	}
	if verifiedAt != nil && (status == SiteCheckinSupportSupported || status == SiteCheckinSupportUnsupported) {
		capability.Support, capability.VerifiedAt = status, verifiedAt
		capability.Enabled = capability.Support == SiteCheckinSupportSupported
		capability.Source = "verified"
	} else if adapter == SiteCheckinAdapterHTTP {
		capability.Enabled, capability.Source = true, "custom_http"
	}
	switch mode {
	case SiteCheckinModeDisabled:
		capability.Enabled, capability.CanVerify, capability.Source = false, false, "disabled"
	case SiteCheckinModeEnabled:
		capability.Enabled, capability.Source = capability.CanVerify, "instance"
	}
	capability.Enabled = capability.Enabled && capability.CanVerify
	return capability
}
