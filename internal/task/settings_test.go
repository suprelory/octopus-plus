package task

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestTaskIntervalsApplyOnSettingWritesAndRestore(t *testing.T) {
	resetTaskScheduler(t)
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "settings.db"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(initIntervalTasks())
	assertInterval := func(spec intervalTask, count int) {
		t.Helper()
		tasksMu.RLock()
		entry := tasks[spec.name]
		var got time.Duration
		if entry != nil {
			got = entry.interval
		}
		tasksMu.RUnlock()
		if entry == nil || got != time.Duration(count)*spec.unit {
			t.Fatalf("%s interval = %v, want %v", spec.name, got, time.Duration(count)*spec.unit)
		}
	}
	dump := model.DBDump{Version: 1}
	for _, spec := range intervalTasks() {
		for _, value := range []int{0, 3, 0, 1} {
			if err := op.SettingSetInt(spec.key, value); err != nil {
				t.Fatal(err)
			}
			assertInterval(spec, value)
		}
		dump.Settings = append(dump.Settings, model.Setting{Key: spec.key, Value: "7"})
	}
	if _, err := op.DBImportIncremental(context.Background(), &dump); err != nil {
		t.Fatal(err)
	}
	if err := op.RefreshCacheAfterImport(false); err != nil {
		t.Fatal(err)
	}
	for _, spec := range intervalTasks() {
		assertInterval(spec, 7)
	}
	// An invalid imported interval disables that schedule and surfaces an error.
	dump.Settings = []model.Setting{{Key: model.SettingKeyStatsSaveInterval, Value: "-1"}}
	if _, err := op.DBImportIncremental(context.Background(), &dump); err != nil {
		t.Fatal(err)
	}
	if err := op.RefreshCacheAfterImport(false); err == nil {
		t.Fatal("invalid imported schedule was reported as successfully applied")
	}
	assertInterval(intervalTask{name: TaskStatsSave, unit: time.Minute}, 0)
}
