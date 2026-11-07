package op

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

type metAPIImportAccountData struct {
	Input          importedAccountInput
	OriginalID     int
	Tokens         []model.SiteToken
	Groups         []model.SiteUserGroup
	Models         []model.SiteModel
	DisabledModels []model.SiteModel
}

func SiteImportMetAPI(ctx context.Context, body []byte) (*model.MetAPIImportResult, error) {
	var payload rawImportObject
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, newSiteImportInvalidJSONError()
	}
	if len(payload) == 0 {
		return nil, newSiteImportEmptyPayloadError()
	}

	inputs, warnings, skipped, err := extractMetAPIAccounts(payload)
	if err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		return nil, newSiteImportNoImportableMetapiError()
	}

	result := &model.MetAPIImportResult{
		SkippedAccounts: skipped,
		Warnings:        warnings,
	}
	createdSiteIDs := make(map[int]struct{})
	reusedSiteIDs := make(map[int]struct{})

	if err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, input := range inputs {
			siteRecord, created, err := upsertImportedSite(tx, input.Input.Site)
			if err != nil {
				return err
			}
			if created {
				createdSiteIDs[siteRecord.ID] = struct{}{}
			} else if _, ok := createdSiteIDs[siteRecord.ID]; !ok {
				reusedSiteIDs[siteRecord.ID] = struct{}{}
			}

			accountRecord, createdAccount, updatedAccount, err := upsertImportedAccount(tx, siteRecord, input.Input)
			if err != nil {
				return err
			}
			if createdAccount {
				result.CreatedAccounts++
			}
			if updatedAccount {
				result.UpdatedAccounts++
			}

			tokens, groups, models, disabledModels, err := replaceMetAPIAccountData(tx, accountRecord.ID, input)
			if err != nil {
				return err
			}
			result.ImportedTokens += tokens
			result.ImportedGroups += groups
			result.ImportedModels += models
			result.DisabledModels += disabledModels
		}
		return db.SeparateLegacySiteCheckins(tx)
	}); err != nil {
		return nil, wrapSiteImportPersistFailedError(err)
	}

	result.CreatedSites = len(createdSiteIDs)
	result.ReusedSites = len(reusedSiteIDs)
	return result, nil
}

