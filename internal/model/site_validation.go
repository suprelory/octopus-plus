package model

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func NormalizeSiteGroupKey(value string) string {
	key := strings.TrimSpace(value)
	if key == "" {
		return SiteDefaultGroupKey
	}
	return key
}

func NormalizeSiteGroupName(groupKey string, name string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(groupKey); trimmed != "" {
		return trimmed
	}
	return SiteDefaultGroupName
}

func (t SiteCredentialType) Validate() error {
	switch t {
	case SiteCredentialTypeUsernamePassword, SiteCredentialTypeAccessToken, SiteCredentialTypeAPIKey, SiteCredentialTypeCookie, SiteCredentialTypeLinkedAccount:
		return nil
	default:
		return fmt.Errorf("unsupported site credential type: %s", t)
	}
}

func (k SiteKind) Validate() error {
	switch k {
	case SiteKindRelay, SiteKindCheckin:
		return nil
	default:
		return fmt.Errorf("site kind must be relay or checkin")
	}
}

func (s *Site) Normalize() {
	s.Name = strings.TrimSpace(s.Name)
	if s.Kind == "" {
		s.Kind = SiteKindRelay
	}
	if s.Kind != SiteKindCheckin || (s.LinkedSiteID != nil && *s.LinkedSiteID <= 0) {
		s.LinkedSiteID = nil
	}
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if s.ExternalCheckinURL != nil {
		trimmed := strings.TrimRight(strings.TrimSpace(*s.ExternalCheckinURL), "/")
		if trimmed == "" {
			s.ExternalCheckinURL = nil
		} else {
			s.ExternalCheckinURL = &trimmed
		}
	}
	s.CheckinTimezone = strings.TrimSpace(s.CheckinTimezone)
	if s.CheckinTimezone == "" {
		s.CheckinTimezone = DefaultSiteCheckinTimezone
	}
	s.CheckinWindowStart = strings.TrimSpace(s.CheckinWindowStart)
	if s.CheckinWindowStart == "" {
		s.CheckinWindowStart = DefaultSiteCheckinWindowStart
	}
	s.CheckinWindowEnd = strings.TrimSpace(s.CheckinWindowEnd)
	if s.CheckinWindowEnd == "" {
		s.CheckinWindowEnd = DefaultSiteCheckinWindowEnd
	}
	s.CheckinHTTPMethod = strings.ToUpper(strings.TrimSpace(s.CheckinHTTPMethod))
	if s.CheckinHTTPMethod == "" {
		s.CheckinHTTPMethod = http.MethodPost
	}
	if s.CheckinMode == "" {
		s.CheckinMode = SiteCheckinModeAuto
	}
	s.CheckinHTTPPath = strings.TrimSpace(s.CheckinHTTPPath)
	s.CheckinHTTPBody = strings.TrimSpace(s.CheckinHTTPBody)
	s.CheckinRewardExtractor = strings.TrimSpace(s.CheckinRewardExtractor)
	normalizeCheckinHeaderKeys(s.CheckinHTTPHeaders)
	if strings.TrimSpace(string(s.ProxyMode)) == "" {
		s.ProxyMode = ProxyUsageModeDirect
	}
	if s.ProxyMode != ProxyUsageModePool {
		s.ProxyConfigID = nil
	}
	if s.GlobalWeight <= 0 {
		s.GlobalWeight = 1
	}
	if s.SortOrder < 0 {
		s.SortOrder = 0
	}
	s.Tags = NormalizeSiteTags(s.Tags)
	s.RouteBaseURLs = NormalizeSiteRouteBaseURLs(s.RouteBaseURLs)
	if s.DefaultRouteType != "" {
		s.DefaultRouteType = NormalizeSiteModelRouteType(s.DefaultRouteType)
	}
}

