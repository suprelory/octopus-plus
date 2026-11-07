package sitesync

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestBuildManualSyncPlanParsesManagementResponses(t *testing.T) {
	siteRecord := &model.Site{ID: 7, Platform: model.SitePlatformNewAPI, Enabled: true}
	account := &model.SiteAccount{ID: 11, SiteID: siteRecord.ID, Enabled: true, Balance: 9, BalanceUsed: 3}
	req := ManualSyncRequest{
		Mode:          ManualSyncModeReplace,
		Format:        ManualSyncFormatResponses,
		TokenResponse: json.RawMessage(`{"success":true,"data":{"items":[{"name":"primary","key":"abc123","group":"default","status":1},{"name":"vip","api_key":"vip-key","group_id":"vip","group_name":"VIP","enabled":true}]}}`),
		GroupResponses: []json.RawMessage{
			json.RawMessage(`{"success":true,"data":{"default":"默认","vip":"VIP"}}`),
		},
		ModelResponses: []ManualSyncModelResponse{
			{GroupKey: "default", Response: json.RawMessage(`{"object":"list","data":[{"id":"gpt-5"},{"id":"claude-sonnet-4-5"}]}`)},
			{GroupKey: "vip", Response: json.RawMessage(`{"data":{"gemini-2.5-pro":{"owned_by":"google"}}}`)},
		},
		AccountResponse: json.RawMessage(`{"success":true,"data":{"quota":1000000,"used_quota":250000,"today_income":50000}}`),
	}

	plan, err := buildManualSyncPlan(siteRecord, account, req)
	if err != nil {
		t.Fatalf("buildManualSyncPlan returned error: %v", err)
	}
	if plan.preview.ImportedTokenCount != 2 || plan.preview.TokenCount != 2 {
		t.Fatalf("expected two imported/final tokens, got imported=%d final=%d", plan.preview.ImportedTokenCount, plan.preview.TokenCount)
	}
	if plan.preview.ImportedGroupCount != 2 || plan.preview.GroupCount != 2 {
		t.Fatalf("expected two imported/final groups, got imported=%d final=%d", plan.preview.ImportedGroupCount, plan.preview.GroupCount)
	}
	if plan.preview.ImportedModelCount != 3 || plan.preview.ModelCount != 3 {
		t.Fatalf("expected three imported/final models, got imported=%d final=%d", plan.preview.ImportedModelCount, plan.preview.ModelCount)
	}
	if plan.snapshot.balance != 2 || plan.snapshot.balanceUsed != 0.5 || plan.snapshot.todayIncome != 0.1 {
		t.Fatalf("unexpected parsed balance snapshot: balance=%v used=%v income=%v", plan.snapshot.balance, plan.snapshot.balanceUsed, plan.snapshot.todayIncome)
	}
	for _, token := range plan.finalTokens {
		if token.Source != manualSyncSource {
			t.Fatalf("expected imported token source %q, got %q", manualSyncSource, token.Source)
		}
	}
	if plan.preview.PreviewFingerprint == "" {
		t.Fatalf("expected a non-empty preview fingerprint")
	}
}