func extractMetAPIAccounts(payload rawImportObject) ([]metAPIImportAccountData, []string, int, error) {
	section := detectMetAPIAccountsSection(payload)
	if section == nil {
		return nil, nil, 0, newSiteImportUnrecognizedMetapiError()
	}

	siteRows := asObjectSlice(section["sites"])
	accountRows := asObjectSlice(section["accounts"])
	if len(siteRows) == 0 || len(accountRows) == 0 {
		return nil, nil, 0, newSiteImportUnsupportedPayloadError("metapi accounts section must include sites and accounts")
	}

	tokenRows := asObjectSlice(section["accountTokens"])
	manualModelRows := asObjectSlice(section["manualModels"])
	disabledModelRows := asObjectSlice(section["siteDisabledModels"])
	routeRows := asObjectSlice(section["tokenRoutes"])
	routeChannelRows := asObjectSlice(section["routeChannels"])
	downstreamKeyRows := asObjectSlice(section["downstreamApiKeys"])

	warnings := make([]string, 0)
	if len(routeRows) > 0 || len(routeChannelRows) > 0 {
		warnings = append(warnings, "已跳过 metapi 路由策略和路由通道，导入后由 Octopus 重新同步/投影生成")
	}
	if len(downstreamKeyRows) > 0 {
		warnings = append(warnings, "已跳过 metapi 下游 Key，Octopus API Key 需要单独配置")
	}

	siteByID := make(map[int]importedSiteInput, len(siteRows))
	for _, row := range siteRows {
		id := asInt(row["id"])
		siteURL := normalizeImportBaseURL(firstNonEmptyString(asString(row["url"]), asString(row["baseUrl"]), asString(row["base_url"])))
		siteName := firstNonEmptyString(asString(row["name"]), siteURL)
		if id <= 0 || siteURL == "" {
			continue
		}
		platform, ok := resolveImportedPlatform(firstNonEmptyString(asString(row["platform"]), asString(row["site_type"])), siteURL)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("跳过 metapi 站点 %s：站点平台不受支持", firstNonEmptyString(siteName, fmt.Sprintf("%d", id))))
			continue
		}
		siteByID[id] = importedSiteInput{
			Name:     siteName,
			Platform: platform,
			BaseURL:  siteURL,
		}
	}

	tokensByAccountID := make(map[int][]model.SiteToken)
	for _, row := range tokenRows {
		accountID := asInt(row["accountId"])
		token := strings.TrimSpace(asString(row["token"]))
		if accountID <= 0 || token == "" {
			continue
		}
		groupKey := model.NormalizeSiteGroupKey(firstNonEmptyString(asString(row["tokenGroup"]), asString(row["groupKey"]), asString(row["group_key"])))
		tokensByAccountID[accountID] = append(tokensByAccountID[accountID], model.SiteToken{
			Name:        firstNonEmptyString(asString(row["name"]), groupKey),
			Token:       token,
			ValueStatus: model.NormalizeSiteTokenValueStatus(model.SiteTokenValueStatus(asString(row["valueStatus"])), token),
			GroupKey:    groupKey,
			GroupName:   model.NormalizeSiteGroupName(groupKey, asString(row["tokenGroup"])),
			Enabled:     asBool(row["enabled"], true),
			Source:      firstNonEmptyString(asString(row["source"]), "metapi"),
			IsDefault:   asBool(row["isDefault"], false),
		})
	}

	manualModelsByAccountID := make(map[int][]model.SiteModel)
	for _, row := range manualModelRows {
		accountID := asInt(row["accountId"])
		modelName := strings.TrimSpace(asString(row["modelName"]))
		if accountID <= 0 || modelName == "" {
			continue
		}
		item := model.SiteModel{
			GroupKey:       model.SiteDefaultGroupKey,
			ModelName:      modelName,
			Source:         "metapi",
			RouteType:      model.InferSiteModelRouteType(modelName),
			RouteSource:    model.SiteModelRouteSourceSyncInferred,
			ManualOverride: false,
			Disabled:       false,
		}
		manualModelsByAccountID[accountID] = append(manualModelsByAccountID[accountID], item)
	}

	disabledModelsBySiteID := make(map[int][]string)
	for _, row := range disabledModelRows {
		siteID := asInt(row["siteId"])
		modelName := strings.TrimSpace(asString(row["modelName"]))
		if siteID <= 0 || modelName == "" {
			continue
		}
		disabledModelsBySiteID[siteID] = append(disabledModelsBySiteID[siteID], modelName)
	}

	inputs := make([]metAPIImportAccountData, 0, len(accountRows))
	skipped := 0
	for _, row := range accountRows {
		originalID := asInt(row["id"])
		input, warning, ok := parseMetAPIAccountRow(row, siteByID, tokensByAccountID[originalID])
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if !ok {
			skipped++
			continue
		}
		inputs = append(inputs, metAPIImportAccountData{
			Input:          input,
			OriginalID:     originalID,
			Tokens:         tokensByAccountID[originalID],
			Groups:         buildMetAPIGroups(tokensByAccountID[originalID]),
			Models:         manualModelsByAccountID[originalID],
			DisabledModels: buildMetAPIDisabledModels(disabledModelsBySiteID[asInt(row["siteId"])]),
		})
	}

	return inputs, warnings, skipped, nil
}

func detectMetAPIAccountsSection(payload rawImportObject) rawImportObject {
	if section := coerceMetAPIAccountsSection(payload); section != nil {
		return section
	}
	if section := coerceMetAPIAccountsSection(asObject(payload["accounts"])); section != nil {
		return section
	}
	if data := asObject(payload["data"]); data != nil {
		if section := coerceMetAPIAccountsSection(asObject(data["accounts"])); section != nil {
			return section
		}
	}
	return nil
}

func coerceMetAPIAccountsSection(value rawImportObject) rawImportObject {
	if value == nil {
		return nil
	}
	if len(asObjectSlice(value["sites"])) == 0 || len(asObjectSlice(value["accounts"])) == 0 {
		return nil
	}
	if value["accountTokens"] == nil || value["tokenRoutes"] == nil || value["routeChannels"] == nil {
		return nil
	}
	return value
}

