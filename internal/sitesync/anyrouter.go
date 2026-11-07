package sitesync

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
)

func syncAnyRouter(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount) (*syncSnapshot, error) {
	if account.CredentialType == model.SiteCredentialTypeAPIKey {
		return syncWithDirectToken(ctx, siteRecord, account, resolveDirectToken(account), "manual")
	}

	accessToken, err := resolveAnyRouterManagedAccessToken(ctx, siteRecord, account)
	if err != nil {
		return nil, err
	}

	userID, _ := anyRouterDiscoverUserID(ctx, siteRecord, account, accessToken)
	tokens, err := fetchAnyRouterManagementTokens(ctx, siteRecord, account, accessToken, userID)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 && account.CredentialType == model.SiteCredentialTypeAccessToken && strings.TrimSpace(account.AccessToken) != "" {
		tokens = append(tokens, model.SiteToken{
			Name:      "default",
			Token:     strings.TrimSpace(account.AccessToken),
			GroupKey:  model.SiteDefaultGroupKey,
			GroupName: model.SiteDefaultGroupName,
			Enabled:   true,
			Source:    "access_token_fallback",
			IsDefault: true,
		})
	}
	if len(tokens) == 0 && strings.TrimSpace(account.APIKey) != "" {
		tokens = append(tokens, model.SiteToken{
			Name:      "default",
			Token:     strings.TrimSpace(account.APIKey),
			GroupKey:  model.SiteDefaultGroupKey,
			GroupName: model.SiteDefaultGroupName,
			Enabled:   true,
			Source:    "fallback",
			IsDefault: true,
		})
	}
	if len(tokens) == 0 {
		return nil, newMissingGroupKeyError(model.SiteDefaultGroupKey)
	}

	groups, err := fetchAnyRouterManagementGroups(ctx, siteRecord, account, accessToken, userID)
	if err != nil {
		groups = nil
	}
	groups = mergeSiteGroups(groups, tokens)

	siteModels, tokenGroupResults := syncSiteModelsByGroup(
		ctx,
		siteRecord,
		account,
		accessToken,
		pickModelTokensByGroup(tokens),
		userID,
		siteModelSourceSync,
		func(token model.SiteToken, allowGlobalFallback bool) (siteModelFetchResult, error) {
			models, err := fetchModelsForSiteToken(ctx, siteRecord, account, token)
			if (err != nil || len(models) == 0) && allowGlobalFallback {
				fallbackModels, fallbackErr := fetchAnyRouterSessionModels(ctx, siteRecord, account, accessToken, userID)
				if fallbackErr == nil && len(fallbackModels) > 0 {
					return siteModelFetchResult{names: fallbackModels, source: siteModelSourceSync, authoritative: true, message: fmt.Sprintf("同步到 %d 个模型", len(fallbackModels))}, nil
				}
			}
			message := "上游当前没有可用模型"
			if len(models) > 0 {
				message = fmt.Sprintf("同步到 %d 个模型", len(models))
			}
			return siteModelFetchResult{names: models, source: siteModelSourceSync, authoritative: err == nil, message: message}, err
		},
	)
	siteModels = expandExplicitGroupModelsToGroups(siteModels, groups, tokens)
	groupResults := finalizeSiteGroupSyncResults(account, groups, tokens, siteModels, tokenGroupResults)
	status := buildSyncSnapshotStatus(groupResults)
	balance, balanceUsed, todayIncome := fetchSiteAccountBalance(ctx, siteRecord, account, accessToken, userID)
	message := buildSyncSnapshotMessage(groupResults)
	snapshot := &syncSnapshot{
		accessToken:  accessToken,
		groups:       groups,
		tokens:       tokens,
		models:       siteModels,
		groupResults: groupResults,
		status:       status,
		balance:      balance,
		balanceUsed:  balanceUsed,
		todayIncome:  todayIncome,
		message:      message,
	}
	if status == model.SiteExecutionStatusFailed {
		return snapshot, buildSyncSnapshotFailure(groupResults)
	}
	return snapshot, nil
}