func TestManualSyncReplacePreservesUnprovidedSections(t *testing.T) {
	ctx := setupProjectTestDB(t)
	_, account := createProjectionFixture(t, ctx)
	addManualSyncVIPFixture(t, ctx, account.ID)
	if err := dbpkg.GetDB().WithContext(ctx).Model(&model.SiteAccount{}).Where("id = ?", account.ID).Updates(map[string]any{
		"balance":      12.5,
		"balance_used": 4.25,
		"today_income": 0.75,
	}).Error; err != nil {
		t.Fatalf("update account balances failed: %v", err)
	}

	loaded, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil {
		t.Fatalf("SiteAccountGet failed: %v", err)
	}
	siteRecord, err := op.SiteGet(loaded.SiteID, ctx)
	if err != nil {
		t.Fatalf("SiteGet failed: %v", err)
	}
	enabled := true
	req := ManualSyncRequest{
		Mode:   ManualSyncModeReplace,
		Format: ManualSyncFormatSnapshot,
		Snapshot: &ManualSyncSnapshotInput{
			Tokens: &[]ManualSyncTokenInput{{Name: "primary", Token: "replacement-primary", GroupKey: "default", Enabled: &enabled}},
			Models: &map[string][]ManualSyncModelInput{
				"default": {{ModelName: "gpt-5"}},
			},
		},
	}
	plan, err := buildManualSyncPlan(siteRecord, loaded, req)
	if err != nil {
		t.Fatalf("buildManualSyncPlan returned error: %v", err)
	}
	if plan.preview.TokenCount != 1 {
		t.Fatalf("expected only the imported token after replacement, got %d", plan.preview.TokenCount)
	}
	if plan.preview.ModelCount != 2 {
		t.Fatalf("expected replaced default model plus preserved vip model, got %d", plan.preview.ModelCount)
	}
	if plan.snapshot.balance != 12.5 || plan.snapshot.balanceUsed != 4.25 || plan.snapshot.todayIncome != 0.75 {
		t.Fatalf("expected omitted balance section to preserve historical values, got %+v", plan.snapshot)
	}
	if err := persistSyncSnapshot(ctx, account.ID, plan.snapshot); err != nil {
		t.Fatalf("persistSyncSnapshot returned error: %v", err)
	}

	reloaded, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil {
		t.Fatalf("SiteAccountGet after persist failed: %v", err)
	}
	if hasModel(reloaded.Models, "default", "gpt-4o-mini") || !hasModel(reloaded.Models, "default", "gpt-5") || !hasModel(reloaded.Models, "vip", "gpt-4o-vip") {
		t.Fatalf("expected replace to remove old default models and preserve unmentioned vip models, got %+v", reloaded.Models)
	}
	if !hasToken(reloaded.Tokens, "default", "replacement-primary") || hasToken(reloaded.Tokens, "default", "key-backup") || hasToken(reloaded.Tokens, "vip", "key-vip") {
		t.Fatalf("expected replace to remove unmatched non-manual historical tokens, got %+v", reloaded.Tokens)
	}
	if _, ok := findSiteGroup(reloaded.UserGroups, "vip"); !ok {
		t.Fatalf("expected unprovided vip group to be preserved, got %+v", reloaded.UserGroups)
	}
}

func TestManualSyncRejectsMergeMode(t *testing.T) {
	siteRecord := &model.Site{ID: 12, Platform: model.SitePlatformNewAPI, Enabled: true}
	account := &model.SiteAccount{ID: 13, SiteID: siteRecord.ID, Enabled: true}
	req := ManualSyncRequest{
		Mode:   "merge",
		Format: ManualSyncFormatSnapshot,
		Snapshot: &ManualSyncSnapshotInput{
			Balance: floatPointer(1),
		},
	}
	if _, err := buildManualSyncPlan(siteRecord, account, req); !IsManualSyncValidationError(err) {
		t.Fatalf("expected merge mode to be rejected, got %v", err)
	}
}

func TestManualSyncDefaultsToReplaceMode(t *testing.T) {
	siteRecord := &model.Site{ID: 14, Platform: model.SitePlatformNewAPI, Enabled: true}
	account := &model.SiteAccount{ID: 15, SiteID: siteRecord.ID, Enabled: true}
	req := ManualSyncRequest{
		Format: ManualSyncFormatSnapshot,
		Snapshot: &ManualSyncSnapshotInput{
			Balance: floatPointer(1),
		},
	}
	plan, err := buildManualSyncPlan(siteRecord, account, req)
	if err != nil {
		t.Fatalf("buildManualSyncPlan returned error: %v", err)
	}
	if plan.preview.Mode != ManualSyncModeReplace {
		t.Fatalf("expected omitted mode to default to replace, got %q", plan.preview.Mode)
	}
}

