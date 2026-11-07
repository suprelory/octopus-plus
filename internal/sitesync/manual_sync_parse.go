package sitesync

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
)

func normalizeManualSyncRequest(req *ManualSyncRequest) (string, string, error) {
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = ManualSyncModeReplace
	}
	if mode != ManualSyncModeReplace {
		return "", "", manualSyncInvalid("不支持的导入模式：%s", req.Mode)
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = ManualSyncFormatResponses
	}
	if format != ManualSyncFormatResponses && format != ManualSyncFormatSnapshot {
		return "", "", manualSyncInvalid("不支持的输入格式：%s", req.Format)
	}
	req.Mode = mode
	req.Format = format
	return mode, format, nil
}

func parseManualSyncSections(siteRecord *model.Site, req ManualSyncRequest) (manualSyncSections, error) {
	sections := manualSyncSections{models: make(map[string][]model.SiteModel)}
	var err error
	switch req.Format {
	case ManualSyncFormatResponses:
		sections, err = parseManualResponseSections(siteRecord, req)
	case ManualSyncFormatSnapshot:
		sections, err = parseManualSnapshotSections(req.Snapshot)
	}
	if err != nil {
		return manualSyncSections{}, err
	}
	if !manualSyncHasProvidedSection(sections) {
		return manualSyncSections{}, manualSyncInvalid("未找到可导入的数据区段")
	}
	return sections, nil
}

func manualSyncHasProvidedSection(sections manualSyncSections) bool {
	return sections.tokensProvided ||
		sections.groupsProvided ||
		sections.accountProvided ||
		len(sections.models) > 0 ||
		sections.balance != nil ||
		sections.balanceUsed != nil ||
		sections.todayIncome != nil ||
		sections.accessToken != nil
}

func manualSyncHasActionableSection(sections manualSyncSections) bool {
	return sections.tokensProvided ||
		sections.groupsProvided ||
		len(sections.models) > 0 ||
		sections.balance != nil ||
		sections.balanceUsed != nil ||
		sections.todayIncome != nil ||
		sections.accessToken != nil
}

func parseManualResponseSections(siteRecord *model.Site, req ManualSyncRequest) (manualSyncSections, error) {
	sections := manualSyncSections{models: make(map[string][]model.SiteModel)}
	if manualRawProvided(req.TokenResponse) {
		value, err := decodeManualResponse(req.TokenResponse, "Token 响应")
		if err != nil {
			return sections, err
		}
		sections.tokensProvided = true
		sections.tokens = parseManualTokens(value)
		if len(sections.tokens) == 0 {
			sections.warnings = append(sections.warnings, "Token 响应中未解析到 Key")
		}
	}

	for index, raw := range req.GroupResponses {
		if !manualRawProvided(raw) {
			continue
		}
		value, err := decodeManualResponse(raw, fmt.Sprintf("分组响应 %d", index+1))
		if err != nil {
			return sections, err
		}
		sections.groupsProvided = true
		for _, group := range parseGroupItemsFromAny(value) {
			group.GroupKey = model.NormalizeSiteGroupKey(group.GroupKey)
			group.Name = model.NormalizeSiteGroupName(group.GroupKey, group.Name)
			group.RawPayload = ""
			sections.groups = upsertManualGroup(sections.groups, group)
		}
	}
	if sections.groupsProvided && len(sections.groups) == 0 {
		sections.warnings = append(sections.warnings, "分组响应中未解析到分组")
	}

	for index, item := range req.ModelResponses {
		groupKey := model.NormalizeSiteGroupKey(item.GroupKey)
		if strings.TrimSpace(item.GroupKey) == "" {
			return sections, manualSyncInvalid("模型响应 %d 缺少分组标识", index+1)
		}
		if !manualRawProvided(item.Response) {
			return sections, manualSyncInvalid("分组 %q 的模型响应为空", groupKey)
		}
		value, err := decodeManualResponse(item.Response, fmt.Sprintf("分组 %q 的模型响应", groupKey))
		if err != nil {
			return sections, err
		}
		names := collectManualModelNames(value)
		for _, name := range names {
			sections.models[groupKey] = upsertManualModel(sections.models[groupKey], model.SiteModel{
				GroupKey:  groupKey,
				ModelName: name,
				Source:    manualSyncSource,
			})
		}
		if _, ok := sections.models[groupKey]; !ok {
			sections.models[groupKey] = nil
		}
		if len(names) == 0 {
			sections.warnings = append(sections.warnings, fmt.Sprintf("分组 %q 的响应中未解析到模型", groupKey))
		}
	}

	if manualRawProvided(req.AccountResponse) {
		value, err := decodeManualResponse(req.AccountResponse, "账户响应")
		if err != nil {
			return sections, err
		}
		sections.accountProvided = true
		sections.balance, sections.balanceUsed, sections.todayIncome = parseManualAccountBalance(siteRecord.Platform, value)
		if sections.balance == nil && sections.balanceUsed == nil && sections.todayIncome == nil {
			sections.warnings = append(sections.warnings, "账户响应中未解析到余额、已用额度或今日收入")
		}
	}
	return sections, nil
}

