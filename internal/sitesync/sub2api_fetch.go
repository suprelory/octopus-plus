package sitesync

import (
	"context"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/model"
)

func fetchSub2APITokens(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string) ([]model.SiteToken, error) {
	ctx = withSub2APIRecoveryBudget(ctx)
	endpoints := []string{"/api/v1/keys?page=1&page_size=100", "/api/v1/api-keys?page=1&page_size=100", "/api/v1/keys", "/api/v1/api-keys"}
	var firstErr error
	for _, endpoint := range endpoints {
		payload, err := requestSub2APIJSON(ctx, siteRecord, "GET", endpoint, nil, account, accessToken)
		if err != nil {
			if status, _ := sub2APIUpstreamErrorDetails(err); status == 401 || status == 403 || strings.HasPrefix(apperror.Code(err), "site.sub2api.") {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		data, err := unwrapSub2APIData(payload, endpoint)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		items := parseTokenItemsFromAny(data)
		tokens := buildSub2APITokensFromItems(items)
		if len(tokens) > 0 {
			return tokens, nil
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, nil
}

func fetchSub2APIGroups(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, tokens []model.SiteToken) ([]model.SiteUserGroup, error) {
	ctx = withSub2APIRecoveryBudget(ctx)
	inferredGroups := inferSub2APIGroupsFromTokens(tokens)

	endpoints := []string{
		"/api/v1/groups/available",
		"/api/v1/groups?page=1&page_size=100",
		"/api/v1/groups",
		"/api/v1/group?page=1&page_size=100",
		"/api/v1/group",
	}
	var firstErr error
	for _, endpoint := range endpoints {
		payload, err := requestSub2APIJSON(ctx, siteRecord, "GET", endpoint, nil, account, accessToken)
		if err != nil {
			if status, _ := sub2APIUpstreamErrorDetails(err); status == 401 || status == 403 || strings.HasPrefix(apperror.Code(err), "site.sub2api.") {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		data, err := unwrapSub2APIData(payload, endpoint)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		items := parseGroupItemsFromAny(data)
		if len(items) > 0 {
			return items, nil
		}
	}
	if len(inferredGroups) > 0 {
		return inferredGroups, nil
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return []model.SiteUserGroup{{GroupKey: model.SiteDefaultGroupKey, Name: model.SiteDefaultGroupName}}, nil
}

func unwrapSub2APIData(payload map[string]any, endpoint string) (any, error) {
	if payload == nil {
		return nil, apperror.Newf(apperror.CodeSiteSub2APIMissingData, "sub2api %s returned empty response", endpoint)
	}
	if rawCode, ok := payload["code"]; ok {
		code := anyToInt64(rawCode)
		if code != 0 {
			message := firstNonEmptyString(extractSiteResponseMessage(payload), fmt.Sprintf("sub2api %s returned code %d", endpoint, code))
			return nil, apperror.Newf(apperror.CodeSiteSub2APIEnvelopeFailed, "sub2api %s failed: %s", endpoint, message)
		}
		if data, ok := payload["data"]; ok {
			return data, nil
		}
		return nil, apperror.Newf(apperror.CodeSiteSub2APIMissingData, "sub2api %s response missing data", endpoint)
	}
	return payload, nil
}

func buildSub2APITokensFromItems(items []map[string]any) []model.SiteToken {
	tokens := make([]model.SiteToken, 0, len(items))
	for index, item := range items {
		tokenValue := firstNonEmptyString(
			jsonString(item["key"]),
			jsonString(item["token"]),
			jsonString(item["api_key"]),
			jsonString(item["apiKey"]),
			jsonString(item["access_token"]),
			jsonString(item["accessToken"]),
		)
		if tokenValue == "" {
			continue
		}
		groupKey := model.NormalizeSiteGroupKey(firstNonEmptyString(
			jsonString(item["group_id"]),
			jsonString(item["groupId"]),
			jsonString(nestedValue(item, "group", "id")),
			jsonString(item["token_group"]),
			jsonString(item["tokenGroup"]),
			jsonString(item["group_name"]),
			jsonString(item["groupName"]),
			jsonString(nestedValue(item, "group", "name")),
			jsonString(item["group"]),
		))
		groupName := model.NormalizeSiteGroupName(groupKey, firstNonEmptyString(
			jsonString(item["group_name"]),
			jsonString(item["groupName"]),
			jsonString(nestedValue(item, "group", "name")),
			jsonString(item["group"]),
			jsonString(item["token_group"]),
			jsonString(item["tokenGroup"]),
		))
		tokens = append(tokens, model.SiteToken{
			Name:      firstNonEmptyString(strings.TrimSpace(jsonString(item["name"])), fmt.Sprintf("token-%d", index+1)),
			Token:     tokenValue,
			GroupKey:  groupKey,
			GroupName: groupName,
			Enabled:   parseSub2APITokenEnabled(item),
			Source:    "sync",
			IsDefault: index == 0,
		})
	}
	return tokens
}

func parseSub2APITokenEnabled(item map[string]any) bool {
	for _, key := range []string{"enabled", "is_enabled", "isEnabled", "active", "is_active", "isActive", "status"} {
		if raw, ok := item[key]; ok {
			return parseEnabledFlag(raw)
		}
	}
	if raw, ok := item["disabled"]; ok {
		return !parseEnabledFlag(raw)
	}
	return true
}

func inferSub2APIGroupsFromTokens(tokens []model.SiteToken) []model.SiteUserGroup {
	inferredGroups := make([]model.SiteUserGroup, 0)
	seen := make(map[string]struct{})
	for _, token := range tokens {
		key := model.NormalizeSiteGroupKey(token.GroupKey)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		inferredGroups = append(inferredGroups, model.SiteUserGroup{GroupKey: key, Name: model.NormalizeSiteGroupName(key, token.GroupName)})
	}
	return inferredGroups
}

func stripBearerPrefix(token string) string {
	return model.NormalizeSiteAccessToken(token)
}

func fetchSub2APIModelsForSiteToken(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, token model.SiteToken) ([]string, error) {
	tokenValue := strings.TrimSpace(token.Token)
	if tokenValue == "" {
		return nil, apperror.New(apperror.CodeSiteSub2APIModelAPIKeyRequired, "sub2api api key is required for model discovery")
	}

	var firstErr error
	for _, endpoint := range buildSub2APIModelEndpointURLs(siteRecord) {
		payload, err := requestJSON(ctx, siteRecord, "GET", endpoint, nil, map[string]string{"Authorization": ensureBearer(tokenValue)}, account)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		models, err := parseSub2APIModelNames(payload)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(models) > 0 {
			return models, nil
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, nil
}

func buildSub2APIModelEndpointURLs(siteRecord *model.Site) []string {
	if siteRecord == nil {
		return nil
	}
	baseURL := strings.TrimRight(strings.TrimSpace(siteRecord.BaseURL), "/")
	if baseURL == "" {
		return nil
	}
	lower := strings.ToLower(baseURL)
	if strings.HasSuffix(lower, "/models") {
		return []string{baseURL}
	}

	candidates := make([]string, 0, 5)
	appendCandidate := func(url string) {
		url = strings.TrimRight(strings.TrimSpace(url), "/")
		if url == "" {
			return
		}
		for _, existing := range candidates {
			if strings.EqualFold(existing, url) {
				return
			}
		}
		candidates = append(candidates, url)
	}

	if strings.HasSuffix(lower, "/v1") || strings.HasSuffix(lower, "/v1beta") || strings.HasSuffix(lower, "/api/v1") || strings.HasSuffix(lower, "/antigravity/v1") || strings.HasSuffix(lower, "/antigravity/v1beta") {
		appendCandidate(baseURL + "/models")
		return candidates
	}
	if strings.HasSuffix(lower, "/antigravity") {
		appendCandidate(baseURL + "/v1/models")
		appendCandidate(baseURL + "/v1beta/models")
		return candidates
	}

	appendCandidate(baseURL + "/v1/models")
	appendCandidate(baseURL + "/api/v1/models")
	appendCandidate(baseURL + "/v1beta/models")
	appendCandidate(baseURL + "/antigravity/v1/models")
	appendCandidate(baseURL + "/antigravity/v1beta/models")
	appendCandidate(baseURL + "/models")
	return candidates
}

func parseSub2APIModelNames(payload map[string]any) ([]string, error) {
	data, err := unwrapSub2APIData(payload, "/models")
	if err != nil {
		return nil, err
	}
	return normalizeModelNames(collectSub2APIModelNames(data)), nil
}

func collectSub2APIModelNames(value any) []string {
	switch typed := value.(type) {
	case []any:
		names := make([]string, 0, len(typed))
		for _, item := range typed {
			names = append(names, collectSub2APIModelNames(item)...)
		}
		return names
	case []string:
		return typed
	case map[string]any:
		for _, key := range []string{"items", "models", "data", "list", "records", "rows"} {
			if child, ok := typed[key]; ok {
				if names := collectSub2APIModelNames(child); len(names) > 0 {
					return names
				}
			}
		}
		name := firstNonEmptyString(jsonString(typed["id"]), jsonString(typed["name"]), jsonString(typed["model"]), jsonString(typed["model_name"]), jsonString(typed["modelName"]))
		if name != "" {
			return []string{strings.TrimPrefix(name, "models/")}
		}
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			return []string{strings.TrimPrefix(trimmed, "models/")}
		}
	}
	return nil
}
