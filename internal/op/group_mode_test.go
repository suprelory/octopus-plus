package op

import (
	"fmt"
	"strings"
	"testing"

	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestGroupModesPreservedAcrossPresets(t *testing.T) {
	ctx := setupChannelSupportTestDB(t)
	group := &model.Group{Name: "mode-group"}
	if err := GroupCreate(group, ctx); err != nil {
		t.Fatal(err)
	}
	if group.Mode != model.GroupModeRoundRobin {
		t.Fatalf("default mode = %d", group.Mode)
	}
	for _, mode := range []model.GroupMode{model.GroupModeRoundRobin, model.GroupModeFailover, model.GroupModeWeighted} {
		if _, err := GroupUpdate(&model.GroupUpdateRequest{ID: group.ID, Mode: &mode}, ctx); err != nil {
			t.Fatalf("update mode %d: %v", mode, err)
		}
		preset, err := GroupPresetCreate(group.ID, fmt.Sprintf("mode-%d", mode), ctx)
		if err != nil {
			t.Fatalf("snapshot mode %d: %v", mode, err)
		}
		clone, err := GroupPresetClone(preset.ID, preset.Name+"-copy", ctx)
		if err != nil {
			t.Fatalf("clone mode %d: %v", mode, err)
		}
		if preset.Mode != mode || clone.Mode != mode {
			t.Fatalf("mode %d changed: snapshot=%d clone=%d", mode, preset.Mode, clone.Mode)
		}
		if err := GroupPresetActivate(clone.ID, ctx); err != nil {
			t.Fatalf("activate mode %d: %v", mode, err)
		}
		cached, err := GroupGetEnabledMap(group.Name, ctx)
		if err != nil || cached.Mode != mode {
			t.Fatalf("activated mode = %d, want %d; error=%v", cached.Mode, mode, err)
		}
	}
}

func TestGroupModeValidationPreservesExistingConfiguration(t *testing.T) {
	ctx := setupChannelSupportTestDB(t)
	group := &model.Group{Name: "preserved-group", Mode: model.GroupModeWeighted}
	if err := GroupCreate(group, ctx); err != nil {
		t.Fatal(err)
	}
	preset, err := GroupPresetCreate(group.ID, "preserved-preset", ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []model.GroupMode{-1, 0, 2, 99} {
		name := fmt.Sprintf("invalid-mode-%d", mode)
		if mode != 0 {
			invalid := &model.Group{Name: name, Mode: mode}
			if err := GroupCreate(invalid, ctx); err == nil || !strings.Contains(err.Error(), "invalid group mode") {
				t.Fatalf("create mode %d: %v", mode, err)
			}
		}
		if _, err := GroupUpdate(&model.GroupUpdateRequest{ID: group.ID, Name: &name, Mode: &mode}, ctx); err == nil || !strings.Contains(err.Error(), "invalid group mode") {
			t.Fatalf("update mode %d: %v", mode, err)
		}
		if _, err := GroupPresetUpdate(preset.ID, &model.GroupPresetUpdateRequest{Name: &name, Mode: &mode}, ctx); err == nil || !strings.Contains(err.Error(), "invalid group mode") {
			t.Fatalf("update preset mode %d: %v", mode, err)
		}
	}
	var savedGroup model.Group
	if err := dbpkg.GetDB().First(&savedGroup, group.ID).Error; err != nil {
		t.Fatal(err)
	}
	if savedGroup.Name != group.Name || savedGroup.Mode != group.Mode {
		t.Fatalf("invalid update changed the group: %+v", savedGroup)
	}
	var savedPreset model.GroupPreset
	if err := dbpkg.GetDB().First(&savedPreset, preset.ID).Error; err != nil {
		t.Fatal(err)
	}
	if savedPreset.Name != preset.Name || savedPreset.Mode != preset.Mode {
		t.Fatalf("invalid update changed the preset: %+v", savedPreset)
	}
	cached, err := GroupGetEnabledMap(group.Name, ctx)
	if err != nil || cached.Mode != group.Mode {
		t.Fatalf("invalid update changed cached mode: %d; error=%v", cached.Mode, err)
	}
}
