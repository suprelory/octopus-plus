package v2

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type journalTestRecord struct {
	ID    int `gorm:"primaryKey"`
	Value string
}

func journalDatabase(t *testing.T) (*Journal, *gorm.DB) {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migration.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = pool.Close() })
	return prepareJournalDatabase(t, conn), conn
}

func prepareJournalDatabase(t *testing.T, conn *gorm.DB) *Journal {
	t.Helper()
	if err := PrepareJournalSchema(conn); err != nil {
		t.Fatal(err)
	}
	if err := PrepareJournalSchema(conn); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sites", "site_accounts", "checkin_sites", "checkin_accounts", "channels"} {
		if conn.Migrator().HasTable(table) {
			t.Fatal("journal initialized business tables")
		}
	}
	if err := conn.AutoMigrate(&journalTestRecord{}); err != nil {
		t.Fatal(err)
	}
	return NewJournal(conn)
}

func TestMigrationJournalAtomicRollbackResumeAndSourceIdentity(t *testing.T) {
	j, conn := journalDatabase(t)
	testJournalAtomicRollbackResume(t, j, conn)
}

func testJournalAtomicRollbackResume(t *testing.T, j *Journal, conn *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	run, err := j.Open(ctx, "database-fixture", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	upper, err := j.Open(ctx, "DATABASE-FIXTURE", strings.Repeat("A", 64))
	if err != nil || upper.ID != run.ID || upper.Source != run.Source || upper.Fingerprint != run.Fingerprint {
		t.Fatalf("source identity depends on collation: %v", err)
	}
	otherRun := *run
	otherRun.Fingerprint = strings.Repeat("b", 64)
	if _, err := j.Mappings(ctx, otherRun); !errors.Is(err, ErrMigrationInputChanged) {
		t.Fatal("mapping lookup accepted mismatched run identity")
	}
	if _, err := j.Open(ctx, "database-fixture", strings.Repeat("b", 64)); !errors.Is(err, ErrMigrationInputChanged) {
		t.Fatalf("changed snapshot accepted: %v", err)
	}
	if _, err := j.Apply(ctx, *run, StageAccounts, nil, func(*gorm.DB) error { return nil }); !errors.Is(err, ErrMigrationStageOrder) {
		t.Fatal("out-of-order stage accepted")
	}
	mappings := []Mapping{{Entity: EntitySite, SourceID: 1, TargetID: 7}}
	applied, err := j.Apply(ctx, *run, StageSites, mappings, func(tx *gorm.DB) error {
		if err := tx.Create(&journalTestRecord{ID: 7, Value: "initial"}).Error; err != nil {
			return err
		}
		return errors.New("database failure containing secret-token")
	})
	if applied || !errors.Is(err, ErrMigrationStageFailed) || strings.Contains(err.Error(), "secret-token") {
		t.Fatal("failed stage not sanitized")
	}
	var count int64
	if err := conn.Model(&journalTestRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("business writes did not roll back")
	}
	rows, err := j.Mappings(ctx, *run)
	if err != nil || len(rows) != 0 {
		t.Fatal("source mappings did not roll back")
	}
	var stage MigrationStage
	if err := conn.First(&stage, "run_id = ? AND name = ?", run.ID, StageSites).Error; err != nil || stage.State != "failed" || stage.ErrorCode != "stage_failed" {
		t.Fatalf("missing failed stage: %v", err)
	}
	// Reopen through a new journal instance, as after a process restart.
	j = NewJournal(conn)
	run, err = j.Open(ctx, "database-fixture", strings.Repeat("a", 64))
	if err != nil || run.NextStage != 0 {
		t.Fatal("failed stage advanced progress")
	}
	applied, err = j.Apply(ctx, *run, StageSites, mappings, func(tx *gorm.DB) error { return tx.Create(&journalTestRecord{ID: 7, Value: "initial"}).Error })
	if err != nil || !applied {
		t.Fatalf("retry failed: %v", err)
	}
	if err := conn.Model(&journalTestRecord{}).Where("id = ?", 7).Update("value", "manually edited").Error; err != nil {
		t.Fatal(err)
	}
	applied, err = j.Apply(ctx, *run, StageSites, mappings, func(*gorm.DB) error { t.Fatal("completed stage reran"); return nil })
	if applied || err != nil {
		t.Fatal("completed stage not idempotent")
	}
	for _, changed := range [][]Mapping{{{EntitySite, 1, 8}}, {{EntitySite, 2, 7}}} {
		if _, err := j.Apply(ctx, *run, StageSites, changed, func(*gorm.DB) error { t.Fatal("completed stage reran"); return nil }); !errors.Is(err, ErrMigrationMappingConflict) {
			t.Fatalf("completed stage accepted altered mappings: %v", err)
		}
	}
	if err := conn.First(&stage, "run_id = ? AND name = ?", run.ID, StageSites).Error; err != nil || stage.State != "completed" || stage.Attempts != 2 || stage.ErrorCode != "" {
		t.Fatalf("stage retry receipt incorrect: %v", err)
	}
	var record journalTestRecord
	if err := conn.First(&record, 7).Error; err != nil || record.Value != "manually edited" {
		t.Fatal("manual edit overwritten")
	}
	for _, name := range migrationStages[1:] {
		if _, err := j.Apply(ctx, *run, name, nil, func(*gorm.DB) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	current, err := j.Open(ctx, "database-fixture", strings.Repeat("a", 64))
	if err != nil || !current.Complete() {
		t.Fatal("migration completion not persisted")
	}
	rows, err = j.Mappings(ctx, *run)
	if err != nil || len(rows) != 1 || rows[0] != mappings[0] {
		t.Fatal("persistent mapping changed")
	}
}

func TestMigrationJournalConcurrentStageRunsOnce(t *testing.T) {
	j, conn := journalDatabase(t)
	testJournalConcurrentStage(t, j, conn)
}

func testJournalConcurrentStage(t *testing.T, j *Journal, conn *gorm.DB) {
	t.Helper()
	run, err := j.Open(context.Background(), "concurrent", strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var group sync.WaitGroup
	errorsCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := j.Apply(context.Background(), *run, StageSites, []Mapping{{EntitySite, 1, 1}}, func(tx *gorm.DB) error {
				calls.Add(1)
				return tx.Create(&journalTestRecord{ID: 1, Value: "once"}).Error
			})
			errorsCh <- err
		}()
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("stage called %d times", calls.Load())
	}
	var count int64
	if err := conn.Model(&journalTestRecord{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("duplicate records")
	}
}

func TestMigrationJournalConflictingMappingsAndCancellationRollBack(t *testing.T) {
	j, conn := journalDatabase(t)
	testJournalMappingConflictAndCancellation(t, j, conn)
}

func testJournalMappingConflictAndCancellation(t *testing.T, j *Journal, conn *gorm.DB) {
	t.Helper()
	run, err := j.Open(context.Background(), "conflict", strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = j.Apply(context.Background(), *run, StageSites, []Mapping{{EntitySite, 1, 2}, {EntitySite, 1, 3}}, func(*gorm.DB) error { called = true; return nil })
	if !errors.Is(err, ErrMigrationMappingConflict) || called {
		t.Fatal("conflicting source mapping allowed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err = j.Apply(ctx, *run, StageSites, []Mapping{{EntitySite, 1, 2}}, func(tx *gorm.DB) error {
		if err := tx.Create(&journalTestRecord{ID: 2}).Error; err != nil {
			return err
		}
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, ErrMigrationStageFailed) {
		t.Fatalf("canceled stage succeeded: %v", err)
	}
	var count int64
	if err := conn.Model(&journalTestRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("canceled data committed")
	}
	rows, err := j.Mappings(context.Background(), *run)
	if err != nil || len(rows) != 0 {
		t.Fatal("canceled mappings committed")
	}
	current, err := j.Open(context.Background(), "conflict", strings.Repeat("d", 64))
	if err != nil || current.NextStage != 0 {
		t.Fatal("canceled stage advanced")
	}
}