func TestManualSyncReplaceCanClearExplicitModelGroup(t *testing.T) {
	siteRecord := &model.Site{ID: 16, Platform: model.SitePlatformNewAPI, Enabled: true}
	account := &model.SiteAccount{
		ID:      17,
		SiteID:  siteRecord.ID,
		Enabled: true,
		Tokens: []model.SiteToken{
			{Name: "primary", Token: "key-primary", GroupKey: "default", Enabled: true, Source: "sync"},
		},
		Models: []model.SiteModel{
			{GroupKey: "default", ModelName: "gpt-4o-mini", Source: "sync"},
			{GroupKey: "vip", ModelName: "gpt-4o-vip", Source: "sync"},
		},
	}
	req := ManualSyncRequest{
		Mode:   ManualSyncModeReplace,
		Format: ManualSyncFormatSnapshot,
		Snapshot: &ManualSyncSnapshotInput{
			Models: &map[string][]ManualSyncModelInput{"default": {}},
		},
	}
	plan, err := buildManualSyncPlan(siteRecord, account, req)
	if err != nil {
		t.Fatalf("buildManualSyncPlan returned error: %v", err)
	}
	if len(modelsForGroup(plan.finalModels, "default")) != 0 || !hasModel(plan.finalModels, "vip", "gpt-4o-vip") {
		t.Fatalf("expected empty explicit group to be cleared and vip models preserved, got %+v", plan.finalModels)
	}
	for _, group := range plan.preview.Groups {
		if group.GroupKey == "default" && (group.ModelAction != ManualSyncModeReplace || group.ModelCount != 0) {
			t.Fatalf("expected default preview to show an empty replacement, got %+v", group)
		}
	}
}

func TestManualSyncReplaceOnlyReplacesExplicitModelGroupsAndPreservesRouteState(t *testing.T) {
	ctx := setupProjectTestDB(t)
	_, account := createProjectionFixture(t, ctx)
	addManualSyncVIPFixture(t, ctx, account.ID)
	if err := dbpkg.GetDB().WithContext(ctx).Model(&model.SiteModel{}).
		Where("site_account_id = ? AND group_key = ? AND model_name = ?", account.ID, model.SiteDefaultGroupKey, "gpt-4o-mini").
		Updates(map[string]any{
			"route_type":      model.SiteModelRouteTypeAnthropic,
			"route_source":    model.SiteModelRouteSourceManualOverride,
			"manual_override": true,
			"disabled":        true,
		}).Error; err != nil {
		t.Fatalf("prepare manual route override failed: %v", err)
	}

	loaded, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil {
		t.Fatalf("SiteAccountGet failed: %v", err)
	}
	siteRecord, err := op.SiteGet(loaded.SiteID, ctx)
	if err != nil {
		t.Fatalf("SiteGet failed: %v", err)
	}
	req := ManualSyncRequest{
		Mode:   ManualSyncModeReplace,
		Format: ManualSyncFormatSnapshot,
		Snapshot: &ManualSyncSnapshotInput{
			Models: &map[string][]ManualSyncModelInput{
				"default": {{ModelName: "gpt-4o-mini"}},
			},
		},
	}
	plan, err := buildManualSyncPlan(siteRecord, loaded, req)
	if err != nil {
		t.Fatalf("buildManualSyncPlan returned error: %v", err)
	}
	if err := persistSyncSnapshot(ctx, account.ID, plan.snapshot); err != nil {
		t.Fatalf("persistSyncSnapshot returned error: %v", err)
	}

	reloaded, err := op.SiteAccountGet(account.ID, ctx)
	if err != nil {
		t.Fatalf("SiteAccountGet after persist failed: %v", err)
	}
	if len(modelsForGroup(reloaded.Models, model.SiteDefaultGroupKey)) != 1 {
		t.Fatalf("expected default group to be replaced by exactly one model, got %+v", modelsForGroup(reloaded.Models, model.SiteDefaultGroupKey))
	}
	if !hasModel(reloaded.Models, "vip", "gpt-4o-vip") {
		t.Fatalf("expected unmentioned vip group models to be preserved, got %+v", reloaded.Models)
	}
	defaultModel := modelsForGroup(reloaded.Models, model.SiteDefaultGroupKey)[0]
	if defaultModel.RouteType != model.SiteModelRouteTypeAnthropic || !defaultModel.ManualOverride || defaultModel.RouteSource != model.SiteModelRouteSourceManualOverride || !defaultModel.Disabled {
		t.Fatalf("expected manual route and disabled state to be preserved, got %+v", defaultModel)
	}
}

