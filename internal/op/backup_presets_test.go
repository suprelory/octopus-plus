package op

import (
	"encoding/json"
	"testing"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestBackupPresetsRoundTripAndRemap(t *testing.T) {
	ctx := setupBackupTestDB(t)
	presetID := 70
	channel := model.Channel{ID: 80, Name: "source-channel", Type: outbound.OutboundTypeOpenAIChat, Enabled: true}
	group := model.Group{ID: 90, Name: "source-group", Mode: model.GroupModeFailover, ActivePresetID: &presetID}
	preset := model.GroupPreset{ID: presetID, GroupID: group.ID, Name: "primary", Mode: group.Mode, MatchRegex: "^model", RetryEnabled: true, MaxRetries: 2, Items: []model.GroupPresetItem{{ChannelID: channel.ID, ModelName: "model", Priority: 2, Weight: 3}, {ChannelID: 79, ModelName: "orphan"}}}
	for _, row := range []any{&channel, &group, &preset} {
		if err := db.GetDB().Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	dump, err := DBExportAll(ctx, false, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}
	var restored model.DBDump
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.GroupPresets) != 1 {
		t.Fatal("JSON export omitted presets")
	}
	var zip bytesBuffer
	if err := DBExportZip(ctx, &zip, false, false); err != nil {
		t.Fatal(err)
	}
	reader, err := zipReaderFromBytes(zip.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var zipped []model.GroupPreset
	if err := json.Unmarshal([]byte(readZipFile(t, reader, "group_presets.json")), &zipped); err != nil || len(zipped) != 1 {
		t.Fatalf("ZIP export omitted presets: %v", err)
	}

	ctx = setupBackupTestDB(t)
	// Occupy the original IDs, including an orphaned item ID, to expose any
	// reference accidentally copied without destination remapping.
	for _, row := range []any{
		&model.Channel{ID: 79, Name: "unrelated-orphan", Type: outbound.OutboundTypeOpenAIChat},
		&model.Channel{ID: 80, Name: "unrelated-channel", Type: outbound.OutboundTypeOpenAIChat},
		&model.Group{ID: 90, Name: "unrelated-group", Mode: model.GroupModeRoundRobin},
		&model.GroupPreset{ID: 70, GroupID: 90, Name: "unrelated-preset", Mode: model.GroupModeRoundRobin},
	} {
		if err := db.GetDB().Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	result, err := DBImportIncremental(ctx, &restored)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsAffected["group_presets"] != 1 {
		t.Fatalf("presets not imported: %+v", result)
	}
	var gotGroup model.Group
	var gotChannel model.Channel
	var gotPreset model.GroupPreset
	if err := db.GetDB().Where("name = ?", group.Name).First(&gotGroup).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Where("name = ?", channel.Name).First(&gotChannel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Where("group_id = ? AND name = ?", gotGroup.ID, preset.Name).First(&gotPreset).Error; err != nil {
		t.Fatal(err)
	}
	if gotGroup.ID == group.ID || gotChannel.ID == channel.ID || gotPreset.ID == presetID {
		t.Fatal("fixture failed to remap IDs")
	}
	if gotGroup.ActivePresetID == nil || *gotGroup.ActivePresetID != gotPreset.ID {
		t.Fatalf("active preset not remapped: %+v", gotGroup)
	}
	if len(gotPreset.Items) != 1 || gotPreset.Items[0].ChannelID != gotChannel.ID || gotPreset.Items[0].Weight != 3 || gotPreset.MatchRegex != "^model" || !gotPreset.RetryEnabled {
		t.Fatalf("preset contents not preserved: %+v", gotPreset)
	}
	if err := db.GetDB().Model(&gotGroup).Update("active_preset_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Model(&gotPreset).Update("match_regex", "local-change").Error; err != nil {
		t.Fatal(err)
	}
	result, err = DBImportIncremental(ctx, &restored)
	if err != nil || result.RowsAffected["group_presets"] != 0 {
		t.Fatalf("repeat import not idempotent: %+v %v", result, err)
	}
	gotGroup = model.Group{}
	if err := db.GetDB().Where("name = ?", group.Name).First(&gotGroup).Error; err != nil {
		t.Fatal(err)
	}
	if gotGroup.ActivePresetID != nil {
		t.Fatal("existing group's active preset overwritten")
	}
	if err := db.GetDB().First(&gotPreset, gotPreset.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotPreset.MatchRegex != "local-change" {
		t.Fatal("existing preset overwritten")
	}
}

func TestLegacyBackupClearsDanglingActivePreset(t *testing.T) {
	ctx := setupBackupTestDB(t)
	id := 7
	dump := &model.DBDump{Version: dbDumpVersion, Groups: []model.Group{{ID: 1, Name: "legacy", Mode: model.GroupModeFailover, ActivePresetID: &id}}}
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatal(err)
	}
	var group model.Group
	if err := db.GetDB().Where("name = ?", "legacy").First(&group).Error; err != nil {
		t.Fatal(err)
	}
	if group.ActivePresetID != nil {
		t.Fatal("legacy backup retained an unresolved preset ID")
	}
}

func TestInvalidPresetRollsBackImport(t *testing.T) {
	ctx := setupBackupTestDB(t)
	dump := &model.DBDump{Version: dbDumpVersion, Groups: []model.Group{{ID: 1, Name: "rollback", Mode: model.GroupModeFailover}}, GroupPresets: []model.GroupPreset{{ID: 1, GroupID: 1, Name: "invalid", Mode: 0}}}
	if _, err := DBImportIncremental(ctx, dump); err == nil {
		t.Fatal("invalid preset accepted")
	}
	var count int64
	if err := db.GetDB().Model(&model.Group{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("preset failure left partially imported group")
	}
}
