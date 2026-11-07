package op

import (
	"context"
	"strings"
	"testing"

	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func setupChannelSupportTestDB(t *testing.T) context.Context {
	t.Helper()
	ctx := setupSiteOpTestDB(t)
	channelCache.Clear()
	channelKeyCache.Clear()
	groupCache.Clear()
	groupMap.Clear()
	enabledGroupCache.Clear()
	return ctx
}

func seedChannelSupportFixtures(t *testing.T, ctx context.Context) (*model.Group, *model.Channel, *model.Channel) {
	t.Helper()
	group := &model.Group{Name: "channel-support-group", Mode: model.GroupModeRoundRobin}
	if err := GroupCreate(group, ctx); err != nil {
		t.Fatalf("GroupCreate failed: %v", err)
	}

	supported := &model.Channel{Name: "supported-channel", Type: outbound.OutboundTypeOpenAIChat, Enabled: true, Model: "gpt-4o"}
	if err := ChannelCreate(supported, ctx); err != nil {
		t.Fatalf("ChannelCreate supported failed: %v", err)
	}

	legacy := &model.Channel{Name: "legacy-volcengine-channel", Type: outbound.OutboundType(-1), Enabled: true, Model: "doubao-seed-1-6"}
	if err := dbpkg.GetDB().WithContext(ctx).Create(legacy).Error; err != nil {
		t.Fatalf("create legacy channel failed: %v", err)
	}
	// Seed the cache directly to exercise the selection guard against an
	// unsupported channel that is marked enabled.
	channelCache.Set(legacy.ID, *legacy)
	return group, supported, legacy
}

func TestRemovedChannelCannotEnterGroupsOrRouting(t *testing.T) {
	ctx := setupChannelSupportTestDB(t)
	group, _, legacy := seedChannelSupportFixtures(t, ctx)

	if err := GroupItemAdd(&model.GroupItem{GroupID: group.ID, ChannelID: legacy.ID, ModelName: "doubao-seed-1-6"}, ctx); err == nil || !strings.Contains(err.Error(), "unsupported channel type") {
		t.Fatalf("GroupItemAdd error = %v, want unsupported channel type", err)
	}
	if err := GroupItemBatchAdd(group.ID, []model.GroupIDAndLLMName{{ChannelID: legacy.ID, ModelName: "doubao-seed-1-6"}}, ctx); err == nil || !strings.Contains(err.Error(), "unsupported channel type") {
		t.Fatalf("GroupItemBatchAdd error = %v, want unsupported channel type", err)
	}
	if _, err := GroupUpdate(&model.GroupUpdateRequest{ID: group.ID, ItemsToAdd: []model.GroupItemAddRequest{{ChannelID: legacy.ID, ModelName: "doubao-seed-1-6"}}}, ctx); err == nil || !strings.Contains(err.Error(), "unsupported channel type") {
		t.Fatalf("GroupUpdate error = %v, want unsupported channel type", err)
	}

	if err := dbpkg.GetDB().WithContext(ctx).Create(&model.GroupItem{GroupID: group.ID, ChannelID: legacy.ID, ModelName: "legacy"}).Error; err != nil {
		t.Fatalf("seed legacy group item failed: %v", err)
	}
	if err := groupRefreshCacheByID(group.ID, ctx); err != nil {
		t.Fatalf("refresh group cache failed: %v", err)
	}
	filtered, err := GroupGetEnabledMap(group.Name, ctx)
	if err != nil {
		t.Fatalf("GroupGetEnabledMap failed: %v", err)
	}
	if len(filtered.Items) != 0 {
		t.Fatalf("expected unsupported group item to be filtered, got %+v", filtered.Items)
	}
}

func TestRemovedChannelCannotBeUsedByGroupPresetsOrAutoGroup(t *testing.T) {
	ctx := setupChannelSupportTestDB(t)
	group, supported, legacy := seedChannelSupportFixtures(t, ctx)
	items := []model.GroupPresetItem{{ChannelID: legacy.ID, ModelName: "doubao-seed-1-6", Priority: 1, Weight: 1}}
	preset := &model.GroupPreset{
		GroupID: group.ID,
		Name:    "legacy-preset",
		Mode:    model.GroupModeRoundRobin,
		Items:   items,
	}
	if err := dbpkg.GetDB().WithContext(ctx).Create(preset).Error; err != nil {
		t.Fatalf("seed legacy preset failed: %v", err)
	}
	if err := dbpkg.GetDB().WithContext(ctx).Create(&model.GroupItem{GroupID: group.ID, ChannelID: legacy.ID, ModelName: "legacy"}).Error; err != nil {
		t.Fatalf("seed legacy group item failed: %v", err)
	}
	if err := groupRefreshCacheByID(group.ID, ctx); err != nil {
		t.Fatalf("refresh group cache failed: %v", err)
	}

	if err := GroupPresetActivate(preset.ID, ctx); err == nil || !strings.Contains(err.Error(), "unsupported channel type") {
		t.Fatalf("GroupPresetActivate error = %v, want unsupported channel type", err)
	}
	if _, err := GroupPresetClone(preset.ID, "legacy-preset-copy", ctx); err == nil || !strings.Contains(err.Error(), "unsupported channel type") {
		t.Fatalf("GroupPresetClone error = %v, want unsupported channel type", err)
	}
	if _, err := GroupPresetUpdate(preset.ID, &model.GroupPresetUpdateRequest{Items: &items}, ctx); err == nil || !strings.Contains(err.Error(), "unsupported channel type") {
		t.Fatalf("GroupPresetUpdate error = %v, want unsupported channel type", err)
	}
	if _, err := GroupPresetCreate(group.ID, "legacy-preset-snapshot", ctx); err == nil || !strings.Contains(err.Error(), "unsupported channel type") {
		t.Fatalf("GroupPresetCreate error = %v, want unsupported channel type", err)
	}

	if err := ChannelAutoGroupUpdate(legacy.ID, model.AutoGroupTypeFuzzy, ctx); err == nil || !strings.Contains(err.Error(), "unsupported channel type") {
		t.Fatalf("ChannelAutoGroupUpdate error = %v, want unsupported channel type", err)
	}
	if err := RunGroupAutoGroup([]int{legacy.ID}, ctx); err == nil || !strings.Contains(err.Error(), "unsupported channel type") {
		t.Fatalf("RunGroupAutoGroup error = %v, want unsupported channel type", err)
	}

	config, err := GroupAutoGroupConfigGet(ctx)
	if err != nil {
		t.Fatalf("GroupAutoGroupConfigGet failed: %v", err)
	}
	if len(config.Sources) != 1 || config.Sources[0].ChannelID != supported.ID {
		t.Fatalf("expected only supported auto-group source, got %+v", config.Sources)
	}
}

func TestRemovedChannelIsExcludedFromModelPicker(t *testing.T) {
	ctx := setupChannelSupportTestDB(t)
	group, supported, legacy := seedChannelSupportFixtures(t, ctx)

	if err := dbpkg.GetDB().WithContext(ctx).Create(&model.GroupItem{
		GroupID: group.ID, ChannelID: supported.ID, ModelName: "gpt-4o",
	}).Error; err != nil {
		t.Fatalf("seed supported group item failed: %v", err)
	}
	if err := dbpkg.GetDB().WithContext(ctx).Create(&model.GroupItem{
		GroupID: group.ID, ChannelID: legacy.ID, ModelName: "doubao-seed-1-6",
	}).Error; err != nil {
		t.Fatalf("seed legacy group item failed: %v", err)
	}

	if err := groupRefreshCacheByID(group.ID, ctx); err != nil {
		t.Fatalf("refresh group cache failed: %v", err)
	}
	models, err := ChannelLLMList(ctx)
	if err != nil {
		t.Fatalf("ChannelLLMList failed: %v", err)
	}
	for _, item := range models {
		if item.ChannelID == legacy.ID {
			t.Fatalf("removed channel must not appear in model picker: %+v", item)
		}
	}
	if len(models) != 1 || models[0].ChannelID != supported.ID {
		t.Fatalf("expected only supported model picker entry, got %+v", models)
	}
}