func TestManualSyncMaskedTokenWarnsAndCannotProject(t *testing.T) {
	siteRecord := &model.Site{ID: 3, Platform: model.SitePlatformNewAPI, Enabled: true}
	account := &model.SiteAccount{ID: 4, SiteID: siteRecord.ID, Enabled: true}
	enabled := true
	req := ManualSyncRequest{
		Mode:   ManualSyncModeReplace,
		Format: ManualSyncFormatSnapshot,
		Snapshot: &ManualSyncSnapshotInput{
			Tokens: &[]ManualSyncTokenInput{{Name: "masked", Token: "abcd********wxyz", GroupKey: "default", Enabled: &enabled}},
			Models: &map[string][]ManualSyncModelInput{"default": {{ModelName: "gpt-5"}}},
		},
	}
	plan, err := buildManualSyncPlan(siteRecord, account, req)
	if err != nil {
		t.Fatalf("buildManualSyncPlan returned error: %v", err)
	}
	if plan.preview.UsableTokenCount != 0 || plan.preview.MaskedTokenCount != 1 || plan.preview.ChannelCountEstimate != 0 {
		t.Fatalf("unexpected masked token preview: %+v", plan.preview)
	}
	if len(plan.finalTokens) != 1 || plan.finalTokens[0].ValueStatus != model.SiteTokenValueStatusMaskedPending || plan.finalTokens[0].Enabled {
		t.Fatalf("expected masked token to remain disabled and pending, got %+v", plan.finalTokens)
	}
	if !warningsContain(plan.preview.Warnings, "脱敏值") {
		t.Fatalf("expected masked token warning, got %+v", plan.preview.Warnings)
	}
	if len(plan.snapshot.groupResults) != 1 || plan.snapshot.groupResults[0].Status != siteGroupSyncStatusMissingKey {
		t.Fatalf("expected masked-only group to be marked missing_key, got %+v", plan.snapshot.groupResults)
	}
	if !strings.Contains(plan.snapshot.message, "清理 1 个缺少可用 Key 的分组历史投影") {
		t.Fatalf("expected missing-key cleanup message, got %q", plan.snapshot.message)
	}
}