func fetchAnyRouterManagementTokens(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int) ([]model.SiteToken, error) {
	requestURL := buildSiteURL(siteRecord.BaseURL, "/api/token/?p=0&size=100")

	payload, _, err := anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet, requestURL, nil, anyRouterAuthHeaders(accessToken, userID), account)
	if err == nil {
		err = validateSiteBusinessResponse(payload)
		if err == nil {
			return buildSiteTokensFromPayload(payload), nil
		}
	}
	if IsCloudflareProtectionError(err) {
		return nil, err
	}

	cookieTokens, cookieErr := fetchAnyRouterTokensByCookie(ctx, siteRecord, account, accessToken, userID)
	if len(cookieTokens) > 0 {
		return cookieTokens, nil
	}
	if cookieErr != nil {
		return nil, cookieErr
	}
	return nil, err
}

func fetchAnyRouterManagementGroups(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int) ([]model.SiteUserGroup, error) {
	endpoints := []string{"/api/user/self/groups", "/api/user_group_map"}
	seen := make(map[string]model.SiteUserGroup)
	var terminalErr error

	for _, endpoint := range endpoints {
		payload, _, err := anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet, buildSiteURL(siteRecord.BaseURL, endpoint), nil, anyRouterAuthHeaders(accessToken, userID), account)
		if err != nil {
			continue
		}
		if payload != nil && !jsonBool(payload["success"]) {
			if message := anyRouterResolveGroupFetchErrorMessage(payload); message != "" {
				terminalErr = fmt.Errorf("%s", message)
			}
		}
		for _, group := range parseGroupItems(payload) {
			key := model.NormalizeSiteGroupKey(group.GroupKey)
			group.GroupKey = key
			group.Name = model.NormalizeSiteGroupName(key, group.Name)
			group.RawPayload = marshalRawPayload(payload)
			seen[key] = group
		}
	}
	if len(seen) > 0 {
		return anyRouterGroupMapToSlice(seen), nil
	}

	cookieGroups, cookieErr := fetchAnyRouterGroupsByCookie(ctx, siteRecord, account, accessToken, userID)
	if len(cookieGroups) > 0 {
		return cookieGroups, nil
	}
	if cookieErr != nil {
		return nil, cookieErr
	}
	if terminalErr != nil {
		return nil, terminalErr
	}
	return []model.SiteUserGroup{{GroupKey: model.SiteDefaultGroupKey, Name: model.SiteDefaultGroupName}}, nil
}

func fetchAnyRouterSessionModels(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int) ([]string, error) {
	requestURL := buildSiteURL(siteRecord.BaseURL, "/api/user/models")

	payload, _, err := anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet, requestURL, nil, anyRouterAuthHeaders(accessToken, userID), account)
	if err == nil {
		err = validateSiteBusinessResponse(payload)
		if err == nil {
			return anyRouterParseModelNames(payload), nil
		}
	}

	for _, cookie := range anyRouterBuildCookieCandidates(accessToken) {
		headers := map[string]string{"Cookie": cookie}
		anyRouterAddUserIDHeaders(headers, userID)
		payload, _, requestErr := anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet, requestURL, nil, headers, account)
		if requestErr != nil {
			continue
		}
		if validateSiteBusinessResponse(payload) == nil {
			return anyRouterParseModelNames(payload), nil
		}
	}
	return nil, err
}

func fetchAnyRouterTokensByCookie(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int) ([]model.SiteToken, error) {
	requestURL := buildSiteURL(siteRecord.BaseURL, "/api/token/?p=0&size=100")
	tryUserIDs := []int{userID}
	if alternateUserID, _ := anyRouterProbeAlternateUserIDByCookie(ctx, siteRecord, account, accessToken, userID); alternateUserID > 0 {
		tryUserIDs = append(tryUserIDs, alternateUserID)
	}
	tryUserIDs = slices.Compact(tryUserIDs)

	var firstErr error
	for _, candidateUserID := range tryUserIDs {
		for _, cookie := range anyRouterBuildCookieCandidates(accessToken) {
			headers := map[string]string{"Cookie": cookie}
			anyRouterAddUserIDHeaders(headers, candidateUserID)
			payload, _, err := anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet, requestURL, nil, headers, account)
			if err == nil {
				err = validateSiteBusinessResponse(payload)
			}
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			return buildSiteTokensFromPayload(payload), nil
		}
	}
	return nil, firstErr
}

