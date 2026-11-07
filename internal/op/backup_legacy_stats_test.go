package op

import (
	"encoding/json"
	"reflect"
	"testing"

	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestBackupWithoutLegacyStatsTable(t *testing.T) {
	ctx := setupBackupTestDB(t)
	conn := dbpkg.GetDB().WithContext(ctx)
	if conn.Migrator().HasTable(&model.StatsModel{}) {
		t.Fatal("fresh database created the legacy statistics table")
	}

	dump, err := DBExportAll(ctx, false, true)
	if err != nil {
		t.Fatalf("export current statistics: %v", err)
	}
	if len(dump.StatsModel) != 0 {
		t.Fatalf("unexpected legacy statistics: %+v", dump.StatsModel)
	}
	var buf bytesBuffer
	if err := DBExportZip(ctx, &buf, false, true); err != nil {
		t.Fatalf("export ZIP without legacy table: %v", err)
	}
	zr, err := zipReaderFromBytes(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var rows []model.StatsModel
	if err := json.Unmarshal([]byte(readZipFile(t, zr, "stats_model.json")), &rows); err != nil {
		t.Fatalf("decode empty legacy ZIP entry: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("unexpected legacy ZIP rows: %+v", rows)
	}
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatalf("restore current backup: %v", err)
	}
	if conn.Migrator().HasTable(&model.StatsModel{}) {
		t.Fatal("backup roundtrip recreated the unused legacy table")
	}
}

func TestBackupPreservesExistingLegacyStats(t *testing.T) {
	ctx := setupBackupTestDB(t)
	conn := dbpkg.GetDB().WithContext(ctx)
	channel := model.Channel{ID: 17, Name: "legacy-channel", Enabled: true}
	if err := conn.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Migrator().CreateTable(&model.StatsModel{}); err != nil {
		t.Fatal(err)
	}
	want := []model.StatsModel{
		{ID: 9, ChannelID: channel.ID, Name: "legacy-model", StatsMetrics: model.StatsMetrics{InputToken: 1234, InputCost: 0.125, RequestSuccess: 3}},
		{ID: 10, ChannelID: channel.ID, Name: "another-model", StatsMetrics: model.StatsMetrics{RequestFailed: 2}},
	}
	if err := conn.Create(&want).Error; err != nil {
		t.Fatal(err)
	}
	dump, err := DBExportAll(ctx, false, true)
	if err != nil {
		t.Fatalf("export legacy statistics: %v", err)
	}
	if !reflect.DeepEqual(dump.StatsModel, want) {
		t.Fatalf("JSON export changed legacy statistics: got %+v, want %+v", dump.StatsModel, want)
	}
	var buf bytesBuffer
	if err := DBExportZip(ctx, &buf, false, true); err != nil {
		t.Fatalf("export legacy statistics ZIP: %v", err)
	}
	zr, err := zipReaderFromBytes(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var rows []model.StatsModel
	if err := json.Unmarshal([]byte(readZipFile(t, zr, "stats_model.json")), &rows); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("ZIP export changed legacy statistics: got %+v, want %+v", rows, want)
	}
	result, err := DBImportIncremental(ctx, dump)
	if err != nil {
		t.Fatalf("restore exported legacy statistics: %v", err)
	}
	if result.RowsAffected["stats_model"] != 0 {
		t.Fatalf("restoring a backup duplicated existing statistics: %+v", result.RowsAffected)
	}
}

func TestLegacyStatsImportDeduplicatesAfterChannelRemapping(t *testing.T) {
	ctx := setupBackupTestDB(t)
	conn := dbpkg.GetDB().WithContext(ctx)
	if err := conn.Create(&model.Channel{ID: 1, Name: "unrelated-channel", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	dump := &model.DBDump{
		Version:      dbDumpVersion,
		IncludeStats: true,
		Channels:     []model.Channel{{ID: 1, Name: "imported-channel", Enabled: true}},
		StatsModel: []model.StatsModel{
			{ID: 101, ChannelID: 1, Name: "gpt-4", StatsMetrics: model.StatsMetrics{InputToken: 100, InputCost: 0.125, RequestSuccess: 1}},
			{ID: 102, ChannelID: 1, Name: "gpt-4", StatsMetrics: model.StatsMetrics{InputToken: 100, InputCost: 0.125, RequestSuccess: 1}},
			{ID: 103, ChannelID: 1, Name: "gpt-4", StatsMetrics: model.StatsMetrics{}},
			{ID: 104, ChannelID: 999, Name: "orphan", StatsMetrics: model.StatsMetrics{RequestSuccess: 2}},
		},
	}
	for attempt, want := range []int64{2, 0} {
		result, err := DBImportIncremental(ctx, dump)
		if err != nil {
			t.Fatalf("import attempt %d: %v", attempt+1, err)
		}
		if got := result.RowsAffected["stats_model"]; got != want {
			t.Fatalf("import attempt %d inserted %d legacy rows, want %d", attempt+1, got, want)
		}
	}
	var channel model.Channel
	if err := conn.Where("name = ?", "imported-channel").First(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if channel.ID == 1 {
		t.Fatal("import reused an unrelated channel ID")
	}
	var rows []model.StatsModel
	if err := conn.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected two distinct historical snapshots, got %+v", rows)
	}
	for i, source := range []model.StatsModel{dump.StatsModel[0], dump.StatsModel[2]} {
		if rows[i].ChannelID != channel.ID || rows[i].Name != source.Name || rows[i].StatsMetrics != source.StatsMetrics {
			t.Fatalf("legacy snapshot was changed or linked to the wrong channel: %+v", rows[i])
		}
	}
}

func TestLegacyStatsImportHonorsIncludeStats(t *testing.T) {
	ctx := setupBackupTestDB(t)
	dump := &model.DBDump{
		Version:    dbDumpVersion,
		Channels:   []model.Channel{{ID: 1, Name: "imported-channel"}},
		StatsModel: []model.StatsModel{{ID: 1, ChannelID: 1, Name: "ignored-model"}},
	}
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatal(err)
	}
	if dbpkg.GetDB().Migrator().HasTable(&model.StatsModel{}) {
		t.Fatal("disabled statistics import created the legacy table")
	}
}
