package sitesync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/suprelory/octopus-plus/internal/helper"
	"github.com/suprelory/octopus-plus/internal/model"
)

const (
	siteModelSourceSync         = "sync"
	siteModelSourceSyncFallback = "sync_fallback"
)

type siteModelFetchResult struct {
	names         []string
	source        string
	detections    map[string]siteModelRouteDetection
	authoritative bool
	message       string
}

func newSiteModelFetchResult(names []string, err error) siteModelFetchResult {
	message := "上游当前没有可用模型"
	if len(names) > 0 {
		message = fmt.Sprintf("同步到 %d 个模型", len(names))
	}
	return siteModelFetchResult{names: names, source: siteModelSourceSync, authoritative: err == nil, message: message}
}

func defaultSiteToken(value, source string) model.SiteToken {
	return model.SiteToken{
		Name:      "default",
		Token:     strings.TrimSpace(value),
		GroupKey:  model.SiteDefaultGroupKey,
		GroupName: model.SiteDefaultGroupName,
		Enabled:   true,
		Source:    source,
		IsDefault: true,
	}
}

func withFallbackSiteToken(tokens []model.SiteToken, apiKey string) []model.SiteToken {
	if len(tokens) > 0 || strings.TrimSpace(apiKey) == "" {
		return tokens
	}
	return append(tokens, defaultSiteToken(apiKey, "fallback"))
}

func fetchManagementTokens(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string) ([]model.SiteToken, map[string]error, error) {
	payload, err := requestJSONWithManagedAccessToken(ctx, siteRecord, "GET", buildSiteURL(siteRecord.BaseURL, "/api/token/?p=0&size=100"), nil, accessToken, account)
	if err != nil {
		return nil, nil, err
	}
	if err := validateSiteBusinessResponse(payload); err != nil {
		return nil, nil, err
	}
	items := parseTokenItems(payload)
	tokens := make([]model.SiteToken, 0, len(items))
	keyErrors := make(map[string]error)
	for index, item := range items {
		tokenValue := strings.TrimSpace(jsonString(item["key"]))
		if tokenValue == "" {
			continue
		}
		groupKey := model.NormalizeSiteGroupKey(firstNonEmptyString(jsonString(item["group"]), jsonString(item["token_group"]), jsonString(item["group_name"])))
		groupName := model.NormalizeSiteGroupName(groupKey, firstNonEmptyString(jsonString(item["group_name"]), jsonString(item["group"]), jsonString(item["token_group"])))
		token := model.SiteToken{Name: firstNonEmptyString(strings.TrimSpace(jsonString(item["name"])), fmt.Sprintf("token-%d", index+1)), Token: tokenValue, GroupKey: groupKey, GroupName: groupName, Enabled: parseEnabledFlag(item["status"]), Source: "sync", IsDefault: index == 0}
		if siteRecord.Platform == model.SitePlatformNewAPI && model.IsMaskedSiteTokenValue(token.Token) {
			fullKey, keyErr := resolveManagementTokenValue(ctx, siteRecord, account, accessToken, token, anyRouterParseInt(item["id"]))
			if keyErr != nil {
				keyErrors[groupKey] = errors.Join(keyErrors[groupKey], keyErr)
			} else {
				token.Token = fullKey
			}
		}
		token.ValueStatus = model.NormalizeSiteTokenValueStatus(token.ValueStatus, token.Token)
		tokens = append(tokens, token)
	}
	return tokens, keyErrors, nil
}

