package v2

import (
	"reflect"
	"slices"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
)

// Pinned legacy default: token-mode requests override the check-in site's
// User-Agent with the subscription authentication site's effective User-Agent.
const legacyUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36"

func (b *conversionBuilder) convertHTTPSettings(old model.LegacySite, site *model.CheckinSite) {
	site.CheckinHTTPBody = retirePlaceholders(site.CheckinHTTPBody)
	for _, header := range site.CustomHeader {
		if strings.Contains(header.HeaderValue, "{{") {
			// Common headers were literal in v1; v2 expands placeholders there.
			b.result.issue("blocker", "literal_header_template_conflict", "site", site.ID, 0)
		}
	}
	clean := func(headers []model.CustomHeader, expand bool) []model.CustomHeader {
		result := make([]model.CustomHeader, 0, len(headers))
		for _, header := range headers {
			key := strings.TrimSpace(header.HeaderKey)
			if strings.EqualFold(key, "Cookie") {
				continue
			}
			if strings.EqualFold(key, "Authorization") {
				if !old.CheckinUseSubscriptionToken && strings.TrimSpace(header.HeaderValue) != "" {
					b.result.issue("blocker", "legacy_http_identity_header_conflict", "site", site.ID, 0)
				}
				continue
			}
			header.HeaderKey = key
			if expand {
				header.HeaderValue = retirePlaceholders(header.HeaderValue)
			}
			result = append(result, header)
		}
		return result
	}
	site.CustomHeader = clean(site.CustomHeader, false)
	site.CheckinHTTPHeaders = clean(site.CheckinHTTPHeaders, true)
	if site.CheckinStatus != nil {
		site.CheckinStatus.Body = retirePlaceholders(site.CheckinStatus.Body)
		site.CheckinStatus.Headers = clean(site.CheckinStatus.Headers, true)
	}
	if old.CheckinUseSubscriptionToken && old.LinkedSiteID != nil {
		if source, exists := b.planner.sites[*old.LinkedSiteID]; exists {
			ua := legacyUserAgent
			for _, header := range source.CustomHeader {
				if strings.EqualFold(strings.TrimSpace(header.HeaderKey), "User-Agent") {
					ua = header.HeaderValue
				}
			}
			site.CustomHeader = withHeader(site.CustomHeader, "User-Agent", ua)
			site.CheckinHTTPHeaders = withHeader(site.CheckinHTTPHeaders, "User-Agent", ua)
			if site.CheckinStatus != nil {
				site.CheckinStatus.Headers = withHeader(site.CheckinStatus.Headers, "User-Agent", ua)
			}
		}
	}
}

func retirePlaceholders(value string) string {
	// v1 deliberately expanded these to empty even in subscription-token mode.
	// Keeping them would expose the newly independent credential in v2 requests.
	return strings.NewReplacer("{{access_token}}", "", "{{platform_user_id}}", "", "{{api_key}}", "", "{{password}}", "", "{{refresh_token}}", "").Replace(value)
}

func withHeader(headers []model.CustomHeader, key, value string) []model.CustomHeader {
	result := make([]model.CustomHeader, 0, len(headers)+1)
	for _, header := range headers {
		if !strings.EqualFold(strings.TrimSpace(header.HeaderKey), key) {
			result = append(result, header)
		}
	}
	return append(result, model.CustomHeader{HeaderKey: key, HeaderValue: value})
}

func authHeaders(headers []model.CustomHeader) []model.CustomHeader {
	// Authentication exchanges apply their selected token/cookie themselves.
	// Preserve configured headers, including static cookies used by challenges.
	return slices.Clone(headers)
}

func sameCredentialContext(a, c model.CheckinCredential) bool {
	canonical := func(headers []model.CustomHeader) []model.CustomHeader {
		copy := slices.Clone(headers)
		for i := range copy {
			copy[i].HeaderKey = strings.ToLower(strings.TrimSpace(copy[i].HeaderKey))
		}
		slices.SortFunc(copy, func(a, c model.CustomHeader) int { return compareString(a.HeaderKey, c.HeaderKey) })
		return copy
	}
	return a.Type == c.Type && a.Platform == c.Platform && a.AuthBaseURL == c.AuthBaseURL &&
		reflect.DeepEqual(a.CheckinNetwork, c.CheckinNetwork) && reflect.DeepEqual(canonical(a.AuthHeaders), canonical(c.AuthHeaders))
}

func (b *conversionBuilder) validateTemplateIdentity(site model.LegacySite, account model.LegacySiteAccount, credential model.CheckinCredential) {
	values := []string{site.CheckinHTTPBody}
	for _, header := range site.CheckinHTTPHeaders {
		if !strings.EqualFold(header.HeaderKey, "Cookie") && !strings.EqualFold(header.HeaderKey, "Authorization") {
			values = append(values, header.HeaderValue)
		}
	}
	if site.CheckinStatus != nil {
		values = append(values, site.CheckinStatus.Body)
		for _, header := range site.CheckinStatus.Headers {
			if !strings.EqualFold(header.HeaderKey, "Cookie") && !strings.EqualFold(header.HeaderKey, "Authorization") {
				values = append(values, header.HeaderValue)
			}
		}
	}
	for _, value := range values {
		if strings.Contains(value, "{{username}}") && account.Username != credential.Username ||
			strings.Contains(value, "{{cookie}}") && account.Cookie != credential.Cookie {
			b.result.issue("blocker", "request_template_identity_conflict", "account", account.ID, credential.ID)
			return
		}
	}
}

func legacyCookie(site model.LegacySite, account model.LegacySiteAccount) string {
	if account.Cookie != "" {
		return account.Cookie
	}
	cookie := ""
	for _, header := range site.CustomHeader {
		if strings.EqualFold(strings.TrimSpace(header.HeaderKey), "Cookie") {
			cookie = strings.TrimSpace(header.HeaderValue)
		}
	}
	if account.CredentialType != model.SiteCredentialTypeAPIKey && strings.Contains(account.AccessToken, "=") && !strings.HasPrefix(strings.ToLower(account.AccessToken), "bearer ") {
		cookie = account.AccessToken
	}
	for _, header := range site.CheckinHTTPHeaders {
		if strings.EqualFold(strings.TrimSpace(header.HeaderKey), "Cookie") {
			cookie = strings.ReplaceAll(header.HeaderValue, "{{access_token}}", account.AccessToken)
		}
	}
	if strings.Contains(cookie, "{{") || model.ValidateSiteCheckinCookie(cookie) != nil {
		return ""
	}
	return strings.TrimSpace(cookie)
}
