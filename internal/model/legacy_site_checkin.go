package model

import "encoding/json"

func (s *LegacySite) CheckinAdapter() SiteCheckinAdapter {
	if s.CheckinHTTPEnabled {
		return SiteCheckinAdapterHTTP
	}
	return PlatformCheckinDefaults(s.Platform).Adapter
}

func (s *LegacySite) ResolveCheckinCapability() SiteCheckinCapability {
	verifiedAt := s.CheckinVerifiedAt
	if verifiedAt != nil && s.CheckinVerificationFingerprint != s.CheckinConfigFingerprint() {
		verifiedAt = nil
	}
	return resolveCheckinCapability(s.Platform, s.CheckinMode, s.CheckinAdapter(), s.CheckinVerificationStatus, verifiedAt)
}

// Bind evidence to the exact endpoint configuration. A changed URL, method,
// body, headers or proxy must never reuse a previous deployment's verdict.
func (s *LegacySite) CheckinConfigFingerprint() string {
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
		UseSubscriptionToken   bool                     `json:",omitempty"`
	}{s.Platform, s.BaseURL, s.CheckinHTTPEnabled, s.CheckinHTTPMethod, s.CheckinHTTPPath, s.CheckinHTTPBody, s.CheckinHTTPHeaders, s.CustomHeader, s.ProxyMode, s.ProxyConfigID, s.LinkedSiteID, s.CheckinStatus, s.CheckinUseSubscriptionToken}
	return checkinFingerprint(config)
}

// The API and exports expose the resolved capability without trusting a
// client-supplied decision or duplicating the platform defaults in the UI.
func (s LegacySite) MarshalJSON() ([]byte, error) {
	type siteAlias LegacySite
	return json.Marshal(struct {
		siteAlias
		Capability SiteCheckinCapability `json:"checkin_capability"`
	}{siteAlias(s), s.ResolveCheckinCapability()})
}

func (s *LegacySite) CheckinWorkflowFingerprint(account *LegacySiteAccount) string {
	var credential any = account.Cookie
	if s.CheckinUseSubscriptionToken {
		// Refreshing the same subscription account's token must not interrupt
		// confirmation, but selecting another account changes the workflow.
		credential = account.LinkedAccountID
	}
	return checkinFingerprint([]any{s.CheckinConfigFingerprint(), s.CheckinTimezone, credential, account.Username, account.ProxyMode, account.ProxyConfigID})
}