func TestApplyManualSyncMissingKeyClearsHistoricalProjection(t *testing.T) {
	ctx := setupProjectTestDB(t)
	_, account := createProjectionFixture(t, ctx)

	if _, err := ProjectAccount(ctx, account.ID); err != nil {
		t.Fatalf("initial ProjectAccount failed: %v", err)
	}
	channelsByGroup := loadProjectedChannelsByGroupKey(t, ctx, account.ID)
	channel := channelsByGroup[model.SiteDefaultGroupKey]
	if channel.ID == 0 {
		t.Fatalf("expected initial projected channel")
	}
	consumerGroup := &model.Group{Name: "manual-sync-missing-key", Mode: model.GroupModeFailover}
	if err := op.GroupCreate(consumerGroup, ctx); err != nil {
		t.Fatalf("GroupCreate failed: %v", err)
	}
	if err := op.GroupItemAdd(&model.GroupItem{
		GroupID:   consumerGroup.ID,
		ChannelID: channel.ID,
		ModelName: "gpt-4o-mini",
		Priority:  1,
		Weight:    1,
	}, ctx); err != nil {
		t.Fatalf("GroupItemAdd failed: %v", err)
	}

	emptyTokens := []ManualSyncTokenInput{}
	req := ManualSyncRequest{
		Mode:   ManualSyncModeReplace,
		Format: ManualSyncFormatSnapshot,
		Snapshot: &ManualSyncSnapshotInput{
			Tokens: &emptyTokens,
		},
	}
	preview, err := PreviewManualSync(ctx, account.ID, req)
	if err != nil {
		t.Fatalf("PreviewManualSync returned error: %v", err)
	}
	if preview.ChannelCountEstimate != 0 || len(preview.Groups) != 1 || preview.Groups[0].WillProject {
		t.Fatalf("expected missing-key preview to be non-projectable, got %+v", preview)
	}
	req.PreviewFingerprint = preview.PreviewFingerprint
	result, err := ApplyManualSync(ctx, account.ID, req)
	if err != nil {
		t.Fatalf("ApplyManualSync returned error: %v", err)
	}
	if result.SyncResult.ChannelCount != 0 || len(result.SyncResult.GroupResults) != 1 || result.SyncResult.GroupResults[0].Status != string(siteGroupSyncStatusMissingKey) {
		t.Fatalf("expected missing-key apply result without projected channels, got %+v", result.SyncResult)
	}
	if result.SyncResult.GroupResults[0].ProjectionSuspended || result.SyncResult.GroupResults[0].ProjectionSuspendReason != "" {
		t.Fatalf("expected missing-key result to be non-suspended, got %+v", result.SyncResult.GroupResults[0])
	}
	if !strings.Contains(result.SyncResult.Message, "清理 1 个缺少可用 Key 的分组历史投影") {
		t.Fatalf("expected missing-key cleanup result message, got %q", result.SyncResult.Message)
	}

	channelsByGroup = loadProjectedChannelsByGroupKey(t, ctx, account.ID)
	if len(channelsByGroup) != 0 {
		t.Fatalf("expected historical projected channels to be removed, got %+v", channelsByGroup)
	}
	items, err := op.GroupItemList(consumerGroup.ID, ctx)
	if err != nil {
		t.Fatalf("GroupItemList failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected historical projected group items to be removed, got %+v", items)
	}
	var group model.SiteUserGroup
	if err := dbpkg.GetDB().WithContext(ctx).Where("site_account_id = ? AND group_key = ?", account.ID, model.SiteDefaultGroupKey).First(&group).Error; err != nil {
		t.Fatalf("query missing-key group failed: %v", err)
	}
	if group.ModelSyncStatus != model.SiteGroupModelSyncStatusMissingKey {
		t.Fatalf("expected persisted missing_key status, got %q", group.ModelSyncStatus)
	}
	if group.ProjectionSuspended || group.ProjectionSuspendReason != "" || group.ProjectionSuspendedAt != nil {
		t.Fatalf("expected persisted missing-key group to be non-suspended, got %+v", group)
	}
}

