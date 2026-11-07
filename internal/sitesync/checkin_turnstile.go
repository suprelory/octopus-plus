package sitesync

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/turnstile"
)

type turnstileSolverConfig struct {
	enabled bool
	solvers []turnstile.Solver
}

// Replaceable so tests can point the solvers at local servers.
var (
	loadTurnstileSolverConfig = loadTurnstileSolverSettings
	turnstileHTTPClient       = turnstile.NewHTTPClient()
)

// loadTurnstileSolverSettings returns the enabled platforms that have a key,
// in fallback order: Cloudflare Solver, then SolverQ.
func loadTurnstileSolverSettings() turnstileSolverConfig {
	enabled, _ := op.SettingGetBool(model.SettingKeyTurnstileSolverEnabled)
	config := turnstileSolverConfig{enabled: enabled}
	if !enabled {
		return config
	}
	for _, candidate := range []struct {
		platform            turnstile.Platform
		enabledKey, keyName model.SettingKey
	}{
		{turnstile.PlatformCloudflareSolver, model.SettingKeyTurnstileCloudflareSolverEnabled, model.SettingKeyTurnstileCloudflareSolverAPIKey},
		{turnstile.PlatformSolverQ, model.SettingKeyTurnstileSolverQEnabled, model.SettingKeyTurnstileSolverQAPIKey},
	} {
		on, _ := op.SettingGetBool(candidate.enabledKey)
		key, _ := op.SettingGetString(candidate.keyName)
		if key = strings.TrimSpace(key); on && key != "" {
			config.solvers = append(config.solvers, turnstile.Solver{Platform: candidate.platform, APIKey: key})
		}
	}
	return config
}

// isTurnstileRequiredMessage recognizes new-api's TurnstileCheck rejections,
// which run before the check-in handler and answer HTTP 200 with
// success=false: "Turnstile token is empty" and "Turnstile verification
// failed, please refresh and try again!" (Chinese text in older releases).
func isTurnstileRequiredMessage(message string) bool {
	return strings.Contains(strings.ToLower(message), "turnstile")
}

// retryCheckinWithTurnstile retries a check-in that the site rejected for a
// missing Turnstile token once, with a freshly solved token. It returns the
// retried response, or a failure message when no token could be used. Other
// responses, and every response while the solver is disabled, pass through.
func retryCheckinWithTurnstile(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, checkinURL, accessToken string, payload map[string]any) (map[string]any, string, error) {
	message := extractSiteResponseMessage(payload)
	if jsonBool(payload["success"]) || !isTurnstileRequiredMessage(message) {
		return payload, "", nil
	}
	config := loadTurnstileSolverConfig()
	if !config.enabled {
		return payload, "", nil
	}
	fail := func(reason string) (map[string]any, string, error) {
		return nil, message + " (Turnstile solver: " + reason + ")", nil
	}
	if len(config.solvers) == 0 {
		return fail("no enabled solver platform has an API key")
	}
	siteKey, err := fetchTurnstileSiteKey(ctx, siteRecord, account)
	if err != nil {
		// Only an unreachable site keeps the request's own reason. Any other
		// answer, such as a 403 or a missing status route, means the key is
		// unavailable, not that the login expired or check-in is unsupported.
		if isSiteUnreachable(err) {
			return nil, "", err
		}
		return fail("could not read the site key")
	}
	if siteKey == "" {
		return fail("the site did not publish a Turnstile site key")
	}
	// Site keys are bound to the host, so the site root stands in for the page
	// that renders the widget.
	pageURL := strings.TrimRight(strings.TrimSpace(siteRecord.BaseURL), "/") + "/"
	token, err := turnstile.Solve(ctx, turnstileHTTPClient, config.solvers, pageURL, siteKey)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return fail("solving failed: " + err.Error())
	}
	// new-api reads the token from the turnstile query parameter. Tokens are
	// single-use, so every check-in solves its own.
	separator := "?"
	if strings.Contains(checkinURL, "?") {
		separator = "&"
	}
	retried, err := requestJSONWithManagedAccessToken(ctx, siteRecord, http.MethodPost, checkinURL+separator+"turnstile="+url.QueryEscape(token), nil, accessToken, account)
	if err != nil {
		return nil, "", err
	}
	if !jsonBool(retried["success"]) && isTurnstileRequiredMessage(extractSiteResponseMessage(retried)) {
		return fail("the site rejected the solved token")
	}
	return retried, "", nil
}

// fetchTurnstileSiteKey reads the site key new-api publishes for its own pages
// from the public /api/status endpoint.
func fetchTurnstileSiteKey(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount) (string, error) {
	payload, err := requestJSON(ctx, siteRecord, http.MethodGet, buildSiteURL(siteRecord.BaseURL, "/api/status"), nil, nil, account)
	if err != nil {
		return "", err
	}
	return jsonString(nestedValue(payload, "data", "turnstile_site_key")), nil
}

// isSiteUnreachable reports network errors, timeouts, Cloudflare challenges
// and cancellation, classified the same way the check-in records them.
func isSiteUnreachable(err error) bool {
	switch siteBatchReason(sanitizeSiteError(err)) {
	case SiteBatchReasonNetworkError, SiteBatchReasonTimeout, SiteBatchReasonCloudflareProtection,
		SiteBatchReasonContextCanceled, SiteBatchReasonContextDeadlineExceeded:
		return true
	default:
		return false
	}
}