func (s *Site) Validate() error {
	if s == nil {
		return fmt.Errorf("site is nil")
	}
	s.Normalize()
	if s.Name == "" {
		return fmt.Errorf("site name is required")
	}
	if err := s.Kind.Validate(); err != nil {
		return err
	}
	if s.LinkedSiteID != nil && s.ID != 0 && *s.LinkedSiteID == s.ID {
		return fmt.Errorf("a check-in site cannot be linked to itself")
	}
	if err := s.Platform.Validate(); err != nil {
		return err
	}
	if s.CheckinUseSubscriptionToken && (!s.IsCheckinOnly() || !s.CheckinHTTPEnabled || s.Platform != SitePlatformSub2API || s.LinkedSiteID == nil) {
		return fmt.Errorf("subscription token checkin requires a Sub2API checkin site with custom HTTP enabled and a linked subscription site")
	}
	switch s.CheckinMode {
	case SiteCheckinModeAuto, SiteCheckinModeEnabled, SiteCheckinModeDisabled:
	default:
		return fmt.Errorf("site checkin mode must be auto, enabled or disabled")
	}
	if s.CheckinMode == SiteCheckinModeEnabled && s.CheckinAdapter() == "" {
		return fmt.Errorf("configure a custom HTTP checkin endpoint before enabling checkin on this platform")
	}
	if s.DefaultRouteType == SiteModelRouteTypeUnknown {
		return fmt.Errorf("site default route type is unsupported")
	}
	if err := s.ProxyMode.Validate(false); err != nil {
		return err
	}
	if s.ProxyMode == ProxyUsageModePool && (s.ProxyConfigID == nil || *s.ProxyConfigID <= 0) {
		return fmt.Errorf("proxy config id is required when proxy mode is pool")
	}
	if err := ValidateSiteTags(s.Tags); err != nil {
		return err
	}
	parsed, err := url.Parse(s.BaseURL)
	if err != nil {
		return fmt.Errorf("site base url is invalid: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("site base url must use http or https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("site base url must have a host")
	}
	if err := ValidateSiteRouteBaseURLs(s.RouteBaseURLs); err != nil {
		return err
	}
	if s.ExternalCheckinURL != nil {
		checkinParsed, err := url.Parse(*s.ExternalCheckinURL)
		if err != nil {
			return fmt.Errorf("external checkin url is invalid: %w", err)
		}
		if checkinParsed.Scheme != "http" && checkinParsed.Scheme != "https" {
			return fmt.Errorf("external checkin url must use http or https")
		}
		if checkinParsed.Host == "" {
			return fmt.Errorf("external checkin url must have a host")
		}
	}
	if err := ValidateCheckinRewardExtractor(s.CheckinRewardExtractor); err != nil {
		return err
	}
	if s.CheckinStatus != nil {
		if err := s.CheckinStatus.Validate(); err != nil {
			return err
		}
	}
	if s.CheckinHTTPEnabled {
		if err := validateSiteCheckinHTTPRequest(s.CheckinHTTPMethod, s.CheckinHTTPPath, s.CheckinHTTPBody, s.CheckinHTTPHeaders); err != nil {
			return err
		}
	}
	if _, err := time.LoadLocation(s.CheckinTimezone); err != nil {
		return fmt.Errorf("site checkin timezone is invalid: %w", err)
	}
	windowStart, err := time.Parse("15:04", s.CheckinWindowStart)
	if err != nil {
		return fmt.Errorf("site checkin window start must use HH:MM format")
	}
	windowEnd, err := time.Parse("15:04", s.CheckinWindowEnd)
	if err != nil {
		return fmt.Errorf("site checkin window end must use HH:MM format")
	}
	if windowEnd.Before(windowStart) {
		return fmt.Errorf("site checkin window end must not be before start")
	}
	return nil
}

func (a *SiteAccount) Normalize() {
	a.Name = strings.TrimSpace(a.Name)
	a.Username = strings.TrimSpace(a.Username)
	a.Password = strings.TrimSpace(a.Password)
	a.AccessToken = strings.TrimSpace(a.AccessToken)
	a.APIKey = strings.TrimSpace(a.APIKey)
	a.RefreshToken = strings.TrimSpace(a.RefreshToken)
	a.Cookie = strings.TrimSpace(a.Cookie)
	if a.TokenExpiresAt < 0 {
		a.TokenExpiresAt = 0
	}
	if a.TokenExpiresAt > 0 && a.TokenExpiresAt < 1_000_000_000_000 {
		a.TokenExpiresAt *= 1000
	}
	if a.PlatformUserID != nil && *a.PlatformUserID <= 0 {
		a.PlatformUserID = nil
	}
	if strings.TrimSpace(string(a.ProxyMode)) == "" {
		a.ProxyMode = ProxyUsageModeInherit
	}
	if a.ProxyMode != ProxyUsageModePool {
		a.ProxyConfigID = nil
	}
	if a.CheckinRandomWindowMinutes < 0 {
		a.CheckinRandomWindowMinutes = 0
	}
}

func (a *SiteAccount) Validate() error {
	if a == nil {
		return fmt.Errorf("site account is nil")
	}
	a.Normalize()
	if a.SiteID == 0 {
		return fmt.Errorf("site id is required")
	}
	if a.Name == "" {
		return fmt.Errorf("site account name is required")
	}
	if err := a.CredentialType.Validate(); err != nil {
		return err
	}
	if err := a.ProxyMode.Validate(true); err != nil {
		return err
	}
	if a.ProxyMode == ProxyUsageModePool && (a.ProxyConfigID == nil || *a.ProxyConfigID <= 0) {
		return fmt.Errorf("proxy config id is required when proxy mode is pool")
	}
	if a.CheckinRandomWindowMinutes < 0 {
		return fmt.Errorf("checkin random window minutes must be greater than or equal to 0")
	}
	if a.CheckinRandomWindowMinutes > 1440 {
		return fmt.Errorf("checkin random window minutes must be less than or equal to 1440")
	}
	if a.PlatformUserID != nil && *a.PlatformUserID <= 0 {
		return fmt.Errorf("platform user id must be greater than 0")
	}
	if a.TokenExpiresAt < 0 {
		return fmt.Errorf("token expires at must be greater than or equal to 0")
	}
	switch a.CredentialType {
	case SiteCredentialTypeUsernamePassword:
		if a.Username == "" || a.Password == "" {
			return fmt.Errorf("username and password are required")
		}
	case SiteCredentialTypeAccessToken:
		if a.AccessToken == "" {
			return fmt.Errorf("access token is required")
		}
	case SiteCredentialTypeAPIKey:
		if a.APIKey == "" {
			return fmt.Errorf("api key is required")
		}
	case SiteCredentialTypeCookie:
		if err := ValidateSiteCheckinCookie(a.Cookie); err != nil {
			return err
		}
	case SiteCredentialTypeLinkedAccount:
		if a.LinkedAccountID == nil || *a.LinkedAccountID <= 0 {
			return fmt.Errorf("select a subscription account for platform checkin")
		}
	}
	return nil
}
