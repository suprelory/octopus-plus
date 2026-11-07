package sitesync

import (
	"context"
	"net/http"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
)

func checkinAnyRouter(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, beforeCheckin func(string) error) (*model.SiteCheckinResult, string, error) {
	accessToken, err := resolveAnyRouterManagedAccessToken(ctx, siteRecord, account)
	if err != nil {
		return nil, accessToken, err
	}

	userID, discoverErr := anyRouterDiscoverUserID(ctx, siteRecord, account, accessToken)
	if IsCloudflareProtectionError(discoverErr) {
		return nil, accessToken, discoverErr
	}
	if beforeCheckin != nil {
		if err := beforeCheckin(accessToken); err != nil {
			return nil, accessToken, err
		}
	}
	result, bearerErr := anyRouterTryCheckinWithBearer(ctx, siteRecord, account, accessToken, userID)
	if result != nil {
		return result, accessToken, nil
	} else if bearerErr != nil && !isUnsupportedCheckinError(bearerErr) && !anyRouterShouldFallbackToCookieCheckin(bearerErr.Error()) {
		return nil, accessToken, bearerErr
	}

	result, checkinErr := anyRouterTryCheckinWithCookies(ctx, siteRecord, account, accessToken, userID)
	if result != nil {
		return result, accessToken, nil
	}
	checkinErr = preferCheckinFailure(bearerErr, checkinErr)

	alternateUserID, _ := anyRouterProbeAlternateUserIDByCookie(ctx, siteRecord, account, accessToken, userID)
	if alternateUserID > 0 {
		var alternateErr error
		result, alternateErr = anyRouterTryCheckinWithCookies(ctx, siteRecord, account, accessToken, alternateUserID)
		if result != nil {
			return result, accessToken, nil
		}
		checkinErr = preferCheckinFailure(checkinErr, alternateErr)
	}

	if isUnsupportedCheckinError(checkinErr) {
		return newUnsupportedCheckinResult(""), accessToken, nil
	}
	return nil, accessToken, checkinErr
}

// An unavailable fallback route must not hide an authentication or transient
// failure from another route. Only unanimous, explicit failures prove absence.
func preferCheckinFailure(current, candidate error) error {
	if current == nil || (isUnsupportedCheckinError(current) && candidate != nil && !isUnsupportedCheckinError(candidate)) {
		return candidate
	}
	return current
}

func anyRouterTryCheckinWithBearer(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int) (*model.SiteCheckinResult, error) {
	payload, _, err := anyRouterRequestJSONWithCookies(
		ctx,
		siteRecord,
		http.MethodPost,
		buildSiteURL(siteRecord.BaseURL, "/api/user/checkin"),
		nil,
		anyRouterAuthHeaders(accessToken, userID),
		account,
	)
	if err != nil {
		return nil, err
	}
	if result, ok := anyRouterBuildCheckinResult(payload); ok {
		return result, nil
	}
	if message := anyRouterExtractResponseMessage(payload); message != "" {
		return nil, newSiteBusinessError(message)
	}
	return nil, newSiteBusinessError("checkin returned an unrecognized response")
}

func anyRouterTryCheckinWithCookies(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int) (*model.SiteCheckinResult, error) {
	var firstFailure error
	for _, cookie := range anyRouterBuildCookieCandidates(accessToken) {
		signInPayload, _, signInErr := anyRouterRequestJSONWithCookies(
			ctx,
			siteRecord,
			http.MethodPost,
			buildSiteURL(siteRecord.BaseURL, "/api/user/sign_in"),
			map[string]any{},
			map[string]string{
				"Cookie":           cookie,
				"X-Requested-With": "XMLHttpRequest",
			},
			account,
		)
		if signInErr == nil {
			if result, ok := anyRouterBuildCheckinResult(signInPayload); ok {
				return result, nil
			}
			signInErr = newSiteBusinessError(firstNonEmptyString(anyRouterExtractResponseMessage(signInPayload), "checkin returned an unrecognized response"))
		}
		firstFailure = preferCheckinFailure(firstFailure, signInErr)

		headers := map[string]string{"Cookie": cookie}
		anyRouterAddUserIDHeaders(headers, userID)
		payload, _, err := anyRouterRequestJSONWithCookies(
			ctx,
			siteRecord,
			http.MethodPost,
			buildSiteURL(siteRecord.BaseURL, "/api/user/checkin"),
			nil,
			headers,
			account,
		)
		if err == nil {
			if result, ok := anyRouterBuildCheckinResult(payload); ok {
				return result, nil
			}
			err = newSiteBusinessError(firstNonEmptyString(anyRouterExtractResponseMessage(payload), "checkin returned an unrecognized response"))
		}
		firstFailure = preferCheckinFailure(firstFailure, err)
	}

	if firstFailure == nil {
		firstFailure = newSiteBusinessError("checkin failed")
	}
	return nil, firstFailure
}

func anyRouterBuildCheckinResult(payload map[string]any) (*model.SiteCheckinResult, bool) {
	if payload == nil {
		return nil, false
	}
	message := anyRouterExtractResponseMessage(payload)
	if jsonBool(payload["success"]) || isAlreadyCheckedInMessage(message) {
		return newSuccessfulCheckinResult(message, checkinRewardString(nestedValue(payload, "data", "reward"))), true
	}
	return nil, false
}

func anyRouterShouldFallbackToCookieCheckin(message string) bool {
	text := strings.ToLower(strings.TrimSpace(message))
	if text == "" {
		return true
	}
	return strings.Contains(text, "unexpected token") ||
		strings.Contains(text, "unrecognized response") ||
		strings.Contains(text, "not valid json") ||
		strings.Contains(text, "<html") ||
		strings.Contains(text, "new-api-user") ||
		strings.Contains(text, "access token") ||
		strings.Contains(text, "unauthorized") ||
		strings.Contains(text, "forbidden") ||
		strings.Contains(text, "not login") ||
		strings.Contains(text, "not logged") ||
		strings.Contains(text, "invalid url (post /api/user/checkin)") ||
		(strings.Contains(text, "http 404") && strings.Contains(text, "/api/user/checkin")) ||
		strings.Contains(text, "未登录") ||
		strings.Contains(text, "未提供")
}
