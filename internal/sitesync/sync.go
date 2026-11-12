package sitesync

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/model"
)

var (
	alreadyCheckedInEnglishPattern = regexp.MustCompile(`(?i)\b(?:already\s+(?:been\s+)?(?:checked[ -]in|signed[ -]in)|(?:checked[ -]in|signed[ -]in)\s+(?:already|today))\b`)
	alreadyCheckedInChinesePattern = regexp.MustCompile(`(?:已(?:经)?签到|签到过(?:了)?)(?:$|[了，。！!\s]|无法重复|不能重复)`)
	negativeCheckinPattern         = regexp.MustCompile(`(?i)\b(?:not|never|haven't|hasn't|didn't)\s+(?:(?:yet|already)\s+)?(?:checked[ -]in|signed[ -]in)\b|(?:未|没|没有)(?:曾经|曾|有|进行|完成)?签到`)
)

func isAlreadyCheckedInMessage(message string) bool {
	lowered := strings.ToLower(strings.TrimSpace(message))
	if lowered == "" || negativeCheckinPattern.MatchString(lowered) {
		return false
	}
	return alreadyCheckedInEnglishPattern.MatchString(lowered) || alreadyCheckedInChinesePattern.MatchString(message)
}

func isUnsupportedCheckinError(err error) bool {
	if apperror.IsCode(err, CodeSiteUpstreamBusinessError) {
		return isUnsupportedCheckinMessage(apperror.Message(err))
	}
	if !apperror.IsCode(err, CodeSiteUpstreamHTTPError) || siteErrorStatusCode(err) != http.StatusNotFound {
		return false
	}
	reason, _ := apperror.Params(err)["reason"].(string)
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "not found", "page not found", "404 not found", "404 page not found", "invalid url (post /api/user/checkin)", "invalid url (post /api/user/sign_in)":
		return true
	default:
		return false
	}
}

// Only explicit feature-level responses are evidence. Account, permission and
// transport failures must not disable check-in for every account on the site.
func isUnsupportedCheckinMessage(message string) bool {
	switch strings.ToLower(strings.TrimRight(strings.TrimSpace(message), ".!。！")) {
	case "checkin is disabled", "check-in is disabled", "checkin is not enabled", "check-in is not enabled",
		"checkin is not supported", "check-in is not supported", "checkin feature is disabled", "check-in feature is disabled",
		"签到功能未启用", "签到功能未开启", "签到功能暂未开启", "签到功能已禁用", "签到功能已关闭", "不支持签到", "本站不支持签到":
		return true
	default:
		return false
	}
}

func syncAccountState(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount) (*syncSnapshot, error) {
	if siteRecord == nil || account == nil {
		return nil, fmt.Errorf("site or account is nil")
	}
	switch siteRecord.Platform {
	case model.SitePlatformAnyRouter:
		return syncAnyRouter(ctx, siteRecord, account)
	case model.SitePlatformNewAPI, model.SitePlatformOneAPI, model.SitePlatformOneHub, model.SitePlatformDoneHub:
		return syncManagementPlatform(ctx, siteRecord, account)
	case model.SitePlatformSub2API:
		return syncSub2API(ctx, siteRecord, account)
	case model.SitePlatformAPI:
		return syncOfficialPlatform(ctx, siteRecord, account)
	default:
		return nil, newUnsupportedSitePlatformError(siteRecord.Platform)
	}
}

