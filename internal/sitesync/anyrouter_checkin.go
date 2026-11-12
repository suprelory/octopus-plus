package sitesync

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/model"
)

func checkinAnyRouter(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, beforeCheckin func(string) error) (result *model.SiteCheckinResult, accessToken string, runErr error) {
	ctx = withAnyRouterSession(ctx, siteRecord, account)
	defer func() {
		if session := anyRouterSessionFromContext(ctx, siteRecord, account); session != nil {
			accessToken = session.resolvedCredential(siteRecord, accessToken)
			if result != nil {
				result.Message = session.redact(result.Message)
			}
			runErr = session.redactError(runErr)
		}
	}()
	accessToken, err := resolveAnyRouterManagedAccessToken(ctx, siteRecord, account)
	if err != nil {
		return nil, accessToken, err
	}

	userID, discoverErr := anyRouterDiscoverUserID(ctx, siteRecord, account, accessToken)
	if discoverErr != nil {
		return nil, accessToken, discoverErr
	}
	if beforeCheckin != nil {
		if err := beforeCheckin(accessToken); err != nil {
			return nil, accessToken, err
		}
	}
	if verifiedID := firstManagedPlatformUserID(account); verifiedID > 0 {
		userID = verifiedID
	}
	cookieFirst := anyRouterIsCookieCredential(accessToken)
	if session := anyRouterSessionFromContext(ctx, siteRecord, account); session != nil {
		cookieFirst = cookieFirst || session.authCookie() != ""
	}
	var bearerErr error
	if !cookieFirst {
		var result *model.SiteCheckinResult
		result, bearerErr = anyRouterTryCheckinWithBearer(ctx, siteRecord, account, accessToken, userID)
		if result != nil {
			return result, accessToken, nil
		}
		if !anyRouterCanTryAlternativeAuth(bearerErr) {
			return nil, accessToken, bearerErr
		}
	}
	result, checkinErr := anyRouterTryCheckinWithCookies(ctx, siteRecord, account, accessToken, userID)
	if result != nil {
		return result, accessToken, nil
	}
	if !anyRouterCanTryAlternativeAuth(checkinErr) {
		return nil, accessToken, checkinErr
	}
	checkinErr = preferCheckinFailure(bearerErr, checkinErr)
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
	cookies := anyRouterBuildCookieCandidates(accessToken)
	if session := anyRouterSessionFromContext(ctx, siteRecord, account); session != nil && session.authCookie() != "" {
		cookies = []string{session.authCookie()}
	}
	for _, cookie := range cookies {
		headers := map[string]string{"Cookie": cookie, "X-Requested-With": "XMLHttpRequest"}
		anyRouterAddUserIDHeaders(headers, userID)
		signInPayload, _, signInErr := anyRouterRequestJSONWithCookies(
			ctx,
			siteRecord,
			http.MethodPost,
			buildSiteURL(siteRecord.BaseURL, "/api/user/sign_in"),
			map[string]any{},
			headers,
			account,
		)
		if signInErr == nil {
			if result, ok := anyRouterBuildCheckinResult(signInPayload); ok {
				rememberAnyRouterCookieAuth(ctx, siteRecord, account, cookie)
				return result, nil
			}
			signInErr = newSiteBusinessError(firstNonEmptyString(anyRouterExtractResponseMessage(signInPayload), "checkin returned an unrecognized response"))
		}
		if !anyRouterCanTryAlternativeAuth(signInErr) {
			return nil, signInErr
		}
		firstFailure = preferCheckinFailure(firstFailure, signInErr)

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
				rememberAnyRouterCookieAuth(ctx, siteRecord, account, cookie)
				return result, nil
			}
			err = newSiteBusinessError(firstNonEmptyString(anyRouterExtractResponseMessage(payload), "checkin returned an unrecognized response"))
		}
		if !anyRouterCanTryAlternativeAuth(err) {
			return nil, err
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
	if isAlreadyCheckedInMessage(message) {
		return newSuccessfulCheckinResult(message, checkinRewardString(nestedValue(payload, "data", "reward"))), true
	}
	// Every explicit status must agree. Missing/null/string/malformed values
	// must not become an implicit numeric zero or override success:false.
	recognized := false
	if value, exists := payload["success"]; exists {
		if !jsonBool(value) {
			return nil, false
		}
		recognized = true
	}
	for key, expected := range map[string]int{"ret": 1, "code": 0} {
		if value, exists := payload[key]; exists {
			if !anyRouterStatusEquals(value, expected) {
				return nil, false
			}
			recognized = true
		}
	}
	if !recognized {
		return nil, false
	}
	return newSuccessfulCheckinResult(message, checkinRewardString(nestedValue(payload, "data", "reward"))), true
}

func anyRouterStatusEquals(value any, expected int) bool {
	switch typed := value.(type) {
	case float64:
		return typed == float64(expected)
	case int:
		return typed == expected
	case json.Number:
		number, err := typed.Int64()
		return err == nil && number == int64(expected)
	default:
		return false
	}
}

// Only a definite route/authentication mismatch justifies another POST. An
// unknown response, timeout, challenge, 429 or 5xx may follow a committed action.
func anyRouterCanTryAlternativeAuth(err error) bool {
	if err == nil || IsCloudflareProtectionError(err) {
		return false
	}
	if isUnsupportedCheckinError(err) {
		return true
	}
	if apperror.IsCode(err, CodeSiteUpstreamHTTPError) {
		status := siteErrorStatusCode(err)
		return status == http.StatusUnauthorized || status == http.StatusForbidden
	}
	if !apperror.IsCode(err, CodeSiteUpstreamBusinessError) {
		return false
	}
	message := apperror.Message(err)
	text := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(text, "new-api-user") ||
		strings.Contains(text, "access token") ||
		strings.Contains(text, "unauthorized") ||
		strings.Contains(text, "forbidden") ||
		strings.Contains(text, "not login") ||
		strings.Contains(text, "not logged") ||
		strings.Contains(text, "invalid token") ||
		strings.Contains(text, "session expired") ||
		strings.Contains(text, "未登录") ||
		strings.Contains(text, "未提供") ||
		strings.Contains(text, "登录失效")
}

func anyRouterRequestMustStop(err error) bool {
	if err == nil {
		return false
	}
	status := siteErrorStatusCode(err)
	return IsCloudflareProtectionError(err) || status == http.StatusTooManyRequests || status >= 500
}