// Match only one distinct saved key. Mask collisions must never pick an
// arbitrary credential, and upstream token IDs are not local SiteToken IDs.
func resolveManagementTokenValue(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string, token model.SiteToken, upstreamID int) (string, error) {
	var savedKey string
	if account != nil {
		for _, existing := range account.Tokens {
			if model.NormalizeSiteGroupKey(existing.GroupKey) != token.GroupKey || normalizeSiteTokenName(existing.Name) != normalizeSiteTokenName(token.Name) ||
				!model.IsReadySiteToken(existing) || !model.SiteMaskedTokenMatches(existing.Token, token.Token) {
				continue
			}
			if savedKey != "" && !sameComparableSiteTokenValue(savedKey, existing.Token) {
				savedKey = ""
				break
			}
			savedKey = strings.TrimSpace(existing.Token)
		}
	}
	if savedKey != "" {
		return savedKey, nil
	}
	if upstreamID <= 0 {
		return "", errors.New("Key 为脱敏值且缺少上游令牌 ID，请手动补全完整 Key")
	}
	endpoint := fmt.Sprintf("/api/token/%d/key", upstreamID)
	payload, err := requestJSONWithManagedAccessToken(ctx, siteRecord, http.MethodPost, buildSiteURL(siteRecord.BaseURL, endpoint), nil, accessToken, account)
	if err == nil {
		err = validateSiteBusinessResponse(payload)
	}
	if err != nil {
		return "", fmt.Errorf("读取完整 Key 失败（POST %s；需要 api_key:reveal 权限或手动补全 Key）：%w", endpoint, err)
	}
	fullKey, _ := nestedValue(payload, "data", "key").(string)
	fullKey = strings.TrimSpace(fullKey)
	if fullKey == "" || model.IsMaskedSiteTokenValue(fullKey) {
		return "", fmt.Errorf("POST %s 未返回完整 Key，请手动补全 Key", endpoint)
	}
	return fullKey, nil
}

func fetchManagementGroups(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string) ([]model.SiteUserGroup, error) {
	endpoints := []string{"/api/user/self/groups", "/api/user_group_map"}
	seen := make(map[string]model.SiteUserGroup)
	var lastErr error
	failedEndpoints := 0
	for _, endpoint := range endpoints {
		payload, err := requestJSONWithManagedAccessToken(ctx, siteRecord, "GET", buildSiteURL(siteRecord.BaseURL, endpoint), nil, accessToken, account)
		if err != nil {
			lastErr = err
			failedEndpoints++
			continue
		}
		if err := validateSiteBusinessResponse(payload); err != nil {
			lastErr = err
			failedEndpoints++
			continue
		}
		for _, group := range parseGroupItems(payload) {
			key := model.NormalizeSiteGroupKey(group.GroupKey)
			group.GroupKey = key
			group.Name = model.NormalizeSiteGroupName(key, group.Name)
			group.RawPayload = marshalRawPayload(payload)
			seen[key] = group
		}
	}
	if len(seen) == 0 {
		if failedEndpoints == len(endpoints) {
			logSiteDataWarning(siteRecord, account, accessToken, "groups", "default_group_fallback", lastErr)
		}
		return []model.SiteUserGroup{{GroupKey: model.SiteDefaultGroupKey, Name: model.SiteDefaultGroupName}}, nil
	}
	groups := make([]model.SiteUserGroup, 0, len(seen))
	for _, group := range seen {
		groups = append(groups, group)
	}
	return groups, nil
}

