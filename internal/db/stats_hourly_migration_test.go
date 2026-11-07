package db

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/migrator"
)

func TestStatsHourlyInsertsZeroByDialect(t *testing.T) {
	for _, dialector := range []gorm.Dialector{
		sqlite.Open(":memory:"),
		mysql.New(mysql.Config{DSN: "user:pass@tcp(localhost:3306)/octopus", SkipInitializeWithVersion: true}),
		postgres.New(postgres.Config{DSN: "host=localhost user=postgres dbname=octopus sslmode=disable", PreferSimpleProtocol: true}),
	} {
		t.Run(dialector.Name(), func(t *testing.T) {
			conn, err := gorm.Open(dialector, &gorm.Config{
				DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := conn.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sqlDB.Close() })
			rows := []model.StatsHourly{{Hour: 0, Date: "20261029", StatsMetrics: model.StatsMetrics{InputCost: 100}}}
			result := conn.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "hour"}}, UpdateAll: true,
			}).Create(&rows)
			if result.Error != nil {
				t.Fatal(result.Error)
			}
			values, ok := result.Statement.Clauses["VALUES"].Expression.(clause.Values)
			if !ok {
				t.Fatalf("missing insert values: %s", result.Statement.SQL.String())
			}
			hourColumn := -1
			for i, column := range values.Columns {
				if column.Name == "hour" {
					hourColumn = i
					break
				}
			}
			if hourColumn < 0 || len(values.Values) != 1 || values.Values[0][hourColumn] != 0 {
				t.Fatalf("midnight must be explicitly inserted as zero: %s, values=%+v", result.Statement.SQL.String(), values)
			}
			hour := result.Statement.Schema.LookUpField("Hour")
			dataType := strings.ToLower(conn.Migrator().FullDataTypeOf(hour).SQL)
			for _, generated := range []string{"auto_increment", "autoincrement", "serial", "identity"} {
				if strings.Contains(dataType, generated) {
					t.Fatalf("hour key must not generate IDs: %s", dataType)
				}
			}
		})
	}
}

// Supply legacy column metadata while retaining the real MySQL ALTER COLUMN
// generator. No MySQL server is needed to check the migration's SQL.
type hourlyStatsMetadataDialector struct {
	gorm.Dialector
	columns []gorm.ColumnType
	err     error
}

func (d *hourlyStatsMetadataDialector) Migrator(conn *gorm.DB) gorm.Migrator {
	return hourlyStatsMetadataMigrator{Migrator: d.Dialector.Migrator(conn), columns: d.columns, err: d.err}
}

type hourlyStatsMetadataMigrator struct {
	gorm.Migrator
	columns []gorm.ColumnType
	err     error
}

func (m hourlyStatsMetadataMigrator) ColumnTypes(interface{}) ([]gorm.ColumnType, error) {
	return m.columns, m.err
}

func TestStatsHourlyMySQLMigrationRemovesOnlyLegacyAutoIncrement(t *testing.T) {
	capture := &sqlCaptureLogger{}
	hour := migrator.ColumnType{
		NameValue:          sql.NullString{String: "hour", Valid: true},
		AutoIncrementValue: sql.NullBool{Bool: true, Valid: true},
	}
	dialector := &hourlyStatsMetadataDialector{
		Dialector: mysql.New(mysql.Config{DSN: "user:pass@tcp(localhost:3306)/octopus", SkipInitializeWithVersion: true}),
		columns:   []gorm.ColumnType{hour},
	}
	conn, err := gorm.Open(dialector, &gorm.Config{
		DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: capture,
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := ensureStatsHourlyHourKey(conn); err != nil {
		t.Fatal(err)
	}
	statements := capture.snapshot()
	if len(statements) != 1 || !strings.HasPrefix(statements[0], "ALTER TABLE `stats_hourlies` MODIFY COLUMN `hour` ") ||
		strings.Contains(strings.ToUpper(statements[0]), "AUTO_INCREMENT") {
		t.Fatalf("expected one targeted ALTER without AUTO_INCREMENT, got %v", statements)
	}

	// The corrected schema must not be altered again on subsequent starts.
	hour.AutoIncrementValue = sql.NullBool{Bool: false, Valid: true}
	dialector.columns = []gorm.ColumnType{hour}
	capture.reset()
	if err := ensureStatsHourlyHourKey(conn); err != nil {
		t.Fatal(err)
	}
	if statements := capture.snapshot(); len(statements) != 0 {
		t.Fatalf("corrected schema was modified again: %v", statements)
	}

	injected := errors.New("column metadata unavailable")
	dialector.err = injected
	if err := ensureStatsHourlyHourKey(conn); !errors.Is(err, injected) {
		t.Fatalf("column inspection failure was lost: %v", err)
	}
	if statements := capture.snapshot(); len(statements) != 0 {
		t.Fatalf("failed column inspection caused a schema change: %v", statements)
	}
}
