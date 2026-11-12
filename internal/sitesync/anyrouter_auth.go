package sitesync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
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
	if siteRecord.Platform == model.SitePlatformAgentRouter {
		_, token, err := loginAgentRouter(ctx, siteRecord, account)
		if err == nil && token == "" {
			err = newSiteLoginTokenMissingError()
		}
		return token, err
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
	rememberManagedPlatformUserID(anyRouterExtractUserID(payload), account)

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
		rememberAnyRouterCookieAuth(ctx, siteRecord, account, cookieHeader)
		return cookieHeader, nil
	}
	return "", newSiteLoginTokenMissingError()
}

func anyRouterDiscoverUserID(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string) (int, error) {
	// Use only account-specific evidence. Guessing unrelated IDs can issue
	// dozens of requests and still never include the user's actual ID.
	candidates := []int{firstManagedPlatformUserID(account)}
	candidates = append(candidates, anyRouterCredentialUserIDs(accessToken)...)
	candidates = append(candidates, 0)
	seen := make(map[int]bool)
	for _, userID := range candidates {
		if seen[userID] {
			continue
		}
		seen[userID] = true
		headers := anyRouterAuthHeaders(accessToken, userID)
		payload, _, err := anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet,
			buildSiteURL(siteRecord.BaseURL, "/api/user/self"), nil, headers, account)
		if err != nil && !anyRouterCanTryAlternativeAuth(err) {
			return 0, err
		}
		if err == nil && jsonBool(payload["success"]) {
			if actualID := anyRouterExtractUserID(payload); actualID > 0 {
				rememberManagedPlatformUserID(actualID, account)
				if cookie := headers["Cookie"]; cookie != "" {
					rememberAnyRouterCookieAuth(ctx, siteRecord, account, cookie)
				}
				return actualID, nil
			}
		}
		if anyRouterIsCookieCredential(accessToken) {
			continue
		}
		payload, _, err = anyRouterFetchUserSelfByCookie(ctx, siteRecord, account, accessToken, userID)
		if err != nil && !anyRouterCanTryAlternativeAuth(err) {
			return 0, err
		}
		if actualID := anyRouterExtractUserID(payload); actualID > 0 {
			rememberManagedPlatformUserID(actualID, account)
			return actualID, nil
		}
	}
	return 0, nil
}

func anyRouterFetchUserSelfByCookie(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int) (map[string]any, string, error) {
	requestURL := buildSiteURL(siteRecord.BaseURL, "/api/user/self")
	var lastErr error
	for _, cookie := range anyRouterBuildCookieCandidates(accessToken) {
		headers := map[string]string{"Cookie": cookie}
		anyRouterAddUserIDHeaders(headers, userID)
		payload, cookieHeader, err := anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet, requestURL, nil, headers, account)
		if err != nil {
			if !anyRouterCanTryAlternativeAuth(err) {
				return nil, "", err
			}
			lastErr = err
			continue
		}
		if payload != nil && jsonBool(payload["success"]) && anyRouterExtractUserID(payload) > 0 {
			rememberAnyRouterCookieAuth(ctx, siteRecord, account, cookie)
			return payload, cookieHeader, nil
		}
	}
	return nil, "", lastErr
}

func anyRouterProbeUserIDByCookie(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string) (int, error) {
	requestURL := buildSiteURL(siteRecord.BaseURL, "/api/user/self")
	for _, cookie := range anyRouterBuildCookieCandidates(accessToken) {
		for _, userID := range append([]int{firstManagedPlatformUserID(account), 0}, anyRouterCredentialUserIDs(accessToken)...) {
			headers := map[string]string{"Cookie": cookie}
			anyRouterAddUserIDHeaders(headers, userID)
			payload, _, err := anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet, requestURL, nil, headers, account)
			if err != nil {
				if !anyRouterCanTryAlternativeAuth(err) {
					return 0, err
				}
				continue
			}
			if payload != nil && jsonBool(payload["success"]) && anyRouterExtractUserID(payload) > 0 {
				actualID := anyRouterExtractUserID(payload)
				rememberManagedPlatformUserID(actualID, account)
				rememberAnyRouterCookieAuth(ctx, siteRecord, account, cookie)
				return actualID, nil
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
	headers := map[string]string{}
	if anyRouterIsCookieCredential(accessToken) {
		headers["Cookie"] = stripBearerPrefix(accessToken)
	} else {
		headers["Authorization"] = "Bearer " + stripBearerPrefix(accessToken)
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
	if anyRouterIsCookieCredential(raw) {
		return []string{raw}
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
	parts := strings.Split(stripBearerPrefix(token), ".")
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

func anyRouterCredentialUserIDs(token string) []int {
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
	return candidates
}

// Other management adapters still use their existing fallback candidates.
func anyRouterBuildUserIDProbeCandidates(token string) []int {
	candidates := anyRouterCredentialUserIDs(token)
	for _, value := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 15, 20, 50, 100, 8899, 11494} {
		if !slices.Contains(candidates, value) {
			candidates = append(candidates, value)
		}
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