func TestApplyManualSyncEmptyClearsHistoricalProjection(t *testing.T) {
	ctx := setupProjectTestDB(t)
	_, account := createProjectionFixture(t, ctx)

	if _, err := ProjectAccount(ctx, account.ID); err != nil {
		t.Fatalf("initial ProjectAccount failed: %v", err)
	}
	channelsByGroup := loadProjectedChannelsByGroupKey(t, ctx, account.ID)
	channel := channelsByGroup[model.SiteDefaultGroupKey]
	if channel.ID == 0 {
		t.Fatalf("expected initial projected channel")
	}
	consumerGroup := &model.Group{Name: "manual-sync-empty", Mode: model.GroupModeFailover}
	if err := op.GroupCreate(consumerGroup, ctx); err != nil {
		t.Fatalf("GroupCreate failed: %v", err)
	}
	if err := op.GroupItemAdd(&model.GroupItem{
		GroupID:   consumerGroup.ID,
		ChannelID: channel.ID,
		ModelName: "gpt-4o-mini",
		Priority:  1,
		Weight:    1,
	}, ctx); err != nil {
		t.Fatalf("GroupItemAdd failed: %v", err)
	}

	req := ManualSyncRequest{
		Mode:   ManualSyncModeReplace,
		Format: ManualSyncFormatSnapshot,
		Snapshot: &ManualSyncSnapshotInput{
			Models: &map[string][]ManualSyncModelInput{model.SiteDefaultGroupKey: {}},
		},
	}
	preview, err := PreviewManualSync(ctx, account.ID, req)
	if err != nil {
		t.Fatalf("PreviewManualSync returned error: %v", err)
	}
	if preview.ChannelCountEstimate != 0 || len(preview.Groups) != 1 || preview.Groups[0].WillProject {
		t.Fatalf("expected empty preview to be non-projectable, got %+v", preview)
	}
	req.PreviewFingerprint = preview.PreviewFingerprint
	result, err := ApplyManualSync(ctx, account.ID, req)
	if err != nil {
		t.Fatalf("ApplyManualSync returned error: %v", err)
	}
	if result.SyncResult.ModelCount != 0 || result.SyncResult.ChannelCount != 0 || len(result.SyncResult.GroupResults) != 1 || result.SyncResult.GroupResults[0].Status != string(siteGroupSyncStatusEmpty) {
		t.Fatalf("expected empty apply result without models or projected channels, got %+v", result.SyncResult)
	}

	var modelCount int64
	if err := dbpkg.GetDB().WithContext(ctx).Model(&model.SiteModel{}).
		Where("site_account_id = ? AND group_key = ?", account.ID, model.SiteDefaultGroupKey).
		Count(&modelCount).Error; err != nil {
		t.Fatalf("count site models failed: %v", err)
	}
	if modelCount != 0 {
		t.Fatalf("expected empty sync to clear site models, got %d", modelCount)
	}
	channelsByGroup = loadProjectedChannelsByGroupKey(t, ctx, account.ID)
	if len(channelsByGroup) != 0 {
		t.Fatalf("expected empty sync to clear projected channels, got %+v", channelsByGroup)
	}
	if _, err := op.ChannelGet(channel.ID, ctx); err == nil {
		t.Fatalf("expected empty sync to delete projected channel %d", channel.ID)
	}
	items, err := op.GroupItemList(consumerGroup.ID, ctx)
	if err != nil {
		t.Fatalf("GroupItemList failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty sync to clear projected group items, got %+v", items)
	}
	var group model.SiteUserGroup
	if err := dbpkg.GetDB().WithContext(ctx).Where("site_account_id = ? AND group_key = ?", account.ID, model.SiteDefaultGroupKey).First(&group).Error; err != nil {
		t.Fatalf("query empty group failed: %v", err)
	}
	if group.ModelSyncStatus != model.SiteGroupModelSyncStatusEmpty || !group.ProjectionSuspended {
		t.Fatalf("expected persisted empty status to remain suspended after projection cleanup, got %+v", group)
	}
}

func TestManualSyncReplaceTokenSectionKeepsOnlyImportedAndManualTokens(t *testing.T) {
	siteRecord := &model.Site{ID: 8, Platform: model.SitePlatformNewAPI, Enabled: true}
	account := &model.SiteAccount{
		ID:      9,
		SiteID:  siteRecord.ID,
		Enabled: true,
		Tokens: []model.SiteToken{
			{ID: 1, Name: "primary", Token: "old-primary", GroupKey: "default", Enabled: true, Source: "sync"},
			{ID: 2, Name: "vip", Token: "old-vip", GroupKey: "vip", Enabled: true, Source: "sync"},
			{ID: 3, Name: "operator", Token: "manual-key", GroupKey: "ops", Enabled: true, Source: "manual"},
		},
	}
	enabled := true
	req := ManualSyncRequest{
		Mode:   ManualSyncModeReplace,
		Format: ManualSyncFormatSnapshot,
		Snapshot: &ManualSyncSnapshotInput{
			Tokens: &[]ManualSyncTokenInput{{Name: "primary", Token: "new-primary", GroupKey: "default", Enabled: &enabled}},
		},
	}
	plan, err := buildManualSyncPlan(siteRecord, account, req)
	if err != nil {
		t.Fatalf("buildManualSyncPlan returned error: %v", err)
	}
	if len(plan.finalTokens) != 2 {
		t.Fatalf("expected imported token plus preserved manual token, got %+v", plan.finalTokens)
	}
	if !hasToken(plan.finalTokens, "default", "new-primary") || !hasToken(plan.finalTokens, "ops", "manual-key") {
		t.Fatalf("expected imported and manual tokens, got %+v", plan.finalTokens)
	}
	if hasToken(plan.finalTokens, "default", "old-primary") || hasToken(plan.finalTokens, "vip", "old-vip") {
		t.Fatalf("expected omitted synced tokens to be removed, got %+v", plan.finalTokens)
	}
	if !warningsContain(plan.preview.Warnings, "手工维护的 Key") {
		t.Fatalf("expected preserved manual key warning, got %+v", plan.preview.Warnings)
	}
}

