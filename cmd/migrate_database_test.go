package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	v2 "github.com/suprelory/octopus-plus/internal/migration/v2"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func createMigrationCommandDatabase(t *testing.T, path string) {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []any{
		&model.Site{}, &model.SiteAccount{}, &model.Channel{}, &model.ChannelKey{}, &model.ProxyConfiguration{},
		&model.SiteUserGroup{}, &model.SiteChannelBinding{}, &model.Group{}, &model.GroupItem{}, &model.GroupPreset{},
		&model.Setting{}, &model.StatsChannel{}, &model.StatsSiteModelHourly{}, &model.CheckinLog{}, &model.CheckinBatchJob{},
	} {
		if err := conn.Migrator().CreateTable(table); err != nil {
			t.Fatalf("create %T: %v", table, err)
		}
	}
	job := model.CheckinBatchJob{ID: 31, Status: model.SiteCheckinBatchJobStatusRunning, StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := conn.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	pool, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
}

func executeDatabaseMigrationCommand(config, snapshot, backup string, apply, stopped bool) (string, error) {
	command := newMigrationCommand()
	var output, diagnostics bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&diagnostics)
	args := []string{"v2-database", "--config", config, "--snapshot", snapshot}
	if apply {
		args = append(args, "--apply")
	}
	if stopped {
		args = append(args, "--writers-stopped")
	}
	if backup != "" {
		args = append(args, "--backup", backup)
	}
	command.SetArgs(args)
	err := command.Execute()
	return output.String(), err
}

func TestDatabaseMigrationCommand(t *testing.T) {
	t.Setenv("OCTOPUS_DATABASE_TYPE", "")
	t.Setenv("OCTOPUS_DATABASE_PATH", "")
	directory := t.TempDir()
	database := filepath.Join(directory, "v1.db")
	config := filepath.Join(directory, "config.json")
	snapshot := filepath.Join(directory, "checkpoint.json")
	backup := filepath.Join(directory, "backup.db")
	createMigrationCommandDatabase(t, database)
	configData, err := json.Marshal(map[string]any{"database": map[string]string{"type": "sqlite", "path": database}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, configData, 0600); err != nil {
		t.Fatal(err)
	}

	beforePreview, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	output, err := executeDatabaseMigrationCommand(config, snapshot, "", false, false)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	var preview v2.MigrationResult
	if err := json.Unmarshal([]byte(output), &preview); err != nil {
		t.Fatalf("preview output is not JSON: %v", err)
	}
	if preview.Completed || preview.NextStage != v2.StageSites || preview.Report.InterruptedBatches != 1 {
		t.Fatalf("unexpected preview report: %+v", preview)
	}
	afterPreview, err := os.ReadFile(database)
	if err != nil || !bytes.Equal(beforePreview, afterPreview) {
		t.Fatal("preview modified the SQLite database")
	}
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatalf("checkpoint was not created: %v", err)
	}

	if err := os.WriteFile(backup, beforePreview, 0600); err != nil {
		t.Fatal(err)
	}
	liveAlias := filepath.Join(directory, "live-alias")
	if err := os.Link(database, liveAlias); err != nil {
		t.Fatal(err)
	}
	checkpointAlias := filepath.Join(directory, "checkpoint-alias")
	if err := os.Link(snapshot, checkpointAlias); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(directory, "empty")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, checkpoint, rollback string
		apply, stopped             bool
	}{
		{"missing confirmation", snapshot, backup, true, false},
		{"missing checkpoint", filepath.Join(directory, "missing"), backup, true, true},
		{"missing backup", snapshot, "", true, true},
		{"empty backup", snapshot, empty, true, true},
		{"live checkpoint", liveAlias, "", false, false},
		{"checkpoint as backup", snapshot, checkpointAlias, true, true},
		{"live backup", snapshot, liveAlias, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := executeDatabaseMigrationCommand(config, tc.checkpoint, tc.rollback, tc.apply, tc.stopped)
			if err == nil || out != "" {
				t.Fatalf("unexpected command result: %q, %v", out, err)
			}
		})
	}
	output, err = executeDatabaseMigrationCommand(config, snapshot, backup, true, true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var applied v2.MigrationResult
	if err := json.Unmarshal([]byte(output), &applied); err != nil || !applied.Completed {
		t.Fatalf("apply output is not a completed JSON report: %v, %+v", err, applied)
	}

	conn, err := gorm.Open(sqlite.Open(database), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	var job model.CheckinBatchJob
	if err := conn.First(&job, 31).Error; err != nil || job.Status != model.SiteCheckinBatchJobStatusInterrupted || job.ErrorMessage != "Interrupted by v2 migration" {
		t.Fatalf("legacy batch did not receive the migration interruption state: %+v, %v", job, err)
	}
	if !conn.Migrator().HasTable(&model.CheckinSite{}) || !conn.Migrator().HasTable(&v2.MigrationRun{}) {
		t.Fatal("apply did not prepare the new schema and migration journal")
	}
	pool, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}

	output, err = executeDatabaseMigrationCommand(config, snapshot, backup, true, true)
	if err != nil {
		t.Fatalf("resume completed migration: %v", err)
	}
	if err := json.Unmarshal([]byte(output), &applied); err != nil || !applied.Completed {
		t.Fatalf("resume output is not a completed JSON report: %v, %+v", err, applied)
	}
	if contents, err := os.ReadFile(backup); err != nil || !bytes.Equal(beforePreview, contents) {
		t.Fatal("apply or resume modified the rollback backup")
	}
}

func TestDatabaseMigrationCommandEnvironmentOnly(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "v1.db")
	createMigrationCommandDatabase(t, path)
	t.Setenv("OCTOPUS_DATABASE_PATH", path)
	t.Setenv("OCTOPUS_DATABASE_TYPE", "sqlite")
	out, err := executeDatabaseMigrationCommand("", filepath.Join(dir, "snapshot.json"), "", false, false)
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("preview using environment: %v", err)
	}
	if _, err := os.Stat("data"); !os.IsNotExist(err) {
		t.Fatal("preview created the default configuration directory")
	}
}

func TestDatabaseMigrationCommandDoesNotCreateMissingConfig(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing.json")
	output, err := executeDatabaseMigrationCommand(missing, filepath.Join(directory, "checkpoint.json"), "", false, false)
	if err == nil || output != "" {
		t.Fatal("migration accepted a missing explicit config")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("migration created the missing config")
	}
}