func fetchAnyRouterGroupsByCookie(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, userID int) ([]model.SiteUserGroup, error) {
	endpoints := []string{"/api/user/self/groups", "/api/user_group_map"}
	tryUserIDs := []int{userID}
	if alternateUserID, _ := anyRouterProbeAlternateUserIDByCookie(ctx, siteRecord, account, accessToken, userID); alternateUserID > 0 {
		tryUserIDs = append(tryUserIDs, alternateUserID)
	}
	tryUserIDs = slices.Compact(tryUserIDs)

	seen := make(map[string]model.SiteUserGroup)
	var terminalErr error

	for _, candidateUserID := range tryUserIDs {
		for _, endpoint := range endpoints {
			requestURL := buildSiteURL(siteRecord.BaseURL, endpoint)
			for _, cookie := range anyRouterBuildCookieCandidates(accessToken) {
				headers := map[string]string{"Cookie": cookie}
				anyRouterAddUserIDHeaders(headers, candidateUserID)
				payload, _, err := anyRouterRequestJSONWithCookies(ctx, siteRecord, http.MethodGet, requestURL, nil, headers, account)
				if err != nil {
					continue
				}
				if payload != nil && !jsonBool(payload["success"]) {
					if message := anyRouterResolveGroupFetchErrorMessage(payload); message != "" {
						terminalErr = fmt.Errorf("%s", message)
					}
				}
				for _, group := range parseGroupItems(payload) {
					key := model.NormalizeSiteGroupKey(group.GroupKey)
					group.GroupKey = key
					group.Name = model.NormalizeSiteGroupName(key, group.Name)
					group.RawPayload = marshalRawPayload(payload)
					seen[key] = group
				}
			}
		}
	}
	if len(seen) > 0 {
		return anyRouterGroupMapToSlice(seen), nil
	}
	return nil, terminalErr
}

func buildSiteTokensFromPayload(payload map[string]any) []model.SiteToken {
	items := parseTokenItems(payload)
	tokens := make([]model.SiteToken, 0, len(items))
	for index, item := range items {
		tokenValue := strings.TrimSpace(jsonString(item["key"]))
		if tokenValue == "" {
			continue
		}
		groupKey := model.NormalizeSiteGroupKey(firstNonEmptyString(
			jsonString(item["group"]),
			jsonString(item["token_group"]),
			jsonString(item["group_name"]),
		))
		groupName := model.NormalizeSiteGroupName(groupKey, firstNonEmptyString(
			jsonString(item["group_name"]),
			jsonString(item["group"]),
			jsonString(item["token_group"]),
		))
		tokens = append(tokens, model.SiteToken{
			Name:      firstNonEmptyString(strings.TrimSpace(jsonString(item["name"])), fmt.Sprintf("token-%d", index+1)),
			Token:     tokenValue,
			GroupKey:  groupKey,
			GroupName: groupName,
			Enabled:   parseEnabledFlag(item["status"]),
			Source:    "sync",
			IsDefault: index == 0,
		})
	}
	return tokens
}

func anyRouterParseModelNames(payload map[string]any) []string {
	if payload == nil {
		return nil
	}
	if values, ok := nestedValue(payload, "data").([]any); ok {
		names := make([]string, 0, len(values))
		for _, value := range values {
			if name := strings.TrimSpace(fmt.Sprint(value)); name != "" && name != "<nil>" {
				names = append(names, name)
			}
		}
		return normalizeModelNames(names)
	}
	if dataMap, ok := nestedValue(payload, "data").(map[string]any); ok {
		names := make([]string, 0, len(dataMap))
		for key := range dataMap {
			if trimmed := strings.TrimSpace(key); trimmed != "" {
				names = append(names, trimmed)
			}
		}
		return normalizeModelNames(names)
	}
	return nil
}

func anyRouterGroupMapToSlice(items map[string]model.SiteUserGroup) []model.SiteUserGroup {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]model.SiteUserGroup, 0, len(keys))
	for _, key := range keys {
		result = append(result, items[key])
	}
	return result
}
