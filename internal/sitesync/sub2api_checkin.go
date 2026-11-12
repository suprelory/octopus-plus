package sitesync

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/model"
)

type sub2APICheckinAuthKey struct{}

type sub2APICheckinAuth struct {
	site    *model.Site
	account *model.SiteAccount
	token   string
	err     error
}

func configuredCheckinAuth(ctx context.Context) *sub2APICheckinAuth {
	auth, _ := ctx.Value(sub2APICheckinAuthKey{}).(*sub2APICheckinAuth)
	return auth
}

func configuredCheckinToken(ctx context.Context, fallback string) string {
	if auth := configuredCheckinAuth(ctx); auth != nil {
		return auth.token
	}
	return fallback
}

func configuredCheckinHTTPClient(ctx context.Context, site *model.Site, account *model.SiteAccount) (*http.Client, error) {
	if auth := configuredCheckinAuth(ctx); auth != nil && site.CheckinUseSubscriptionToken {
		return siteHTTPClient(ctx, auth.site, auth.account)
	}
	return siteHTTPClient(ctx, site, account)
}

func applySub2APICheckinUserAgent(ctx context.Context, req *http.Request) {
	if auth := configuredCheckinAuth(ctx); auth != nil {
		userAgent := anyRouterUserAgent
		for _, header := range auth.site.CustomHeader {
			if strings.EqualFold(strings.TrimSpace(header.HeaderKey), "User-Agent") {
				userAgent = header.HeaderValue
			}
		}
		req.Header.Set("User-Agent", userAgent)
	}
}

// Query retry is explicit. The submission function never automatically retries,
// including check-in endpoints configured to use GET.
func requestConfiguredCheckinQuery(ctx context.Context, site *model.Site, account *model.SiteAccount, token string) (*configuredCheckinResponse, string, error) {
	auth := configuredCheckinAuth(ctx)
	if auth == nil || !site.CheckinUseSubscriptionToken {
		return requestConfiguredCheckinHTTP(ctx, site, account, token)
	}
	if auth.err != nil {
		return nil, auth.token, auth.err
	}
	response, _, err := requestConfiguredCheckinHTTP(ctx, site, account, auth.token)
	if err != nil || response.StatusCode != http.StatusUnauthorized {
		return response, auth.token, err
	}
	recovered, err := recoverSub2APICheckinAuth(ctx, response)
	if err != nil {
		return response, auth.token, err
	}
	if recovered {
		response, _, err = requestConfiguredCheckinHTTP(ctx, site, account, auth.token)
		if err == nil && response.StatusCode == http.StatusUnauthorized {
			upstreamErr := formatSiteHTTPError(response.StatusCode, response.Header, response.Body)
			if permanent := sub2APIPermanentAuthError(upstreamErr); permanent != nil {
				auth.err = permanent
				return response, auth.token, permanent
			}
		}
	}
	return response, auth.token, err
}

func recoverSub2APICheckinAuth(ctx context.Context, response *configuredCheckinResponse) (bool, error) {
	auth := configuredCheckinAuth(ctx)
	if auth == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		return false, nil
	}
	upstreamErr := formatSiteHTTPError(response.StatusCode, response.Header, response.Body)
	if permanent := sub2APIPermanentAuthError(upstreamErr); permanent != nil {
		auth.err = permanent
		return false, permanent
	}
	if !shouldRetrySub2APIAfterRefresh(upstreamErr, auth.account) || !claimSub2APIRecovery(ctx) {
		return false, nil
	}
	rejected := *auth.account
	rejected.AccessToken = auth.token
	token, err := ensureFreshSub2APIAccessToken(ctx, auth.site, &rejected, true)
	copySub2APICredentials(auth.account, &rejected)
	if err != nil {
		auth.err = err
		return false, err
	}
	auth.token = token
	return true, nil
}

func transientSub2APICheckinAuthError(err error) bool {
	return isSub2APITransientRefreshError(err) || apperror.Code(err) == sub2APISessionSaveFailed ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func configuredCheckinAuthDetail(ctx context.Context, err error) string {
	if auth := configuredCheckinAuth(ctx); auth != nil {
		return sanitizeCheckinText(err.Error(), auth.account, auth.token, auth.site)
	}
	return sanitizeSiteStatusText(err.Error())
}