func parseManualSnapshotSections(snapshot *ManualSyncSnapshotInput) (manualSyncSections, error) {
	sections := manualSyncSections{models: make(map[string][]model.SiteModel)}
	if snapshot == nil {
		return sections, manualSyncInvalid("统一快照不能为空")
	}
	if snapshot.Tokens != nil {
		sections.tokensProvided = true
		for index, item := range *snapshot.Tokens {
			tokenValue := strings.TrimSpace(item.Token)
			if tokenValue == "" {
				return sections, manualSyncInvalid("快照中的第 %d 个 Token 为空", index+1)
			}
			groupKey := model.NormalizeSiteGroupKey(item.GroupKey)
			enabled := true
			if item.Enabled != nil {
				enabled = *item.Enabled
			}
			isDefault := index == 0
			if item.IsDefault != nil {
				isDefault = *item.IsDefault
			}
			sections.tokens = append(sections.tokens, model.SiteToken{
				Name:        firstNonEmptyString(item.Name, fmt.Sprintf("token-%d", index+1)),
				Token:       tokenValue,
				ValueStatus: model.NormalizeSiteTokenValueStatus("", tokenValue),
				GroupKey:    groupKey,
				GroupName:   model.NormalizeSiteGroupName(groupKey, item.GroupName),
				Enabled:     enabled,
				Source:      manualSyncSource,
				IsDefault:   isDefault,
			})
		}
	}
	if snapshot.Groups != nil {
		sections.groupsProvided = true
		for _, item := range *snapshot.Groups {
			groupKey := model.NormalizeSiteGroupKey(item.GroupKey)
			sections.groups = upsertManualGroup(sections.groups, model.SiteUserGroup{
				GroupKey: groupKey,
				Name:     model.NormalizeSiteGroupName(groupKey, item.Name),
			})
		}
	}
	if snapshot.Models != nil {
		for rawGroupKey, items := range *snapshot.Models {
			groupKey := model.NormalizeSiteGroupKey(rawGroupKey)
			if strings.TrimSpace(rawGroupKey) == "" {
				return sections, manualSyncInvalid("快照模型映射包含空分组标识")
			}
			for _, item := range items {
				modelName := strings.TrimSpace(item.ModelName)
				if modelName == "" {
					return sections, manualSyncInvalid("分组 %q 中存在空模型名称", groupKey)
				}
				routeType := item.RouteType
				if strings.TrimSpace(string(routeType)) != "" && !isKnownManualRouteType(routeType) {
					return sections, manualSyncInvalid("模型 %q 的端点类型 %q 不受支持", modelName, routeType)
				}
				sections.models[groupKey] = upsertManualModel(sections.models[groupKey], model.SiteModel{
					GroupKey:  groupKey,
					ModelName: modelName,
					Source:    manualSyncSource,
					RouteType: routeType,
				})
			}
			if _, ok := sections.models[groupKey]; !ok {
				sections.models[groupKey] = nil
			}
		}
	}
	if snapshot.AccessToken != nil {
		value := strings.TrimSpace(*snapshot.AccessToken)
		if value != "" {
			sections.accessToken = &value
		}
	}
	sections.balance = snapshot.Balance
	sections.balanceUsed = snapshot.BalanceUsed
	sections.todayIncome = snapshot.TodayIncome
	if err := validateManualBalanceValues(sections.balance, sections.balanceUsed, sections.todayIncome); err != nil {
		return sections, err
	}
	return sections, nil
}

