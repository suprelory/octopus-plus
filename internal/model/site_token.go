package model

import "strings"

func NormalizeSiteSyncTokenValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "sk-") {
		return trimmed
	}
	return "sk-" + trimmed
}

// usesSyncTokenSkPrefix reports whether the platform follows the new-api family
// convention where tokens are surfaced without the "sk-" prefix but must carry
// it on upstream requests. Direct provider platforms (OpenAI/Claude/Gemini) use
// their keys verbatim, so they must never have a prefix forced on them.
func (p SitePlatform) usesSyncTokenSkPrefix() bool {
	switch p {
	case SitePlatformAPI:
		return false
	default:
		return true
	}
}

// NormalizeSiteSyncTokenValueForPlatform normalizes a sync token for upstream
// use according to the platform convention: new-api family platforms get the
// "sk-" prefix added when missing, while direct provider platforms keep the
// value verbatim (trimmed only).
func NormalizeSiteSyncTokenValueForPlatform(platform SitePlatform, value string) string {
	if platform.usesSyncTokenSkPrefix() {
		return NormalizeSiteSyncTokenValue(value)
	}
	return strings.TrimSpace(value)
}

func NormalizeComparableSiteTokenValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) >= 3 && strings.EqualFold(trimmed[:3], "sk-") {
		return strings.TrimSpace(trimmed[3:])
	}
	return trimmed
}

func IsMaskedSiteTokenValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	return strings.Contains(trimmed, "*") || strings.Contains(trimmed, "•")
}

// SiteMaskedTokenMatches reports whether fullToken can plausibly produce
// maskedToken under the upstream masking scheme. Both sides are normalised so
// that an optional "sk-" prefix on either value does not break the comparison.
func SiteMaskedTokenMatches(fullToken string, maskedToken string) bool {
	normalizedFull := NormalizeComparableSiteTokenValue(fullToken)
	normalizedMasked := NormalizeComparableSiteTokenValue(maskedToken)
	if normalizedFull == "" || normalizedMasked == "" {
		return false
	}
	if !IsMaskedSiteTokenValue(normalizedMasked) {
		return normalizedFull == normalizedMasked
	}
	firstMask := strings.IndexAny(normalizedMasked, "*•")
	if firstMask < 0 {
		return normalizedFull == normalizedMasked
	}
	lastMask := strings.LastIndexAny(normalizedMasked, "*•")
	if lastMask < firstMask {
		return normalizedFull == normalizedMasked
	}
	maskedRunes := []rune(normalizedMasked)
	runeFirst := len([]rune(normalizedMasked[:firstMask]))
	runeLast := len([]rune(normalizedMasked[:lastMask])) + 1
	prefix := string(maskedRunes[:runeFirst])
	suffix := string(maskedRunes[runeLast:])
	if prefix == "" && suffix == "" {
		return false
	}
	if len(normalizedFull) < len(prefix)+len(suffix)+1 {
		return false
	}
	if prefix != "" && !strings.HasPrefix(normalizedFull, prefix) {
		return false
	}
	if suffix != "" && !strings.HasSuffix(normalizedFull, suffix) {
		return false
	}
	return true
}

func NormalizeSiteTokenValueStatus(value SiteTokenValueStatus, token string) SiteTokenValueStatus {
	if IsMaskedSiteTokenValue(token) {
		return SiteTokenValueStatusMaskedPending
	}
	_ = value
	return SiteTokenValueStatusReady
}

func IsReadySiteToken(token SiteToken) bool {
	return NormalizeSiteTokenValueStatus(token.ValueStatus, token.Token) == SiteTokenValueStatusReady
}
