package sitesync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
)

func resolveAnyRouterManagedAccessToken(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount) (string, error) {
	if account.CredentialType == model.SiteCredentialTypeAccessToken {
		token := strings.TrimSpace(account.AccessToken)
		if token == "" {
			return "", newAccessTokenRequiredError()
		}
		return token, nil
	}
	if account.CredentialType != model.SiteCredentialTypeUsernamePassword {
		return "", fmt.Errorf("managed access token is not available for credential type %s", account.CredentialType)
	}

	payload, cookieHeader, err := anyRouterRequestJSONWithCookies(
		ctx,
		siteRecord,
		http.MethodPost,
		buildSiteURL(siteRecord.BaseURL, "/api/user/login"),
		map[string]any{"username": account.Username, "password": account.Password},
		map[string]string{"X-Requested-With": "XMLHttpRequest"},
		account,
	)
	if err != nil {
		return "", err
	}
	if payload == nil {
		return "", wrapSiteDecodeError("login response is empty", nil)
	}
	if !jsonBool(payload["success"]) {
		return "", newSiteLoginFailedError(firstNonEmptyString(anyRouterExtractResponseMessage(payload), "login failed"))
	}

	for _, candidate := range []string{
		jsonString(payload["data"]),
		jsonString(payload["token"]),
		jsonString(payload["access_token"]),
		jsonString(payload["accessToken"]),
		jsonString(nestedValue(payload, "data", "token")),
		jsonString(nestedValue(payload, "data", "access_token")),
		jsonString(nestedValue(payload, "data", "accessToken")),
	} {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate), nil
		}
	}
	if anyRouterHasUsableSessionCookie(cookieHeader) {
		return cookieHeader, nil
	}
	return "", newSiteLoginTokenMissingError()
}

func anyRouterDiscoverUserID(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string) (int, error) {
	if jwtID := anyRouterTryDecodeJWTUserID(accessToken); jwtID > 0 {
		if ok, err := anyRouterTestBearerUserID(ctx, siteRecord, account, accessToken, jwtID); IsCloudflareProtectionError(err) {
			return 0, err
		} else if ok {
			return jwtID, nil
		}
	}

	payload, _, err := anyRouterRequestJSONWithCookies(
		ctx,
		siteRecord,
		http.MethodGet,
		buildSiteURL(siteRecord.BaseURL, "/api/user/self"),
		nil,
		map[string]string{"Authorization": "Bearer " + strings.TrimSpace(accessToken)},
		account,
	)
	if IsCloudflareProtectionError(err) {
		return 0, err
	}
	if err == nil {
		if userID := anyRouterExtractUserID(payload); userID > 0 {
			return userID, nil
		}
	}

	for _, userID := range anyRouterBuildUserIDProbeCandidates(accessToken) {
		if ok, err := anyRouterTestBearerUserID(ctx, siteRecord, account, accessToken, userID); IsCloudflareProtectionError(err) {
			return 0, err
		} else if ok {
			return userID, nil
		}
	}

	if payload, _, cookieErr := anyRouterFetchUserSelfByCookie(ctx, siteRecord, account, accessToken, 0); IsCloudflareProtectionError(cookieErr) {
		return 0, cookieErr
	} else if cookieErr == nil {
		if userID := anyRouterExtractUserID(payload); userID > 0 {
			return userID, nil
		}
	}

	if userID, probeErr := anyRouterProbeUserIDByCookie(ctx, siteRecord, account, accessToken); userID > 0 || probeErr != nil {
		return userID, probeErr
	}

	return 0, nil
}

func anyRouterTestBearerUserID(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int) (bool, error) {
	payload, _, err := anyRouterRequestJSONWithCookies(
		ctx,
		siteRecord,
		http.MethodGet,
		buildSiteURL(siteRecord.BaseURL, "/api/user/self"),
		nil,
		anyRouterAuthHeaders(accessToken, userID),
		account,
	)
	if err != nil {
		return false, err
	}
	return payload != nil && jsonBool(payload["success"]) && anyRouterExtractUserID(payload) > 0, nil
}

