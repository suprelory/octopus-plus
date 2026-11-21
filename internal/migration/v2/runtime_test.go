package v2

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/suprelory/octopus-plus/internal/db"
	"gorm.io/gorm"
)

func TestRuntimeMigrationGate(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var conn *gorm.DB
			if dialect == "sqlite" {
				conn = sqliteMigrationDatabase(t)
			} else {
				dsn := os.Getenv("CHECKIN_TEST_" + strings.ToUpper(dialect) + "_DSN")
				if dsn == "" {
					t.Skip("integration DSN not configured")
				}
				conn = openExternalDatabase(t, dialect, dsn)
			}
			seedMigrationDatabase(t, conn)
			if err := db.PrepareRuntimeSchema(conn); !errors.Is(err, db.ErrRuntimeMigrationRequired) {
				t.Fatalf("old database gate: %v", err)
			}
			input, err := CaptureDatabaseInput(t.Context(), conn)
			if err != nil {
				t.Fatal(err)
			}
			if err := prepareMigrationSchema(conn); err != nil {
				t.Fatal(err)
			}
			conversion, err := Convert(input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewJournal(conn).Open(t.Context(), DatabaseSource, conversion.Fingerprint); err != nil {
				t.Fatal(err)
			}
			if err := db.PrepareRuntimeSchema(conn); !errors.Is(err, db.ErrRuntimeMigrationRequired) {
				t.Fatalf("partial migration gate: %v", err)
			}
			if _, err := MigrateDatabase(t.Context(), conn, DatabaseSource, input); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for range 2 {
				wg.Go(func() {
					if err := db.PrepareRuntimeSchema(conn); err != nil {
						t.Errorf("concurrent cutover: %v", err)
					}
				})
			}
			wg.Wait()
			for _, table := range []string{"site_channel_bindings", "site_user_groups", "site_accounts", "sites"} {
				if err := conn.Migrator().DropTable(table); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.PrepareRuntimeSchema(conn); err != nil {
				t.Fatalf("restart without legacy tables: %v", err)
			}
			if err := conn.Table("v2_migration_runs").Where("source = ?", DatabaseSource).Update("next_stage", 5).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.PrepareRuntimeSchema(conn); !errors.Is(err, db.ErrRuntimeMigrationRequired) {
				t.Fatalf("installed marker hid incomplete migration: %v", err)
			}
		})
	}
}

func TestRuntimeConcurrentFreshStartup(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var conn *gorm.DB
			if dialect == "sqlite" {
				conn = sqliteMigrationDatabase(t)
			} else {
				dsn := os.Getenv("CHECKIN_TEST_" + strings.ToUpper(dialect) + "_DSN")
				if dsn == "" {
					t.Skip("integration DSN not configured")
				}
				conn = openExternalDatabase(t, dialect, dsn)
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() {
					<-start
					if err := db.PrepareRuntimeSchema(conn); err != nil {
						t.Errorf("concurrent fresh startup: %v", err)
					}
				})
			}
			close(start)
			wg.Wait()
			if err := db.PrepareRuntimeSchema(conn); err != nil {
				t.Fatalf("restart: %v", err)
			}
			var markers int64
			if err := conn.Table("application_schema").Where("version = 2").Count(&markers).Error; err != nil || markers != 1 {
				t.Fatalf("installation marker: %d, %v", markers, err)
			}
			for _, name := range []string{"sites", "site_accounts", "site_tokens", "site_user_groups", "site_models", "site_channel_bindings"} {
				if conn.Migrator().HasTable(name) {
					t.Fatalf("created retired table %s", name)
				}
			}
		})
	}
}