func fetchModelsForSiteToken(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, token model.SiteToken) ([]string, error) {
	if model.IsMaskedSiteTokenValue(token.Token) {
		return nil, errors.New("Key 为脱敏值，无法请求模型接口，请先补全完整 Key")
	}
	if siteRecord != nil && siteRecord.Platform == model.SitePlatformSub2API {
		return fetchSub2APIModelsForSiteToken(ctx, siteRecord, account, token)
	}

	// Normalize the token the same way projection does, so the model-fetch
	// request authenticates with the value the upstream actually expects
	// (new-api family needs the "sk-" prefix; direct providers stay verbatim).
	tokenValue := model.NormalizeSiteSyncTokenValueForPlatform(siteRecord.Platform, token.Token)

	proxyMode, proxyConfigID := resolveSiteAccountProxy(siteRecord, account)
	var requestErrors []error

	for _, baseURL := range buildModelFetchBaseURLs(siteRecord) {
		channel := model.Channel{Type: platformOutboundType(siteRecord), BaseUrls: []model.BaseUrl{{URL: baseURL, Delay: 0}}, Keys: []model.ChannelKey{{Enabled: true, ChannelKey: tokenValue}}, ProxyMode: proxyMode, ProxyConfigID: proxyConfigID, CustomHeader: siteRecord.CustomHeader}
		fetched, err := helper.FetchModels(ctx, channel)
		// A successful New API listing, including an empty list, takes
		// precedence over errors from compatibility endpoints.
		if err == nil && (len(fetched) > 0 || siteRecord.Platform == model.SitePlatformNewAPI) {
			return normalizeModelNames(fetched), nil
		}
		if err != nil {
			endpointPath := "/models"
			if endpoint, parseErr := url.Parse(baseURL + "/models"); parseErr == nil {
				endpointPath = endpoint.EscapedPath()
			}
			requestErrors = append(requestErrors, fmt.Errorf("GET %s：%w", endpointPath, err))
		}
	}
	if siteRecord.Platform != model.SitePlatformOneHub && siteRecord.Platform != model.SitePlatformDoneHub {
		return nil, errors.Join(requestErrors...)
	}

	payload, fallbackErr := requestJSON(ctx, siteRecord, "GET", buildSiteURL(siteRecord.BaseURL, "/api/available_model"), nil, map[string]string{"Authorization": "Bearer " + tokenValue}, account)
	if fallbackErr != nil {
		return nil, errors.Join(append(requestErrors, fmt.Errorf("GET /api/available_model：%w", fallbackErr))...)
	}

	modelSet := make(map[string]struct{})
	if dataMap, ok := nestedValue(payload, "data").(map[string]any); ok {
		for key := range dataMap {
			trimmed := strings.TrimSpace(key)
			if trimmed != "" {
				modelSet[trimmed] = struct{}{}
			}
		}
	}
	if len(modelSet) == 0 {
		return nil, errors.Join(requestErrors...)
	}
	names := make([]string, 0, len(modelSet))
	for name := range modelSet {
		names = append(names, name)
	}
	return normalizeModelNames(names), nil
}

func fetchManagementModels(
	ctx context.Context,
	siteRecord *model.Site,
	account *model.SiteAccount,
	token model.SiteToken,
	keyErr error,
	sessionFallbackFetcher func(token model.SiteToken) (siteModelFetchResult, error),
) (siteModelFetchResult, error) {
	models, err := fetchModelsForSiteToken(ctx, siteRecord, account, token)
	if model.IsMaskedSiteTokenValue(token.Token) && keyErr != nil {
		err = keyErr
	}
	if len(models) > 0 {
		return siteModelFetchResult{names: models, source: siteModelSourceSync, authoritative: true, message: fmt.Sprintf("同步到 %d 个模型", len(models))}, nil
	}
	if siteRecord.Platform != model.SitePlatformNewAPI {
		return siteModelFetchResult{source: siteModelSourceSync, authoritative: err == nil, message: "上游当前没有返回可用模型"}, err
	}

	if sessionFallbackFetcher == nil {
		return siteModelFetchResult{message: "本次未能确认该分组模型，已保留历史模型"}, err
	}

	fallbackResult, fallbackErr := sessionFallbackFetcher(token)
	if len(fallbackResult.names) > 0 || fallbackResult.authoritative {
		if strings.TrimSpace(fallbackResult.source) == "" {
			fallbackResult.source = siteModelSourceSyncFallback
		}
		return fallbackResult, nil
	}
	if fallbackErr != nil {
		fallbackErr = fmt.Errorf("GET /api/user/models 失败：%w", fallbackErr)
	}
	if err != nil {
		err = fmt.Errorf("模型接口请求失败：%w", err)
	}
	if strings.TrimSpace(fallbackResult.message) == "" {
		fallbackResult.message = "本次未能确认该分组模型，已保留历史模型"
	}
	if combinedErr := errors.Join(fallbackErr, err); combinedErr != nil {
		return fallbackResult, fmt.Errorf("%s；%w", fallbackResult.message, combinedErr)
	}
	return fallbackResult, nil
}

func fetchManagedSessionModels(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string) ([]string, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, nil
	}
	payload, err := requestJSONWithManagedAccessToken(ctx, siteRecord, "GET", buildSiteURL(siteRecord.BaseURL, "/api/user/models"), nil, accessToken, account)
	if err != nil {
		return nil, err
	}
	if err := validateSiteBusinessResponse(payload); err != nil {
		return nil, err
	}
	return anyRouterParseModelNames(payload), nil
}

