package op

import (
	"net/url"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
)

type rawImportObject map[string]any

type importedSiteInput struct {
	Name     string
	Platform model.SitePlatform
	BaseURL  string
}

type importedAccountInput struct {
	Site           importedSiteInput
	Name           string
	CredentialType model.SiteCredentialType
	Username       string
	Password       string
	AccessToken    string
	APIKey         string
	RefreshToken   string
	TokenExpiresAt int64
	PlatformUserID *int
	AccountProxy   *string
	Enabled        bool
	AutoSync       bool
	AutoCheckin    bool
	Balance        float64
	BalanceUsed    float64
}

var supportedImportPlatforms = map[string]model.SitePlatform{
	"new-api":   model.SitePlatformNewAPI,
	"newapi":    model.SitePlatformNewAPI,
	"one-api":   model.SitePlatformOneAPI,
	"oneapi":    model.SitePlatformOneAPI,
	"anyrouter": model.SitePlatformAnyRouter,
	"one-hub":   model.SitePlatformOneHub,
	"onehub":    model.SitePlatformOneHub,
	"done-hub":  model.SitePlatformDoneHub,
	"donehub":   model.SitePlatformDoneHub,
	"sub2api":   model.SitePlatformSub2API,
	"openai":    model.SitePlatformAPI,
	"anthropic": model.SitePlatformAPI,
	"claude":    model.SitePlatformAPI,
	"google":    model.SitePlatformAPI,
	"gemini":    model.SitePlatformAPI,
	"api":       model.SitePlatformAPI,
}

var unsupportedImportHints = []string{
	"codex",
	"gemini-cli",
	"cliproxyapi",
	"veloera",
}

var directImportPlatforms = map[model.SitePlatform]struct{}{
	model.SitePlatformAPI: {},
}

func normalizeImportBaseURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return strings.TrimRight(parsed.Scheme+"://"+parsed.Host, "/")
	}
	return strings.TrimRight(trimmed, "/")
}

func resolveImportedPlatform(rawPlatform any, rawURL string) (model.SitePlatform, bool) {
	if hinted, unsupported := detectSupportedPlatform(rawPlatform, rawURL); unsupported {
		return "", false
	} else if hinted != "" {
		return hinted, true
	}

	value := strings.ToLower(strings.TrimSpace(asString(rawPlatform)))
	if platform, ok := supportedImportPlatforms[value]; ok {
		return platform, true
	}
	if strings.Contains(value, "wong") {
		return model.SitePlatformNewAPI, true
	}
	if strings.Contains(value, "done") {
		return model.SitePlatformDoneHub, true
	}
	if strings.Contains(value, "anyrouter") {
		return model.SitePlatformAnyRouter, true
	}
	if value == "" {
		return model.SitePlatformNewAPI, true
	}
	return model.SitePlatformNewAPI, true
}

func resolveImportedProfilePlatform(rawType any, baseURL string) (model.SitePlatform, bool) {
	if hinted, unsupported := detectSupportedPlatform(rawType, baseURL); unsupported {
		return "", false
	} else if hinted != "" {
		return hinted, true
	}

	switch strings.ToLower(strings.TrimSpace(asString(rawType))) {
	case "openai", "openai-compatible", "":
		return model.SitePlatformAPI, true
	case "anthropic":
		return model.SitePlatformAPI, true
	case "google":
		return model.SitePlatformAPI, true
	default:
		return model.SitePlatformAPI, true
	}
}

func detectSupportedPlatform(values ...any) (model.SitePlatform, bool) {
	joined := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.ToLower(strings.TrimSpace(asString(value)))
		if text != "" {
			joined = append(joined, text)
		}
	}
	combined := strings.Join(joined, " ")
	for _, hint := range unsupportedImportHints {
		if strings.Contains(combined, hint) {
			return "", true
		}
	}

	switch {
	case strings.Contains(combined, "api.openai.com"):
		return model.SitePlatformAPI, false
	case strings.Contains(combined, "api.anthropic.com"), strings.Contains(combined, "anthropic.com/v1"):
		return model.SitePlatformAPI, false
	case strings.Contains(combined, "generativelanguage.googleapis.com"),
		strings.Contains(combined, "googleapis.com/v1beta/openai"),
		strings.Contains(combined, "gemini.google.com"):
		return model.SitePlatformAPI, false
	case strings.Contains(combined, "anyrouter"):
		return model.SitePlatformAnyRouter, false
	case strings.Contains(combined, "donehub"), strings.Contains(combined, "done-hub"):
		return model.SitePlatformDoneHub, false
	case strings.Contains(combined, "onehub"), strings.Contains(combined, "one-hub"):
		return model.SitePlatformOneHub, false
	case strings.Contains(combined, "oneapi"), strings.Contains(combined, "one-api"):
		return model.SitePlatformOneAPI, false
	case strings.Contains(combined, "sub2api"):
		return model.SitePlatformSub2API, false
	}
	return "", false
}

func isDirectImportPlatform(platform model.SitePlatform) bool {
	_, ok := directImportPlatforms[platform]
	return ok
}
