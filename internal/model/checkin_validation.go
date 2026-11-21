package model

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

func (n *CheckinNetwork) Validate(allowInherit bool) error {
	if n.ProxyMode == "" {
		n.ProxyMode = ProxyUsageModeDirect
		if allowInherit {
			n.ProxyMode = ProxyUsageModeInherit
		}
	}
	if err := n.ProxyMode.Validate(allowInherit); err != nil {
		return errors.New("invalid checkin proxy mode")
	}
	if n.ProxyMode == ProxyUsageModePool {
		if n.ProxyConfigID == nil || *n.ProxyConfigID <= 0 {
			return errors.New("checkin proxy ID is required")
		}
	} else {
		n.ProxyConfigID = nil
	}
	return nil
}

func validateCheckinURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("checkin address must be an HTTP(S) URL without credentials, query or fragment")
	}
	return nil
}

func validateCheckinHeaders(headers []CustomHeader) error {
	if len(headers) > 32 {
		return errors.New("checkin headers must not exceed 32")
	}
	seen := make(map[string]bool)
	for _, header := range headers {
		key := strings.ToLower(header.HeaderKey)
		if !httpguts.ValidHeaderFieldName(header.HeaderKey) || !httpguts.ValidHeaderFieldValue(header.HeaderValue) || strings.ContainsRune(header.HeaderValue, 0) || len(key) > 256 || len(header.HeaderValue) > 4096 || seen[key] || !siteCheckinHeaderAllowed(key) {
			return errors.New("invalid or duplicate checkin header")
		}
		seen[key] = true
	}
	return nil
}