func buildModelFetchBaseURLs(siteRecord *model.Site) []string {
	if siteRecord == nil {
		return nil
	}

	baseURL := strings.TrimRight(strings.TrimSpace(siteRecord.BaseURL), "/")
	if baseURL == "" {
		return nil
	}

	candidates := []string{baseURL}
	if sitePlatformUsesV1ModelEndpoint(siteRecord) && !strings.HasSuffix(strings.ToLower(baseURL), "/v1") {
		if siteRecord.Platform == model.SitePlatformNewAPI {
			return []string{baseURL + "/v1", baseURL}
		}
		candidates = append(candidates, baseURL+"/v1")
	}
	return candidates
}

func filterSessionFallbackModelsByGroup(
	names []string,
	groupKey string,
	detections map[string]siteModelRouteDetection,
) siteModelFetchResult {
	normalizedGroupKey := model.NormalizeSiteGroupKey(groupKey)
	if normalizedGroupKey == "" {
		normalizedGroupKey = model.SiteDefaultGroupKey
	}
	if len(detections) == 0 {
		return siteModelFetchResult{message: fmt.Sprintf("无法从显式分组元数据确认分组 %q 的模型", normalizedGroupKey)}
	}

	filteredNames := make([]string, 0, len(names))
	filteredDetections := make(map[string]siteModelRouteDetection)
	hasExplicitGroupMetadata := false
	allModelsHaveExplicitGroupMetadata := true
	for _, name := range normalizeModelNames(names) {
		lookupKey := strings.ToLower(strings.TrimSpace(name))
		detection, ok := detections[lookupKey]
		if !ok {
			allModelsHaveExplicitGroupMetadata = false
			continue
		}
		metadata, ok := model.ParseSiteModelRouteMetadata(detection.RouteRawPayload)
		if !ok || len(metadata.EnableGroups) == 0 {
			allModelsHaveExplicitGroupMetadata = false
			continue
		}
		hasExplicitGroupMetadata = true
		if !stringSliceContainsFold(metadata.EnableGroups, normalizedGroupKey) {
			continue
		}
		filteredNames = append(filteredNames, name)
		filteredDetections[lookupKey] = detection
	}
	if len(filteredNames) > 0 {
		return siteModelFetchResult{
			names:         filteredNames,
			source:        siteModelSourceSyncFallback,
			detections:    filteredDetections,
			authoritative: true,
			message:       fmt.Sprintf("同步到 %d 个模型", len(filteredNames)),
		}
	}
	if !hasExplicitGroupMetadata {
		return siteModelFetchResult{message: fmt.Sprintf("显式分组元数据缺失，无法确认分组 %q 的模型", normalizedGroupKey)}
	}
	if !allModelsHaveExplicitGroupMetadata {
		return siteModelFetchResult{message: fmt.Sprintf("部分模型缺少显式分组元数据，无法确认分组 %q 的模型", normalizedGroupKey)}
	}
	if len(filteredNames) == 0 {
		return siteModelFetchResult{source: siteModelSourceSyncFallback, authoritative: true, message: fmt.Sprintf("分组 %q 当前没有可用模型", normalizedGroupKey)}
	}

	return siteModelFetchResult{message: fmt.Sprintf("无法确认分组 %q 的模型", normalizedGroupKey)}
}

func stringSliceContainsFold(values []string, target string) bool {
	normalizedTarget := strings.ToLower(strings.TrimSpace(target))
	if normalizedTarget == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), normalizedTarget) {
			return true
		}
	}
	return false
}

func sitePlatformUsesV1ModelEndpoint(site *model.Site) bool {
	if site.Platform == model.SitePlatformAPI {
		rt := site.ResolveDefaultRouteType()
		return rt == model.SiteModelRouteTypeOpenAIChat || rt == ""
	}
	return true
}

func buildSiteModels(names []string, groupKey string, source string) []model.SiteModel {
	names = normalizeModelNames(names)
	models := make([]model.SiteModel, 0, len(names))
	groupKey = model.NormalizeSiteGroupKey(groupKey)
	for _, name := range names {
		models = append(models, model.SiteModel{GroupKey: groupKey, ModelName: name, Source: source})
	}
	return models
}

func jsonFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0
		}
		var f float64
		if _, err := fmt.Sscanf(trimmed, "%f", &f); err == nil {
			return f
		}
		return 0
	default:
		return 0
	}
}