func checkinAccountState(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, beforeCheckin func(string) error) (*model.SiteCheckinResult, string, error) {
	if siteRecord == nil || account == nil {
		return nil, "", fmt.Errorf("site or account is nil")
	}
	switch siteRecord.CheckinAdapter() {
	case model.SiteCheckinAdapterHTTP:
		return checkinConfiguredHTTP(ctx, siteRecord, account)
	case model.SiteCheckinAdapterAnyRouter:
		return checkinAnyRouter(ctx, siteRecord, account, beforeCheckin)
	case model.SiteCheckinAdapterManagement:
		accessToken, err := resolveManagedAccessToken(ctx, siteRecord, account)
		if err != nil {
			return nil, accessToken, err
		}
		if beforeCheckin != nil {
			if err := beforeCheckin(accessToken); err != nil {
				return nil, accessToken, err
			}
		}
		checkinURL := buildSiteURL(siteRecord.BaseURL, "/api/user/checkin")
		payload, err := requestJSONWithManagedAccessToken(ctx, siteRecord, http.MethodPost, checkinURL, nil, accessToken, account)
		var turnstileFailure string
		if err == nil {
			payload, turnstileFailure, err = retryCheckinWithTurnstile(ctx, siteRecord, account, checkinURL, accessToken, payload)
		}
		if err != nil {
			if isUnsupportedCheckinError(err) {
				return newUnsupportedCheckinResult(""), accessToken, nil
			}
			return nil, accessToken, err
		}
		if turnstileFailure != "" {
			// An explicit reason keeps solver error codes in the message from being
			// classified as site timeouts or HTTP errors.
			return &model.SiteCheckinResult{Status: model.SiteExecutionStatusFailed, Reason: string(SiteBatchReasonUpstreamBusinessError), Message: turnstileFailure}, accessToken, nil
		}
		success := jsonBool(payload["success"])
		message := extractSiteResponseMessage(payload)
		if success || isAlreadyCheckedInMessage(message) {
			return newSuccessfulCheckinResult(message, checkinRewardString(nestedValue(payload, "data", "reward"))), accessToken, nil
		}
		if isUnsupportedCheckinMessage(message) {
			return newUnsupportedCheckinResult(message), accessToken, nil
		}
		return &model.SiteCheckinResult{Status: model.SiteExecutionStatusFailed, Message: firstNonEmptyString(message, "checkin failed")}, accessToken, nil
	default:
		return &model.SiteCheckinResult{Status: model.SiteExecutionStatusSkipped, Reason: model.SiteCheckinReasonNotConfigured, Message: "configure a custom HTTP checkin endpoint for this site"}, "", nil
	}
}