func (s *CheckinSite) Validate() error {
	if s == nil {
		return errors.New("checkin site is required")
	}
	s.Name = strings.TrimSpace(s.Name)
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if s.Name == "" || len([]rune(s.Name)) > 191 {
		return errors.New("checkin site name must contain 1 to 191 characters")
	}
	if err := s.Platform.Validate(); err != nil {
		return errors.New("unsupported checkin platform")
	}
	if err := validateCheckinURL(s.BaseURL); err != nil {
		return err
	}
	if err := s.CheckinNetwork.Validate(false); err != nil {
		return err
	}
	if err := validateCheckinHeaders(s.CustomHeader); err != nil {
		return err
	}
	if s.CheckinMode == "" {
		s.CheckinMode = SiteCheckinModeAuto
	}
	switch s.CheckinMode {
	case SiteCheckinModeAuto, SiteCheckinModeEnabled, SiteCheckinModeDisabled:
	default:
		return errors.New("invalid checkin mode")
	}
	if s.CheckinMode == SiteCheckinModeEnabled && !s.CheckinHTTPEnabled && PlatformCheckinDefaults(s.Platform).Adapter == "" {
		return errors.New("custom HTTP checkin is required for this platform")
	}
	if s.CheckinTimezone == "" {
		s.CheckinTimezone = DefaultSiteCheckinTimezone
	}
	if s.CheckinWindowStart == "" {
		s.CheckinWindowStart = DefaultSiteCheckinWindowStart
	}
	if s.CheckinWindowEnd == "" {
		s.CheckinWindowEnd = DefaultSiteCheckinWindowEnd
	}
	if _, err := time.LoadLocation(s.CheckinTimezone); err != nil {
		return errors.New("invalid checkin timezone")
	}
	start, err := time.Parse("15:04", s.CheckinWindowStart)
	if err != nil {
		return errors.New("invalid checkin window start")
	}
	end, err := time.Parse("15:04", s.CheckinWindowEnd)
	if err != nil || end.Before(start) {
		return errors.New("invalid checkin window end")
	}
	if s.ExternalCheckinURL != nil {
		parsed, err := url.Parse(*s.ExternalCheckinURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
			return errors.New("invalid manual checkin URL")
		}
	}
	if err := ValidateSiteTags(s.Tags); err != nil {
		return err
	}
	if err := ValidateCheckinRewardExtractor(s.CheckinRewardExtractor); err != nil {
		return err
	}
	if s.CheckinHTTPEnabled {
		s.CheckinHTTPMethod = strings.ToUpper(strings.TrimSpace(s.CheckinHTTPMethod))
		if s.CheckinHTTPMethod == "" {
			s.CheckinHTTPMethod = http.MethodPost
		}
		if s.CheckinHTTPMethod != http.MethodGet && s.CheckinHTTPMethod != http.MethodPost {
			return errors.New("checkin HTTP method must be GET or POST")
		}
		if err := ValidateSiteCheckinHTTPPath(s.CheckinHTTPPath); err != nil {
			return err
		}
		if err := validateCheckinHeaders(s.CheckinHTTPHeaders); err != nil {
			return err
		}
		if len(s.CheckinHTTPBody) > 64<<10 || (s.CheckinHTTPBody != "" && (s.CheckinHTTPMethod == http.MethodGet || !json.Valid([]byte(s.CheckinHTTPBody)))) {
			return errors.New("invalid checkin request body")
		}
	}
	if s.CheckinStatus != nil {
		if !s.CheckinHTTPEnabled {
			return errors.New("status confirmation requires custom HTTP checkin")
		}
		s.CheckinStatus.Normalize()
		if err := s.CheckinStatus.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (b *CheckinBalanceConfig) Validate() error {
	if b.Mode == "" {
		b.Mode = CheckinBalanceDisabled
	}
	switch b.Mode {
	case CheckinBalanceDisabled, CheckinBalancePrimary:
		*b = CheckinBalanceConfig{Mode: b.Mode}
	case CheckinBalanceCustom:
		if b.CredentialID == nil || *b.CredentialID <= 0 {
			return errors.New("balance credential ID is required")
		}
		if b.Platform == SitePlatformAPI || b.Platform.Validate() != nil {
			return errors.New("platform does not support balance queries")
		}
		b.BaseURL = strings.TrimRight(strings.TrimSpace(b.BaseURL), "/")
		if err := validateCheckinURL(b.BaseURL); err != nil {
			return err
		}
		if err := b.CheckinNetwork.Validate(false); err != nil {
			return err
		}
		if err := validateCheckinHeaders(b.Headers); err != nil {
			return err
		}
	default:
		return errors.New("invalid checkin balance mode")
	}
	return nil
}

func (a *CheckinAccount) Validate() error {
	if a == nil {
		return errors.New("checkin account is required")
	}
	a.Name = strings.TrimSpace(a.Name)
	if a.SiteID <= 0 || a.CredentialID <= 0 {
		return errors.New("checkin site and credential IDs are required")
	}
	if a.Name == "" || len([]rune(a.Name)) > 191 {
		return errors.New("checkin account name must contain 1 to 191 characters")
	}
	switch a.AuthMode {
	case CheckinAuthPlatform, CheckinAuthCookie, CheckinAuthBearer:
	default:
		return errors.New("invalid checkin authentication mode")
	}
	if err := a.CheckinNetwork.Validate(true); err != nil {
		return err
	}
	if a.CheckinRandomWindowMinutes < 0 || a.CheckinRandomWindowMinutes > 1440 {
		return errors.New("random checkin window must be between 0 and 1440 minutes")
	}
	return a.BalanceConfig.Validate()
}

func (c *CheckinCredential) Validate() error {
	if c == nil {
		return errors.New("checkin credential is required")
	}
	c.Name = strings.TrimSpace(c.Name)
	c.AuthBaseURL = strings.TrimRight(strings.TrimSpace(c.AuthBaseURL), "/")
	c.Username = strings.TrimSpace(c.Username)
	c.AccessToken = NormalizeSiteAccessToken(c.AccessToken)
	c.RefreshToken, c.APIKey, c.Cookie = strings.TrimSpace(c.RefreshToken), strings.TrimSpace(c.APIKey), strings.TrimSpace(c.Cookie)
	if c.Name == "" || len([]rune(c.Name)) > 191 {
		return errors.New("credential name must contain 1 to 191 characters")
	}
	if c.Platform.Validate() != nil {
		return errors.New("unsupported credential platform")
	}
	if err := validateCheckinURL(c.AuthBaseURL); err != nil {
		return err
	}
	if err := c.CheckinNetwork.Validate(false); err != nil {
		return err
	}
	if err := validateCheckinHeaders(c.AuthHeaders); err != nil {
		return err
	}
	if err := c.ValidateSessionCookies(); err != nil {
		return err
	}
	if c.TokenExpiresAt < 0 || c.CredentialVersion < 0 || c.IdentityRevision < 0 {
		return errors.New("invalid credential version or expiration")
	}
	if c.PlatformUserID != nil && *c.PlatformUserID <= 0 {
		return errors.New("invalid platform user ID")
	}
	for _, token := range []string{c.AccessToken, c.RefreshToken, c.APIKey} {
		if len(token) > 8192 || strings.ContainsAny(token, "\r\n\x00") {
			return errors.New("credential token must be a single value of at most 8 KiB")
		}
	}
	switch c.Type {
	case CheckinCredentialPassword:
		if c.Username == "" || c.Password == "" {
			return errors.New("username and password are required")
		}
	case CheckinCredentialAccessToken:
		if c.AccessToken == "" {
			return errors.New("access token is required")
		}
	case CheckinCredentialAPIKey:
		if c.APIKey == "" {
			return errors.New("API key is required")
		}
	case CheckinCredentialCookie:
		if err := ValidateSiteCheckinCookie(c.Cookie); err != nil {
			return err
		}
	default:
		return errors.New("unsupported checkin credential type")
	}
	return nil
}

// ValidateCheckinIdentity checks compatibility without resolving credentials from
// any subscription entity. A custom HTTP token may intentionally target another
// address; built-in platform sign-in must authenticate against the same service.
func ValidateCheckinIdentity(site CheckinSite, account CheckinAccount, credential CheckinCredential) error {
	if account.SiteID != site.ID || account.CredentialID != credential.ID {
		return errors.New("checkin identity reference mismatch")
	}
	if site.CheckinHTTPEnabled {
		switch account.AuthMode {
		case CheckinAuthCookie:
			if credential.Type == CheckinCredentialCookie {
				return nil
			}
		case CheckinAuthBearer:
			if credential.Type == CheckinCredentialAccessToken {
				return nil
			}
		}
		return errors.New("custom HTTP checkin requires a matching Cookie or Access Token credential")
	}
	if account.AuthMode != CheckinAuthPlatform || credential.Platform != site.Platform || credential.AuthBaseURL != site.BaseURL {
		return errors.New("platform checkin requires credentials for the same platform and address")
	}
	if site.Platform == SitePlatformAgentRouter && credential.Type != CheckinCredentialPassword {
		return errors.New("AgentRouter checkin requires username and password")
	}
	if site.Platform == SitePlatformAnyRouter && credential.Type == CheckinCredentialCookie {
		return nil
	}
	if credential.Type != CheckinCredentialPassword && credential.Type != CheckinCredentialAccessToken {
		return errors.New("platform checkin requires username/password or Access Token")
	}
	return nil
}

func ValidateCheckinBalanceCredential(config CheckinBalanceConfig, credential CheckinCredential) error {
	if config.Platform != credential.Platform {
		return errors.New("balance platform must match its credential")
	}
	switch config.Platform {
	case SitePlatformSub2API:
		if credential.Type == CheckinCredentialAccessToken || credential.Type == CheckinCredentialAPIKey {
			return nil
		}
	case SitePlatformNewAPI, SitePlatformAnyRouter, SitePlatformAgentRouter, SitePlatformOneAPI, SitePlatformOneHub, SitePlatformDoneHub:
		if credential.Type == CheckinCredentialAccessToken || credential.Type == CheckinCredentialPassword || credential.Type == CheckinCredentialCookie {
			return nil
		}
	}
	return errors.New("credential cannot query platform balances")
}
