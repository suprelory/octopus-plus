package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"testing"

	mysqlconfig "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Integration DSNs identify disposable test servers. Only randomly named
// databases created by this test are written or dropped.
func externalBackupDatabase(t *testing.T, dialect, dsn string) *Service {
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
			return gorm.Open(mysql.New(mysql.Config{DSN: parsed.FormatDSN(), DefaultStringSize: 191}), config)
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
	pool, err := admin.DB()
	if err != nil {
		t.Fatal("cannot access integration connection")
	}
	t.Cleanup(func() { _ = pool.Close() })
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	name := "backup_it_" + hex.EncodeToString(random[:])
	quoted := "`" + name + "`"
	if dialect == "postgres" {
		quoted = `"` + name + `"`
	}
	if err := admin.Exec("CREATE DATABASE " + quoted).Error; err != nil {
		t.Fatal("cannot create integration database")
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE " + quoted).Error; err != nil {
			t.Errorf("cannot remove generated database %s", name)
		}
	})
	conn, err := open(name)
	if err != nil {
		t.Fatal("cannot open integration database")
	}
	dataPool, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	dataPool.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = dataPool.Close() })
	prepareOrdinaryTables(t, conn)
	return NewService(conn)
}

func TestBackupDatabases(t *testing.T) {
	for _, database := range []struct{ dialect, env string }{{"mysql", "CHECKIN_TEST_MYSQL_DSN"}, {"postgres", "CHECKIN_TEST_POSTGRES_DSN"}} {
		t.Run(database.dialect, func(t *testing.T) {
			dsn := os.Getenv(database.env)
			if dsn == "" {
				t.Skip("set " + database.env + " to run database integration tests")
			}
			source := externalBackupDatabase(t, database.dialect, dsn)
			d := backupFixture()
			var group sync.WaitGroup
			results := make(chan bool, 2)
			failures := make(chan error, 2)
			for range 2 {
				group.Add(1)
				go func() {
					defer group.Done()
					result, err := source.Import(context.Background(), d)
					failures <- err
					results <- result != nil && result.AlreadyImported
				}()
			}
			group.Wait()
			close(failures)
			close(results)
			for err := range failures {
				if err != nil {
					t.Fatal(err)
				}
			}
			repeats := 0
			for repeated := range results {
				if repeated {
					repeats++
				}
			}
			if repeats != 1 {
				t.Fatal("concurrent first import wrote twice")
			}
			var archive bytes.Buffer
			if err := source.ExportZIP(context.Background(), &archive, true, true); err != nil {
				t.Fatal(err)
			}
			restored, err := Read(context.Background(), &archive)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			target := externalBackupDatabase(t, database.dialect, dsn)
			requireImport(t, target, restored)
			assertRestored(t, target, true)
		})
	}
}