func TestApplyManualSyncRequiresPreviewFingerprint(t *testing.T) {
	ctx := setupProjectTestDB(t)
	_, account := createProjectionFixture(t, ctx)
	req := ManualSyncRequest{
		Mode:   ManualSyncModeReplace,
		Format: ManualSyncFormatSnapshot,
		Snapshot: &ManualSyncSnapshotInput{
			Models: &map[string][]ManualSyncModelInput{"default": {{ModelName: "gpt-5"}}},
		},
	}
	if _, err := ApplyManualSync(ctx, account.ID, req); !IsManualSyncValidationError(err) {
		t.Fatalf("expected apply without preview fingerprint to fail validation, got %v", err)
	}
	preview, err := PreviewManualSync(ctx, account.ID, req)
	if err != nil {
		t.Fatalf("PreviewManualSync returned error: %v", err)
	}
	req.PreviewFingerprint = preview.PreviewFingerprint
	result, err := ApplyManualSync(ctx, account.ID, req)
	if err != nil {
		t.Fatalf("ApplyManualSync returned error: %v", err)
	}
	if result.SyncResult.ModelCount != 1 || result.SyncResult.ChannelCount == 0 {
		t.Fatalf("expected one imported model and a projected channel, got %+v", result.SyncResult)
	}
	reloaded, err := op.SiteAccountGet(account.ID, context.Background())
	if err != nil {
		t.Fatalf("SiteAccountGet failed: %v", err)
	}
	if len(reloaded.Models) != 1 || reloaded.Models[0].ModelName != "gpt-5" {
		t.Fatalf("expected applied model replacement, got %+v", reloaded.Models)
	}
}

func addManualSyncVIPFixture(t *testing.T, ctx context.Context, accountID int) {
	t.Helper()
	group := model.SiteUserGroup{SiteAccountID: accountID, GroupKey: "vip", Name: "VIP"}
	if err := dbpkg.GetDB().WithContext(ctx).Create(&group).Error; err != nil {
		t.Fatalf("create vip group failed: %v", err)
	}
	token := model.SiteToken{SiteAccountID: accountID, Name: "vip", Token: "key-vip", GroupKey: "vip", GroupName: "VIP", Enabled: true, Source: "sync"}
	if err := dbpkg.GetDB().WithContext(ctx).Create(&token).Error; err != nil {
		t.Fatalf("create vip token failed: %v", err)
	}
	item := model.SiteModel{SiteAccountID: accountID, GroupKey: "vip", ModelName: "gpt-4o-vip", Source: "sync", RouteType: model.SiteModelRouteTypeOpenAIChat, RouteSource: model.SiteModelRouteSourceSyncInferred}
	if err := dbpkg.GetDB().WithContext(ctx).Create(&item).Error; err != nil {
		t.Fatalf("create vip model failed: %v", err)
	}
}

func hasModel(items []model.SiteModel, groupKey string, modelName string) bool {
	for _, item := range items {
		if model.NormalizeSiteGroupKey(item.GroupKey) == model.NormalizeSiteGroupKey(groupKey) && item.ModelName == modelName {
			return true
		}
	}
	return false
}

func hasToken(items []model.SiteToken, groupKey string, tokenValue string) bool {
	for _, item := range items {
		if model.NormalizeSiteGroupKey(item.GroupKey) == model.NormalizeSiteGroupKey(groupKey) && item.Token == tokenValue {
			return true
		}
	}
	return false
}

func warningsContain(items []string, value string) bool {
	for _, item := range items {
		if strings.Contains(item, value) {
			return true
		}
	}
	return false
}
