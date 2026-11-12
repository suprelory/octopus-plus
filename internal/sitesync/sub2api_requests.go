package sitesync

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/suprelory/octopus-plus/internal/model"
)

type sub2APIRecoveryKey struct{}

func withSub2APIRecoveryBudget(ctx context.Context) context.Context {
	if _, ok := ctx.Value(sub2APIRecoveryKey{}).(*atomic.Bool); ok {
		return ctx
	}
	return context.WithValue(ctx, sub2APIRecoveryKey{}, &atomic.Bool{})
}

func claimSub2APIRecovery(ctx context.Context) bool {
	if used, ok := ctx.Value(sub2APIRecoveryKey{}).(*atomic.Bool); ok {
		return used.CompareAndSwap(false, true)
	}
	return true // Standalone request helpers also perform at most one retry.
}

// Only management requests authenticated with the account's login token use
// this helper. API-key model discovery must never rotate the login session.
func requestSub2APIJSON(ctx context.Context, site *model.Site, method, endpoint string, body any, account *model.SiteAccount, token string) (map[string]any, error) {
	if account != nil && account.CredentialType == model.SiteCredentialTypeAccessToken && strings.TrimSpace(account.AccessToken) != "" {
		token = account.AccessToken
	}
	request := func(accessToken string) (map[string]any, error) {
		payload, err := requestJSON(ctx, site, method, buildSiteURL(site.BaseURL, endpoint), body,
			map[string]string{"Authorization": ensureBearer(accessToken)}, account)
		if err == nil && payload != nil {
			if code := int(anyToInt64(payload["code"])); code == http.StatusUnauthorized || code == http.StatusForbidden {
				err = newSiteHTTPError(code, extractSiteResponseMessage(payload)).WithParam("upstreamReason", siteUpstreamReason(payload))
			}
		}
		return payload, err
	}
	payload, err := request(token)
	if permanent := sub2APIPermanentAuthError(err); permanent != nil {
		return nil, permanent
	}
	if !shouldRetrySub2APIAfterRefresh(err, account) {
		return payload, err
	}
	// A mutation can be retried only for a structured rejection by Sub2API's
	// JWT middleware. An ambiguous response or network timeout is not proof
	// that key creation was never executed.
	if method != http.MethodGet && method != http.MethodHead {
		_, reason := sub2APIUpstreamErrorDetails(err)
		if reason != "TOKEN_EXPIRED" && reason != "ACCESS_TOKEN_EXPIRED" && reason != "INVALID_TOKEN" {
			return nil, err
		}
	}
	if !claimSub2APIRecovery(ctx) {
		return payload, err
	}
	rejected := *account
	rejected.AccessToken = token
	refreshed, refreshErr := ensureFreshSub2APIAccessToken(ctx, site, &rejected, true)
	copySub2APICredentials(account, &rejected)
	if refreshErr != nil {
		return nil, refreshErr
	}
	payload, err = request(refreshed)
	if permanent := sub2APIPermanentAuthError(err); permanent != nil {
		return nil, permanent
	}
	return payload, err
}