func syncManagementPlatform(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount) (*syncSnapshot, error) {
	if account.CredentialType == model.SiteCredentialTypeAPIKey {
		return syncWithDirectToken(ctx, siteRecord, account, resolveDirectToken(account), "manual")
	}

	accessToken, err := resolveManagedAccessToken(ctx, siteRecord, account)
	if err != nil {
		return nil, err
	}

	tokens, err := fetchManagementTokens(ctx, siteRecord, account, accessToken)
	if err != nil {
		return nil, err
	}
	groups, err := fetchManagementGroups(ctx, siteRecord, account, accessToken)
	if err != nil {
		groups = nil
	}
	if len(tokens) == 0 && strings.TrimSpace(account.APIKey) != "" {
		tokens = append(tokens, model.SiteToken{Name: "default", Token: strings.TrimSpace(account.APIKey), GroupKey: model.SiteDefaultGroupKey, GroupName: model.SiteDefaultGroupName, Enabled: true, Source: "fallback", IsDefault: true})
	}
	if len(tokens) == 0 {
		return nil, newMissingGroupKeyError(model.SiteDefaultGroupKey)
	}

	groups = mergeSiteGroups(groups, tokens)
	groupTokens := pickModelTokensByGroup(tokens)
	sessionModelsLoaded := false
	var cachedSessionModels []string
	var cachedSessionModelsErr error
	loadSessionModels := func() ([]string, error) {
		if sessionModelsLoaded {
			return cachedSessionModels, cachedSessionModelsErr
		}
		sessionModelsLoaded = true
		cachedSessionModels, cachedSessionModelsErr = fetchManagedSessionModels(ctx, siteRecord, account, accessToken)
		return cachedSessionModels, cachedSessionModelsErr
	}
	sessionDetectionsLoaded := false
	var cachedSessionDetections map[string]siteModelRouteDetection
	loadSessionDetections := func() map[string]siteModelRouteDetection {
		if sessionDetectionsLoaded {
			return cachedSessionDetections
		}
		sessionDetectionsLoaded = true
		sessionModels, err := loadSessionModels()
		if err != nil || len(sessionModels) == 0 {
			return nil
		}
		cachedSessionDetections = detectManagedExplicitGroupRoutes(ctx, siteRecord, account, accessToken, sessionModels)
		return cachedSessionDetections
	}
	sessionFallbackFetcher := func(token model.SiteToken) (siteModelFetchResult, error) {
		sessionModels, sessionErr := loadSessionModels()
		if sessionErr != nil {
			return siteModelFetchResult{message: "获取会话模型列表失败，本次保留历史模型"}, sessionErr
		}
		if len(sessionModels) == 0 {
			return siteModelFetchResult{source: siteModelSourceSyncFallback, authoritative: true, message: "上游当前没有可用模型"}, nil
		}
		return filterSessionFallbackModelsByGroup(sessionModels, token.GroupKey, loadSessionDetections()), nil
	}
	siteModels, tokenGroupResults := syncSiteModelsByGroup(
		ctx,
		siteRecord,
		account,
		accessToken,
		groupTokens,
		firstManagedPlatformUserID(account),
		siteModelSourceSync,
		func(token model.SiteToken, allowGlobalFallback bool) (siteModelFetchResult, error) {
			if siteRecord.Platform == model.SitePlatformNewAPI {
				return fetchManagementModels(ctx, siteRecord, account, accessToken, token, sessionFallbackFetcher)
			}
			models, err := fetchModelsForSiteToken(ctx, siteRecord, account, token)
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
	balance, balanceUsed, todayIncome := fetchSiteAccountBalance(ctx, siteRecord, account, accessToken, firstManagedPlatformUserID(account))
	message := buildSyncSnapshotMessage(groupResults)
	snapshot := &syncSnapshot{accessToken: accessToken, groups: groups, tokens: tokens, models: siteModels, groupResults: groupResults, status: status, balance: balance, balanceUsed: balanceUsed, todayIncome: todayIncome, message: message}
	if status == model.SiteExecutionStatusFailed {
		return snapshot, buildSyncSnapshotFailure(groupResults)
	}
	return snapshot, nil
}

func syncSub2API(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount) (*syncSnapshot, error) {
	ctx = withSub2APIRecoveryBudget(ctx)
	if account.CredentialType == model.SiteCredentialTypeUsernamePassword {
		return nil, fmt.Errorf("sub2api does not support username/password login")
	}
	if account.CredentialType == model.SiteCredentialTypeAPIKey {
		return syncWithDirectToken(ctx, siteRecord, account, resolveDirectToken(account), "manual")
	}

	accessToken, err := ensureFreshSub2APIAccessToken(ctx, siteRecord, account, false)
	if err != nil {
		return nil, err
	}
	return syncSub2APIWithAccessToken(ctx, siteRecord, account, accessToken)
}

func syncSub2APIWithAccessToken(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, accessToken string) (*syncSnapshot, error) {
	accessToken = stripBearerPrefix(accessToken)
	if accessToken == "" {
		return nil, newAccessTokenRequiredError()
	}
	tokens, err := fetchSub2APITokens(ctx, siteRecord, account, accessToken)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 && strings.TrimSpace(account.APIKey) != "" {
		tokens = append(tokens, model.SiteToken{Name: "default", Token: strings.TrimSpace(account.APIKey), GroupKey: model.SiteDefaultGroupKey, GroupName: model.SiteDefaultGroupName, Enabled: true, Source: "fallback", IsDefault: true})
	}
	if len(tokens) == 0 {
		return nil, apperror.New(apperror.CodeSiteSub2APIAPIKeyRequired, "sub2api sync requires an API key; create a key on the site and sync again")
	}

	groups, err := fetchSub2APIGroups(ctx, siteRecord, account, accessToken, tokens)
	if err != nil {
		if status, _ := sub2APIUpstreamErrorDetails(err); status == 401 || strings.HasPrefix(apperror.Code(err), "site.sub2api.") {
			return nil, err
		}
		groups = nil
	}
	groups = mergeSiteGroups(groups, tokens)
	siteModels, tokenGroupResults := syncSiteModelsByGroup(
		ctx,
		siteRecord,
		account,
		accessToken,
		pickModelTokensByGroup(tokens),
		0,
		siteModelSourceSync,
		func(token model.SiteToken, allowGlobalFallback bool) (siteModelFetchResult, error) {
			models, err := fetchModelsForSiteToken(ctx, siteRecord, account, token)
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
	balance, balanceUsed, todayIncome := fetchSiteAccountBalance(ctx, siteRecord, account, accessToken, 0)
	snapshot := &syncSnapshot{accessToken: accessToken, groups: groups, tokens: tokens, models: siteModels, groupResults: groupResults, status: status, balance: balance, balanceUsed: balanceUsed, todayIncome: todayIncome, message: buildSyncSnapshotMessage(groupResults)}
	if status == model.SiteExecutionStatusFailed {
		return snapshot, buildSyncSnapshotFailure(groupResults)
	}
	return snapshot, nil
}

func syncOfficialPlatform(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount) (*syncSnapshot, error) {
	return syncWithDirectToken(ctx, siteRecord, account, resolveDirectToken(account), "manual")
}

func syncWithDirectToken(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount, token string, source string) (*syncSnapshot, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, newDirectTokenRequiredError()
	}
	models, err := fetchModelsForSiteToken(ctx, siteRecord, account, model.SiteToken{Token: token, GroupKey: model.SiteDefaultGroupKey, GroupName: model.SiteDefaultGroupName, Enabled: true})
	groupToken := model.SiteToken{Name: "default", Token: token, GroupKey: model.SiteDefaultGroupKey, GroupName: model.SiteDefaultGroupName, Enabled: true, Source: source, IsDefault: true}
	siteModels := buildSiteModels(models, model.SiteDefaultGroupKey, source)
	siteModels = applyDetectedRoutesToSiteModels(
		ctx,
		siteRecord,
		account,
		token,
		groupToken,
		firstManagedPlatformUserID(account),
		siteModels,
	)
	baseGroupResult := siteGroupSyncResult{
		GroupKey:      model.SiteDefaultGroupKey,
		GroupName:     model.SiteDefaultGroupName,
		HasKey:        true,
		Authoritative: err == nil,
		ModelCount:    len(siteModels),
	}
	if err != nil {
		baseGroupResult.Status = siteGroupSyncStatusFailed
		baseGroupResult.Message = err.Error()
	} else if len(siteModels) > 0 {
		baseGroupResult.Status = siteGroupSyncStatusSynced
		baseGroupResult.Message = fmt.Sprintf("同步到 %d 个模型", len(siteModels))
	} else {
		baseGroupResult.Status = siteGroupSyncStatusEmpty
		baseGroupResult.Message = "上游当前没有可用模型"
	}
	groupResults := finalizeSiteGroupSyncResults(account, []model.SiteUserGroup{{GroupKey: model.SiteDefaultGroupKey, Name: model.SiteDefaultGroupName}}, []model.SiteToken{groupToken}, siteModels, []siteGroupSyncResult{{
		GroupKey:      baseGroupResult.GroupKey,
		GroupName:     baseGroupResult.GroupName,
		HasKey:        baseGroupResult.HasKey,
		Status:        baseGroupResult.Status,
		Authoritative: baseGroupResult.Authoritative,
		ModelCount:    baseGroupResult.ModelCount,
		Message:       baseGroupResult.Message,
	}})
	status := buildSyncSnapshotStatus(groupResults)
	snapshot := &syncSnapshot{
		accessToken:  strings.TrimSpace(account.AccessToken),
		groups:       []model.SiteUserGroup{{GroupKey: model.SiteDefaultGroupKey, Name: model.SiteDefaultGroupName}},
		tokens:       []model.SiteToken{groupToken},
		models:       siteModels,
		groupResults: groupResults,
		status:       status,
		message:      buildSyncSnapshotMessage(groupResults),
	}
	if err != nil || status == model.SiteExecutionStatusFailed {
		return snapshot, buildSyncSnapshotFailure(groupResults)
	}
	return snapshot, nil
}

func resolveManagedAccessToken(ctx context.Context, siteRecord *model.Site, account *model.SiteAccount) (string, error) {
	if account.CredentialType == model.SiteCredentialTypeAccessToken {
		if strings.TrimSpace(account.AccessToken) == "" {
			return "", newAccessTokenRequiredError()
		}
		return strings.TrimSpace(account.AccessToken), nil
	}
	if account.CredentialType != model.SiteCredentialTypeUsernamePassword {
		return "", fmt.Errorf("managed access token is not available for credential type %s", account.CredentialType)
	}

	payload, err := requestJSON(ctx, siteRecord, http.MethodPost, buildSiteURL(siteRecord.BaseURL, "/api/user/login"), map[string]any{"username": account.Username, "password": account.Password}, nil, account)
	if err != nil {
		return "", err
	}
	if !jsonBool(payload["success"]) {
		return "", newSiteLoginFailedError(firstNonEmptyString(jsonString(payload["message"]), "login failed"))
	}

	token := jsonString(payload["data"])
	if token == "" {
		if dataMap, ok := payload["data"].(map[string]any); ok {
			token = firstNonEmptyString(jsonString(dataMap["token"]), jsonString(dataMap["access_token"]), jsonString(dataMap["accessToken"]))
		}
	}
	if token == "" {
		token = firstNonEmptyString(jsonString(payload["token"]), jsonString(payload["access_token"]), jsonString(payload["accessToken"]))
	}
	if token == "" {
		return "", newSiteLoginTokenMissingError()
	}
	return token, nil
}

func resolveDirectToken(account *model.SiteAccount) string {
	if account == nil {
		return ""
	}
	switch account.CredentialType {
	case model.SiteCredentialTypeAPIKey:
		return strings.TrimSpace(account.APIKey)
	case model.SiteCredentialTypeAccessToken:
		return strings.TrimSpace(account.AccessToken)
	}
	if strings.TrimSpace(account.APIKey) != "" {
		return strings.TrimSpace(account.APIKey)
	}
	return strings.TrimSpace(account.AccessToken)
}
