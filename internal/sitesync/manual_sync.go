package sitesync

import (
	"context"
	"crypto/hmac"
	"sort"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func PreviewManualSync(ctx context.Context, accountID int, req ManualSyncRequest) (*ManualSyncPreview, error) {
	siteRecord, account, err := loadSyncableSiteAccount(ctx, accountID)
	if err != nil {
		return nil, sanitizeSiteError(err)
	}
	plan, err := buildManualSyncPlan(siteRecord, account, req)
	if err != nil {
		return nil, err
	}
	return &plan.preview, nil
}

func ApplyManualSync(ctx context.Context, accountID int, req ManualSyncRequest) (*ManualSyncApplyResult, error) {
	started := time.Now()
	diagnostic := &syncLogContext{}
	result, err := applyManualSync(ctx, accountID, req, diagnostic)
	err = diagnostic.safeError(err)
	var outcome *model.SiteSyncResult
	if result != nil {
		outcome = &result.SyncResult
	}
	logSyncResult(accountID, "manual_data", outcome, err, started, diagnostic)
	return result, err
}

func applyManualSync(ctx context.Context, accountID int, req ManualSyncRequest, diagnostic *syncLogContext) (*ManualSyncApplyResult, error) {
	siteRecord, account, err := loadSyncableSiteAccount(ctx, accountID)
	diagnostic.site, diagnostic.account = siteRecord, account
	if err != nil {
		return nil, sanitizeSiteError(err)
	}
	plan, err := buildManualSyncPlan(siteRecord, account, req)
	if err != nil {
		return nil, err
	}
	diagnostic.snapshot = plan.snapshot
	if !plan.preview.CanApply {
		return nil, manualSyncInvalid("预览中没有可应用的数据，请补充响应内容后重试")
	}
	providedFingerprint := strings.TrimSpace(req.PreviewFingerprint)
	if providedFingerprint == "" || !hmac.Equal([]byte(providedFingerprint), []byte(plan.preview.PreviewFingerprint)) {
		return nil, manualSyncInvalid("预览已失效，请重新预览后再应用")
	}

	if err := persistSyncSnapshot(ctx, account.ID, plan.snapshot); err != nil {
		markAccountSyncFailure(ctx, account.ID, err, plan.snapshot.accessToken)
		return nil, sanitizeSiteError(err)
	}
	channelIDs, err := ProjectAccount(ctx, account.ID)
	if err != nil {
		markAccountSyncFailure(ctx, account.ID, err, plan.snapshot.accessToken)
		return nil, sanitizeSiteError(err)
	}

	modelNames := make([]string, 0, len(plan.finalModels))
	for _, item := range plan.finalModels {
		modelNames = append(modelNames, item.ModelName)
	}
	sort.Strings(modelNames)
	result := model.SiteSyncResult{
		AccountID:       account.ID,
		SiteID:          siteRecord.ID,
		Status:          plan.snapshot.status,
		ChannelCount:    len(channelIDs),
		GroupCount:      len(plan.finalGroups),
		TokenCount:      len(plan.finalTokens),
		ModelCount:      len(plan.finalModels),
		ManagedChannels: channelIDs,
		Models:          modelNames,
		GroupResults:    exportSiteSyncGroupResults(plan.snapshot.groupResults),
		Message:         plan.snapshot.message,
	}
	plan.preview.ChannelCountEstimate = len(channelIDs)
	return &ManualSyncApplyResult{Preview: plan.preview, SyncResult: result}, nil
}

func buildManualSyncPlan(siteRecord *model.Site, account *model.SiteAccount, req ManualSyncRequest) (*manualSyncPlan, error) {
	if siteRecord == nil || account == nil {
		return nil, manualSyncInvalid("站点或账号不存在")
	}
	mode, format, err := normalizeManualSyncRequest(&req)
	if err != nil {
		return nil, err
	}
	sections, err := parseManualSyncSections(siteRecord, req)
	if err != nil {
		return nil, err
	}

	finalTokens := buildManualSyncTokens(account, sections)
	affectedModels, groupResults, explicitModelGroups := buildManualSyncModels(sections)
	groupResults, affectedModels = addManualTokenRecoveryGroups(account, finalTokens, sections, groupResults, affectedModels, explicitModelGroups)
	groupResults = addManualMissingKeyGroups(account, finalTokens, sections, groupResults, explicitModelGroups)

	existingModelMap := make(map[string]model.SiteModel, len(account.Models))
	for _, item := range account.Models {
		key := model.NormalizeSiteGroupKey(item.GroupKey) + "\x00" + strings.TrimSpace(item.ModelName)
		existingModelMap[key] = item
	}
	preparedModels := preparePersistedSyncModels(account.ID, affectedModels, existingModelMap, time.Now())
	finalModels := mergePersistedSiteModelsByGroup(account.Models, preparedModels, groupResults)
	sortSiteModels(finalModels)

	finalGroups := buildManualSyncGroups(account, sections, finalTokens, finalModels, explicitModelGroups)
	groupNames := make(map[string]string, len(finalGroups))
	for _, group := range finalGroups {
		groupNames[model.NormalizeSiteGroupKey(group.GroupKey)] = model.NormalizeSiteGroupName(group.GroupKey, group.Name)
	}
	for index := range groupResults {
		groupKey := model.NormalizeSiteGroupKey(groupResults[index].GroupKey)
		groupResults[index].GroupKey = groupKey
		groupResults[index].GroupName = model.NormalizeSiteGroupName(groupKey, groupNames[groupKey])
		groupResults[index].HasKey = hasUsableToken(tokensForGroup(finalTokens, groupKey))
	}
	sort.Slice(groupResults, func(i, j int) bool { return groupResults[i].GroupKey < groupResults[j].GroupKey })

	balance := account.Balance
	if sections.balance != nil {
		balance = *sections.balance
	}
	balanceUsed := account.BalanceUsed
	if sections.balanceUsed != nil {
		balanceUsed = *sections.balanceUsed
	}
	todayIncome := account.TodayIncome
	if sections.todayIncome != nil {
		todayIncome = *sections.todayIncome
	}
	accessToken := ""
	if sections.accessToken != nil {
		accessToken = strings.TrimSpace(*sections.accessToken)
	}

	snapshot := &syncSnapshot{
		accessToken:  accessToken,
		groups:       cloneSiteGroups(finalGroups),
		tokens:       cloneSiteTokens(finalTokens),
		models:       cloneSiteModels(affectedModels),
		groupResults: append([]siteGroupSyncResult(nil), groupResults...),
		status:       model.SiteExecutionStatusSuccess,
		balance:      balance,
		balanceUsed:  balanceUsed,
		todayIncome:  todayIncome,
		message:      buildManualSyncMessage(sections, groupResults),
	}

	previewGroups, channelCount := buildManualSyncPreviewGroups(siteRecord, account, finalGroups, finalTokens, finalModels, explicitModelGroups, groupResults)
	warnings := append([]string(nil), sections.warnings...)
	warnings = append(warnings, buildManualSyncWarnings(account, sections, finalTokens, finalModels, explicitModelGroups)...)
	warnings = normalizeManualWarnings(warnings)

	usableTokenCount, maskedTokenCount := countManualTokens(finalTokens)
	preview := ManualSyncPreview{
		AccountID:            account.ID,
		SiteID:               siteRecord.ID,
		Mode:                 mode,
		Format:               format,
		ImportedTokenCount:   len(sections.tokens),
		ImportedGroupCount:   len(sections.groups),
		ImportedModelCount:   countManualImportedModels(sections.models),
		TokenCount:           len(finalTokens),
		UsableTokenCount:     usableTokenCount,
		MaskedTokenCount:     maskedTokenCount,
		GroupCount:           len(finalGroups),
		ModelCount:           len(finalModels),
		ChannelCountEstimate: channelCount,
		BalanceProvided:      sections.balance != nil,
		Balance:              balance,
		BalanceUsedProvided:  sections.balanceUsed != nil,
		BalanceUsed:          balanceUsed,
		TodayIncomeProvided:  sections.todayIncome != nil,
		TodayIncome:          todayIncome,
		Groups:               previewGroups,
		Warnings:             warnings,
		CanApply:             manualSyncHasActionableSection(sections),
	}
	preview.PreviewFingerprint = buildManualSyncFingerprint(account.ID, mode, format, sections)

	return &manualSyncPlan{
		snapshot:    snapshot,
		preview:     preview,
		finalTokens: finalTokens,
		finalGroups: finalGroups,
		finalModels: finalModels,
	}, nil
}
