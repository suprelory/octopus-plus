package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSiteCheckinCapabilityPrecedence(t *testing.T) {
	verifiedAt := time.Now().UTC()
	for _, tc := range []struct {
		name               string
		platform           SitePlatform
		mode               SiteCheckinMode
		http               bool
		evidence           SiteCheckinSupport
		stale              bool
		enabled, canVerify bool
		source             string
	}{
		{"default management", SitePlatformNewAPI, SiteCheckinModeAuto, false, "", false, true, true, "platform_default"},
		{"DoneHub default can be verified", SitePlatformDoneHub, SiteCheckinModeAuto, false, "", false, false, true, "platform_default"},
		{"DoneHub enabled", SitePlatformDoneHub, SiteCheckinModeEnabled, false, "", false, true, true, "instance"},
		{"verified DoneHub", SitePlatformDoneHub, SiteCheckinModeAuto, false, SiteCheckinSupportSupported, false, true, true, "verified"},
		{"verified absent NewAPI", SitePlatformNewAPI, SiteCheckinModeAuto, false, SiteCheckinSupportUnsupported, false, false, true, "verified"},
		{"explicit enable overrides evidence", SitePlatformNewAPI, SiteCheckinModeEnabled, false, SiteCheckinSupportUnsupported, false, true, true, "instance"},
		{"explicit disable overrides evidence", SitePlatformDoneHub, SiteCheckinModeDisabled, false, SiteCheckinSupportSupported, false, false, false, "disabled"},
		{"API custom HTTP", SitePlatformAPI, SiteCheckinModeAuto, true, "", false, true, true, "custom_http"},
		{"Sub2API custom HTTP", SitePlatformSub2API, SiteCheckinModeAuto, true, "", false, true, true, "custom_http"},
		{"disabled overrides HTTP", SitePlatformAPI, SiteCheckinModeDisabled, true, SiteCheckinSupportSupported, false, false, false, "disabled"},
		{"unconfigured API", SitePlatformAPI, SiteCheckinModeAuto, false, "", false, false, false, "platform_default"},
		{"old evidence is ignored", SitePlatformDoneHub, SiteCheckinModeAuto, false, SiteCheckinSupportSupported, true, false, true, "platform_default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site := Site{Platform: tc.platform, BaseURL: "https://site.example", CheckinMode: tc.mode, CheckinHTTPEnabled: tc.http}
			if tc.evidence != "" {
				site.CheckinVerificationStatus = tc.evidence
				site.CheckinVerificationFingerprint = site.CheckinConfigFingerprint()
				site.CheckinVerifiedAt = &verifiedAt
			}
			if tc.stale {
				site.BaseURL = "https://another.example"
			}
			got := site.ResolveCheckinCapability()
			if got.Enabled != tc.enabled || got.CanVerify != tc.canVerify || got.Source != tc.source {
				t.Fatalf("unexpected capability: %+v", got)
			}
			if tc.stale && (got.Support != SiteCheckinSupportUnknown || got.VerifiedAt != nil) {
				t.Fatalf("old deployment evidence was reused: %+v", got)
			}
			encoded, err := json.Marshal(site)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Capability SiteCheckinCapability `json:"checkin_capability"`
			}
			if err := json.Unmarshal(encoded, &response); err != nil {
				t.Fatal(err)
			}
			if response.Capability.Enabled != got.Enabled || response.Capability.Source != got.Source {
				t.Fatalf("API capability differs from scheduling policy: %s", encoded)
			}
		})
	}
}

func TestSiteCheckinEvidenceBindsToRequestConfiguration(t *testing.T) {
	site := Site{Platform: SitePlatformDoneHub, BaseURL: "https://site.example", CheckinHTTPMethod: "POST", ProxyMode: ProxyUsageModeDirect}
	for name, change := range map[string]func(*Site){
		"platform":           func(s *Site) { s.Platform = SitePlatformNewAPI },
		"URL":                func(s *Site) { s.BaseURL += "/v2" },
		"HTTP override":      func(s *Site) { s.CheckinHTTPEnabled = true },
		"subscription token": func(s *Site) { s.CheckinUseSubscriptionToken = true },
		"method":             func(s *Site) { s.CheckinHTTPMethod = "GET" },
		"path":               func(s *Site) { s.CheckinHTTPPath = "/daily" },
		"body":               func(s *Site) { s.CheckinHTTPBody = "{}" },
		"headers":            func(s *Site) { s.CheckinHTTPHeaders = []CustomHeader{{HeaderKey: "X-Version", HeaderValue: "2"}} },
		"site headers":       func(s *Site) { s.CustomHeader = []CustomHeader{{HeaderKey: "X-Version", HeaderValue: "2"}} },
		"proxy":              func(s *Site) { s.ProxyMode = ProxyUsageModeSystem },
	} {
		t.Run(name, func(t *testing.T) {
			changed := site
			change(&changed)
			if changed.CheckinConfigFingerprint() == site.CheckinConfigFingerprint() {
				t.Fatal("request change retained old fingerprint")
			}
		})
	}
	changed := site
	changed.Name, changed.CheckinMode = "Renamed", SiteCheckinModeDisabled
	changed.CheckinWindowStart = "08:00"
	if changed.CheckinConfigFingerprint() != site.CheckinConfigFingerprint() {
		t.Fatal("policy-only edit invalidated endpoint evidence")
	}
}

func TestSiteCheckinModeValidation(t *testing.T) {
	for _, tc := range []struct {
		mode     SiteCheckinMode
		platform SitePlatform
		valid    bool
	}{
		{"", SitePlatformDoneHub, true}, {SiteCheckinModeEnabled, SitePlatformDoneHub, true},
		{SiteCheckinModeEnabled, SitePlatformAPI, false}, {"invalid", SitePlatformNewAPI, false},
	} {
		site := Site{Name: "Site", Platform: tc.platform, BaseURL: "https://site.example", CheckinMode: tc.mode}
		if err := site.Validate(); (err == nil) != tc.valid {
			t.Fatalf("mode %q, platform %q: %v", tc.mode, tc.platform, err)
		}
	}
}
