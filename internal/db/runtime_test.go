package db

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func runtimeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "runtime.db")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := conn.DB()
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	return conn
}

func TestRuntimeSchemaFreshRestartAndMissingIdentity(t *testing.T) {
	conn := runtimeTestDB(t)
	for range 2 {
		if err := PrepareRuntimeSchema(conn); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"sites", "site_accounts", "site_tokens", "site_user_groups", "site_models", "site_channel_bindings", "stats_site_model_hourlies"} {
		if conn.Migrator().HasTable(name) {
			t.Fatalf("created retired table %s", name)
		}
	}
	job := model.CheckinBatchJob{ID: 7, Status: model.SiteCheckinBatchJobStatusRunning}
	if err := conn.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := PrepareRuntimeSchema(conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.First(&job, job.ID).Error; err != nil || job.Status != model.SiteCheckinBatchJobStatusRunning {
		t.Fatal("schema preparation interrupted an active batch")
	}
	if err := conn.Migrator().DropTable(&model.CheckinCredential{}); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRuntimeSchema(conn); !errors.Is(err, ErrRuntimeSchemaIncomplete) {
		t.Fatalf("accepted missing credentials: %v", err)
	}
}

func TestRuntimeSchemaRejectsUnmigratedDatabaseWithoutWrites(t *testing.T) {
	conn := runtimeTestDB(t)
	if err := initializeSchema(conn); err != nil {
		t.Fatal(err)
	}
	job := model.CheckinBatchJob{ID: 9, Status: model.SiteCheckinBatchJobStatusRunning}
	if err := conn.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	before, _ := conn.Migrator().GetTables()
	if err := PrepareRuntimeSchema(conn); !errors.Is(err, ErrRuntimeMigrationRequired) {
		t.Fatalf("accepted old database: %v", err)
	}
	after, _ := conn.Migrator().GetTables()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected startup changed schema")
	}
	if err := conn.First(&job, job.ID).Error; err != nil || job.Status != model.SiteCheckinBatchJobStatusRunning {
		t.Fatal("rejected startup changed a legacy batch")
	}
}

func TestRuntimeSchemaRejectsMissingDurableState(t *testing.T) {
	for _, name := range []string{"checkin_sites", "checkin_accounts", "site_checkin_logs", "site_checkin_batch_jobs", "checkin_batch_leases", "checkin_completions", "checkin_notification_deliveries", "checkin_notification_cooldowns", "checkin_notification_gates"} {
		t.Run(name, func(t *testing.T) {
			conn := runtimeTestDB(t)
			if err := PrepareRuntimeSchema(conn); err != nil {
				t.Fatal(err)
			}
			if err := conn.Migrator().DropTable(name); err != nil {
				t.Fatal(err)
			}
			if err := PrepareRuntimeSchema(conn); !errors.Is(err, ErrRuntimeSchemaIncomplete) {
				t.Fatalf("accepted lost state in %s: %v", name, err)
			}
			if conn.Migrator().HasTable(name) {
				t.Fatal("recreated missing state as an empty table")
			}
		})
	}
}
