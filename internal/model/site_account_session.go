package model

import "strings"

// HasSameAccessTokenSession ignores settings such as expiry hints and proxies.
// Editing those settings must not abandon an in-flight refresh token exchange.
func (a *SiteAccount) HasSameAccessTokenSession(other *SiteAccount) bool {
	return a != nil && other != nil && a.SiteID == other.SiteID &&
		a.CredentialType == SiteCredentialTypeAccessToken && other.CredentialType == SiteCredentialTypeAccessToken &&
		NormalizeSiteAccessToken(a.AccessToken) == NormalizeSiteAccessToken(other.AccessToken) &&
		strings.TrimSpace(a.RefreshToken) == strings.TrimSpace(other.RefreshToken)
}

func NormalizeSiteAccessToken(token string) string {
	trimmed := strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(trimmed), "bearer ") {
		return strings.TrimSpace(trimmed[7:])
	}
	return trimmed
}