func anyRouterFetchUserSelfByCookie(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int) (map[string]any, string, error) {
	requestURL := buildSiteURL(siteRecord.BaseURL, "/api/user/self")
	for _, cookie := range anyRouterBuildCookieCandidates(accessToken) {
		headers := map[string]string{"Cookie": cookie}
		anyRouterAddUserIDHeaders(headers, userID)
		payload, cookieHeader, err := anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet, requestURL, nil, headers, account)
		if IsCloudflareProtectionError(err) {
			return nil, "", err
		}
		if err != nil {
			continue
		}
		if payload != nil && jsonBool(payload["success"]) && anyRouterExtractUserID(payload) > 0 {
			return payload, cookieHeader, nil
		}
	}
	return nil, "", nil
}

func anyRouterProbeUserIDByCookie(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string) (int, error) {
	requestURL := buildSiteURL(siteRecord.BaseURL, "/api/user/self")
	for _, cookie := range anyRouterBuildCookieCandidates(accessToken) {
		for _, userID := range anyRouterBuildUserIDProbeCandidates(accessToken) {
			headers := map[string]string{"Cookie": cookie}
			anyRouterAddUserIDHeaders(headers, userID)
			payload, _, err := anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet, requestURL, nil, headers, account)
			if IsCloudflareProtectionError(err) {
				return 0, err
			}
			if err != nil {
				continue
			}
			if payload != nil && jsonBool(payload["success"]) && anyRouterExtractUserID(payload) > 0 {
				return userID, nil
			}
		}
	}
	return 0, nil
}

func anyRouterProbeAlternateUserIDByCookie(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, currentUserID int) (int, error) {
	probed, err := anyRouterProbeUserIDByCookie(ctx, siteRecord, account, accessToken)
	if err != nil {
		return 0, err
	}
	if probed <= 0 || probed == currentUserID {
		return 0, nil
	}
	return probed, nil
}

func anyRouterExtractUserID(payload map[string]any) int {
	if payload == nil {
		return 0
	}
	switch typed := nestedValue(payload, "data", "id").(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case string:
		value, _ := strconv.Atoi(strings.TrimSpace(typed))
		return value
	default:
		return 0
	}
}

func anyRouterAuthHeaders(accessToken string, userID int) map[string]string {
	headers := map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(accessToken),
	}
	anyRouterAddUserIDHeaders(headers, userID)
	return headers
}

func anyRouterAddUserIDHeaders(headers map[string]string, userID int) {
	if headers == nil || userID <= 0 {
		return
	}
	value := strconv.Itoa(userID)
	headers["New-API-User"] = value
	headers["Veloera-User"] = value
	headers["voapi-user"] = value
	headers["User-id"] = value
	headers["Rix-Api-User"] = value
	headers["neo-api-user"] = value
}

func anyRouterBuildCookieCandidates(token string) []string {
	raw := strings.TrimSpace(token)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(raw), "bearer ") {
		raw = strings.TrimSpace(raw[7:])
	}
	seen := make(map[string]struct{}, 3)
	candidates := make([]string, 0, 3)
	appendCandidate := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		candidates = append(candidates, value)
	}
	if strings.Contains(raw, "=") {
		appendCandidate(raw)
	}
	appendCandidate("session=" + raw)
	appendCandidate("token=" + raw)
	return candidates
}

func anyRouterTryDecodeJWTUserID(token string) int {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return 0
	}
	payloadBytes, ok := anyRouterDecodeBase64String(parts[1])
	if !ok {
		return 0
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadBytes), &payload); err != nil {
		return 0
	}
	if id := anyRouterParseInt(payload["id"]); id > 0 {
		return id
	}
	return anyRouterParseInt(payload["sub"])
}

func anyRouterBuildUserIDProbeCandidates(token string) []int {
	seen := make(map[int]struct{})
	candidates := make([]int, 0, 18)
	appendCandidate := func(value int) {
		if value <= 0 || value > 10_000_000 {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		candidates = append(candidates, value)
	}
	appendCandidate(anyRouterTryDecodeJWTUserID(token))
	for _, value := range anyRouterExtractLikelyUserIDs(token) {
		appendCandidate(value)
	}
	for _, value := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 15, 20, 50, 100, 8899, 11494} {
		appendCandidate(value)
	}
	return candidates
}

func anyRouterParseInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(typed))
		return n
	default:
		return 0
	}
}