func parseMetAPIAccountRow(row rawImportObject, sites map[int]importedSiteInput, tokens []model.SiteToken) (importedAccountInput, string, bool) {
	accountID := asInt(row["id"])
	siteID := asInt(row["siteId"])
	siteInput, ok := sites[siteID]
	rowID := firstNonEmptyString(asString(row["username"]), fmt.Sprintf("%d", accountID))
	if !ok {
		return importedAccountInput{}, fmt.Sprintf("跳过 metapi 账号 %s：关联站点缺失或不支持", rowID), false
	}

	accessToken := asString(row["accessToken"])
	apiToken := asString(row["apiToken"])
	if apiToken == "" {
		apiToken = firstReadyMetAPITokenValue(tokens)
	}
	extraConfig := asObjectFromJSONString(asString(row["extraConfig"]))
	credentialMode := strings.ToLower(strings.TrimSpace(asString(extraConfig["credentialMode"])))

	input := importedAccountInput{
		Site:           siteInput,
		Name:           firstNonEmptyString(asString(row["username"]), fmt.Sprintf("metapi-account-%d", accountID)),
		Username:       asString(row["username"]),
		Enabled:        metAPIAccountEnabled(row["status"]),
		AutoSync:       true,
		AutoCheckin:    asBool(row["checkinEnabled"], model.PlatformCheckinDefaults(siteInput.Platform).Enabled),
		Balance:        asFloat64(row["balance"]),
		BalanceUsed:    asFloat64(row["balanceUsed"]),
		PlatformUserID: asIntPointer(extraConfig["platformUserId"]),
		AccountProxy:   asStringPointer(extraConfig["proxyUrl"]),
	}

	if isDirectImportPlatform(siteInput.Platform) || credentialMode == "apikey" || accessToken == "" {
		if apiToken == "" {
			return importedAccountInput{}, fmt.Sprintf("跳过 metapi 账号 %s：apiToken 缺失", rowID), false
		}
		input.CredentialType = model.SiteCredentialTypeAPIKey
		input.APIKey = apiToken
		return input, "", true
	}

	input.CredentialType = model.SiteCredentialTypeAccessToken
	input.AccessToken = accessToken
	input.APIKey = apiToken
	if auth := asObject(extraConfig["sub2apiAuth"]); auth != nil {
		input.RefreshToken = asString(auth["refreshToken"])
		input.TokenExpiresAt = asInt64(auth["tokenExpiresAt"])
	}
	return input, "", true
}

func metAPIAccountEnabled(raw any) bool {
	status := strings.ToLower(strings.TrimSpace(asString(raw)))
	return status == "" || status == "active"
}

func firstReadyMetAPITokenValue(tokens []model.SiteToken) string {
	for _, token := range tokens {
		tokenValue := strings.TrimSpace(token.Token)
		if tokenValue == "" || model.IsMaskedSiteTokenValue(tokenValue) {
			continue
		}
		if !token.Enabled {
			continue
		}
		return tokenValue
	}
	return ""
}

func buildMetAPIGroups(tokens []model.SiteToken) []model.SiteUserGroup {
	seen := make(map[string]model.SiteUserGroup)
	for _, token := range tokens {
		groupKey := model.NormalizeSiteGroupKey(token.GroupKey)
		seen[groupKey] = model.SiteUserGroup{
			GroupKey: groupKey,
			Name:     model.NormalizeSiteGroupName(groupKey, token.GroupName),
		}
	}
	if len(seen) == 0 {
		return nil
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]model.SiteUserGroup, 0, len(keys))
	for _, key := range keys {
		result = append(result, seen[key])
	}
	return result
}

func buildMetAPIDisabledModels(modelNames []string) []model.SiteModel {
	result := make([]model.SiteModel, 0, len(modelNames))
	seen := make(map[string]struct{}, len(modelNames))
	for _, name := range modelNames {
		modelName := strings.TrimSpace(name)
		if modelName == "" {
			continue
		}
		key := strings.ToLower(modelName)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		item := model.SiteModel{
			GroupKey:    model.SiteDefaultGroupKey,
			ModelName:   modelName,
			Source:      "metapi",
			RouteType:   model.InferSiteModelRouteType(modelName),
			RouteSource: model.SiteModelRouteSourceSyncInferred,
			Disabled:    true,
		}
		result = append(result, item)
	}
	return result
}