func manualRawProvided(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

func decodeManualResponse(raw json.RawMessage, label string) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, manualSyncInvalid("%s不是有效 JSON：%v", label, err)
	}
	if value == nil {
		return nil, manualSyncInvalid("%s不能为空", label)
	}
	if payload, ok := value.(map[string]any); ok {
		if rawSuccess, exists := payload["success"]; exists {
			if success, ok := rawSuccess.(bool); ok && !success {
				return nil, manualSyncInvalid("%s返回失败：%s", label, firstNonEmptyString(extractSiteResponseMessage(payload), "上游响应 success=false"))
			}
		}
		if rawCode, exists := payload["code"]; exists {
			code := anyToInt64(rawCode)
			if code != 0 && code != 1 && (code < 200 || code >= 300) {
				return nil, manualSyncInvalid("%s返回失败：%s", label, firstNonEmptyString(extractSiteResponseMessage(payload), fmt.Sprintf("code=%d", code)))
			}
		}
	}
	return value, nil
}

func parseManualTokens(value any) []model.SiteToken {
	items := parseTokenItemsFromAny(value)
	if len(items) == 0 {
		if item, ok := value.(map[string]any); ok && manualTokenValue(item) != "" {
			items = []map[string]any{item}
		}
	}
	tokens := make([]model.SiteToken, 0, len(items))
	for index, item := range items {
		tokenValue := manualTokenValue(item)
		if tokenValue == "" {
			continue
		}
		groupKey := model.NormalizeSiteGroupKey(firstNonEmptyString(
			jsonString(item["group_key"]),
			jsonString(item["groupKey"]),
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
			Name:        firstNonEmptyString(jsonString(item["name"]), jsonString(item["remark"]), fmt.Sprintf("token-%d", index+1)),
			Token:       tokenValue,
			ValueStatus: model.NormalizeSiteTokenValueStatus("", tokenValue),
			GroupKey:    groupKey,
			GroupName:   groupName,
			Enabled:     parseSub2APITokenEnabled(item),
			Source:      manualSyncSource,
			IsDefault:   jsonBool(item["is_default"]) || jsonBool(item["isDefault"]) || index == 0,
		})
	}
	return tokens
}

func manualTokenValue(item map[string]any) string {
	return firstNonEmptyString(
		jsonString(item["key"]),
		jsonString(item["token"]),
		jsonString(item["api_key"]),
		jsonString(item["apiKey"]),
		jsonString(item["token_value"]),
		jsonString(item["tokenValue"]),
		jsonString(item["access_token"]),
		jsonString(item["accessToken"]),
	)
}

func collectManualModelNames(value any) []string {
	return normalizeModelNames(collectManualModelNamesRecursive(value))
}

func collectManualModelNamesRecursive(value any) []string {
	switch typed := value.(type) {
	case string:
		if name := strings.TrimSpace(strings.TrimPrefix(typed, "models/")); name != "" {
			return []string{name}
		}
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			result = append(result, collectManualModelNamesRecursive(item)...)
		}
		return result
	case []string:
		return typed
	case map[string]any:
		if name := firstNonEmptyString(
			jsonString(typed["id"]),
			jsonString(typed["model"]),
			jsonString(typed["model_name"]),
			jsonString(typed["modelName"]),
			jsonString(typed["name"]),
		); name != "" {
			return []string{strings.TrimPrefix(name, "models/")}
		}
		for _, key := range []string{"data", "items", "models", "list", "records", "rows", "available_models", "availableModels"} {
			if child, ok := typed[key]; ok {
				if names := collectManualModelNamesRecursive(child); len(names) > 0 {
					return names
				}
				if isManualEmptyCollection(child) {
					return nil
				}
			}
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if isManualModelEnvelopeKey(key) {
				continue
			}
			if trimmed := strings.TrimSpace(key); trimmed != "" {
				keys = append(keys, strings.TrimPrefix(trimmed, "models/"))
			}
		}
		return keys
	}
	return nil
}

func isManualEmptyCollection(value any) bool {
	switch typed := value.(type) {
	case []any:
		return len(typed) == 0
	case []string:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func isManualModelEnvelopeKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "", "success", "message", "msg", "code", "error", "errors", "object", "total", "page", "page_size", "pagesize", "has_more", "hasmore":
		return true
	default:
		return false
	}
}

func isKnownManualRouteType(routeType model.SiteModelRouteType) bool {
	switch routeType {
	case model.SiteModelRouteTypeOpenAIChat,
		model.SiteModelRouteTypeOpenAIResponse,
		model.SiteModelRouteTypeAnthropic,
		model.SiteModelRouteTypeGemini,
		model.SiteModelRouteTypeOpenAIEmbedding,
		model.SiteModelRouteTypeUnknown:
		return true
	default:
		return false
	}
}
