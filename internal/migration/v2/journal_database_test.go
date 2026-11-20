package v2

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	mysqlconfig "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DSNs point to disposable test servers with CREATE DATABASE permission. Each
// contract test creates and drops only its own randomly named database.
func externalJournalDatabase(t *testing.T, dialect, dsn string) (*Journal, *gorm.DB) {
	t.Helper()
	open := func(database string) (*gorm.DB, error) {
		config := &gorm.Config{Logger: logger.Discard}
		if dialect == "mysql" {
			parsed, err := mysqlconfig.ParseDSN(dsn)
			if err != nil {
				return nil, errors.New("invalid MySQL integration DSN")
			}
			if database != "" {
				parsed.DBName = database
			}
			parsed.ParseTime = true
			return gorm.Open(mysql.Open(parsed.FormatDSN()), config)
		}
		parsed, err := pgx.ParseConfig(dsn)
		if err != nil {
			return nil, errors.New("invalid PostgreSQL integration DSN")
		}
		if database != "" {
			parsed.Database = database
		}
		pool := stdlib.OpenDB(*parsed)
		conn, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), config)
		if err != nil {
			_ = pool.Close()
		}
		return conn, err
	}
	admin, err := open("")
	if err != nil {
		t.Fatalf("cannot connect to %s integration server", dialect)
	}
	adminPool, err := admin.DB()
	if err != nil {
		t.Fatal("cannot access integration connection")
	}
	t.Cleanup(func() { _ = adminPool.Close() })
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	name := "migration_it_" + hex.EncodeToString(random[:])
	quoted := "`" + name + "`"
	if dialect == "postgres" {
		quoted = `"` + name + `"`
	}
	if err := admin.Exec("CREATE DATABASE " + quoted).Error; err != nil {
		t.Fatalf("cannot create isolated %s integration database", dialect)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE " + quoted).Error; err != nil {
			t.Errorf("cannot remove generated integration database %s", name)
		}
	})
	conn, err := open(name)
	if err != nil {
		t.Fatalf("cannot open isolated %s integration database", dialect)
	}
	pool, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = pool.Close() })
	return prepareJournalDatabase(t, conn), conn
}

func TestMigrationJournalDatabases(t *testing.T) {
	for _, database := range []struct{ dialect, env string }{{"mysql", "CHECKIN_TEST_MYSQL_DSN"}, {"postgres", "CHECKIN_TEST_POSTGRES_DSN"}} {
		t.Run(database.dialect, func(t *testing.T) {
			dsn := os.Getenv(database.env)
			if dsn == "" {
				t.Skip("set " + database.env + " to run database integration tests")
			}
			for _, tc := range []struct {
				name string
				run  func(*testing.T, *Journal, *gorm.DB)
			}{
				{"rollback and resume", testJournalAtomicRollbackResume},
				{"concurrent attempts", testJournalConcurrentStage},
				{"mapping conflict and cancellation", testJournalMappingConflictAndCancellation},
			} {
				t.Run(tc.name, func(t *testing.T) {
					j, conn := externalJournalDatabase(t, database.dialect, dsn)
					tc.run(t, j, conn)
				})
			}
		})
	}
}
