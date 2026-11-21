package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var legacyRuntime atomic.Bool

// LegacyRuntime is enabled only by the explicit legacy fixture initializer.
// Production startup always uses InitDB and never selects the legacy runtime.
// The compatibility branches can be removed with the old modules in P6.
func LegacyRuntime() bool { return legacyRuntime.Load() }

var ErrRuntimeMigrationRequired = errors.New("database requires a completed v2 migration; stop old writers, back up the database, then run migrate v2-database before starting")
var ErrRuntimeSchemaIncomplete = errors.New("v2 database schema is incomplete or unsupported; restore a verified backup before starting")

type runtimeSchema struct {
	ID      int `gorm:"primaryKey;autoIncrement:false"`
	Version int `gorm:"not null"`
}

func (runtimeSchema) TableName() string { return "application_schema" }

// InitDB checks the database before any application schema changes, then
// prepares only ordinary relay tables and the independent check-in model.
func InitDB(dbType, dsn string, debug bool) error {
	conn, err := openConnection(dbType, dsn, debug)
	if err != nil {
		return err
	}
	if err := PrepareRuntimeSchema(conn); err != nil {
		if pool, poolErr := conn.DB(); poolErr == nil {
			_ = pool.Close()
		}
		return err
	}
	db = conn
	legacyRuntime.Store(false)
	return nil
}

// PrepareRuntimeSchema also supports injected databases for startup integration
// tests. The readiness check performs no data writes or compatibility migrations.
func PrepareRuntimeSchema(conn *gorm.DB) error {
	if conn == nil {
		return errors.New("runtime schema requires a database")
	}
	if conn.Dialector.Name() == "sqlite" {
		// SQLite needs a schema write to take its first writer lock. Reject old
		// databases before that write; transactional DDL hides concurrent setup.
		if err := checkRuntimeReady(conn); err != nil {
			return err
		}
	}
	prepare := func(tx *gorm.DB) error {
		if err := checkRuntimeReady(tx); err != nil {
			return err
		}
		for _, table := range []any{
			&model.User{}, &model.Channel{}, &model.ChannelKey{}, &model.ProxyConfiguration{},
			&model.Group{}, &model.GroupItem{}, &model.GroupPreset{}, &model.LLMInfo{}, &model.APIKey{}, &model.Setting{},
			&model.StatsTotal{}, &model.StatsDaily{}, &model.StatsHourly{}, &model.StatsChannel{}, &model.StatsAPIKey{},
			&model.WSResponseAffinity{}, &model.RelayLog{}, &model.RelayRequestContent{}, &model.RelayAttemptContent{},
			&runtimeSchema{},
		} {
			if !tx.Migrator().HasTable(table) {
				if err := tx.Migrator().CreateTable(table); err != nil {
					return err
				}
			}
		}
		if err := ensureStatsHourlyHourKey(tx); err != nil {
			return err
		}
		if err := PrepareCheckinSchema(tx); err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&runtimeSchema{ID: 1, Version: 2}).Error
	}
	if conn.Dialector.Name() == "mysql" {
		return conn.Connection(func(pinnedDB *gorm.DB) (err error) {
			pinned := pinnedDB.Statement.ConnPool.(*sql.Conn)
			const lock = "CONCAT('octopus-v2-runtime-', MD5(DATABASE()))"
			ctx := pinnedDB.Statement.Context
			var acquired int
			if err := pinned.QueryRowContext(ctx, "SELECT GET_LOCK("+lock+", 30)").Scan(&acquired); err != nil || acquired != 1 {
				return errors.New("cannot acquire runtime schema lock")
			}
			defer func() {
				release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				var released int
				if releaseErr := pinned.QueryRowContext(release, "SELECT RELEASE_LOCK("+lock+")").Scan(&released); releaseErr != nil || released != 1 {
					_ = pinned.Raw(func(any) error { return driver.ErrBadConn })
					if err == nil {
						err = errors.New("cannot release runtime schema lock")
					}
				}
			}()
			return prepare(pinnedDB.Session(&gorm.Session{NewDB: true}))
		})
	}
	return conn.Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(current_database() || '-octopus-v2-runtime'))").Error; err != nil {
				return err
			}
		} else {
			// Acquires the SQLite writer lock before schema reads, including the
			// first startup. The initial read-only readiness check already passed.
			if err := tx.Exec("CREATE TABLE IF NOT EXISTS application_schema (id INTEGER PRIMARY KEY, version INTEGER NOT NULL)").Error; err != nil {
				return err
			}
			if err := tx.Exec("UPDATE application_schema SET id = id WHERE 0").Error; err != nil {
				return err
			}
		}
		return prepare(tx)
	})
}

func checkRuntimeReady(conn *gorm.DB) error {
	tables, err := conn.Migrator().GetTables()
	if err != nil {
		return fmt.Errorf("inspect runtime schema: %w", err)
	}
	present := make(map[string]bool, len(tables))
	for _, name := range tables {
		present[name] = true
	}
	migrated := false
	if present["v2_migration_runs"] {
		var runs []struct {
			ID                 int64
			Version, NextStage int
		}
		if err := conn.Table("v2_migration_runs").Select("id", "version", "next_stage").Where("source = ?", "legacy-database").Find(&runs).Error; err != nil {
			return ErrRuntimeMigrationRequired
		}
		for _, run := range runs {
			// This is the persisted P5.2 v2 journal contract, including the final
			// verification transaction. Merely creating new tables is insufficient.
			stages := []string{"sites", "credentials", "accounts", "channels", "history", "verify"}
			var count int64
			if run.Version != 2 || run.NextStage != len(stages) || !present["v2_migration_stages"] {
				return ErrRuntimeMigrationRequired
			}
			if err := conn.Table("v2_migration_stages").Where("run_id = ? AND name IN ? AND state = ?", run.ID, stages, "completed").Count(&count).Error; err != nil || count != int64(len(stages)) {
				return ErrRuntimeMigrationRequired
			}
			migrated = true
		}
	}
	installed := false
	if present["application_schema"] {
		var marker runtimeSchema
		err := conn.First(&marker, 1).Error
		if err == nil {
			if marker.Version != 2 {
				return ErrRuntimeSchemaIncomplete
			}
			installed = true
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrRuntimeSchemaIncomplete
		}
	}
	if !installed {
		for _, name := range []string{"sites", "site_accounts", "site_tokens", "site_user_groups", "site_models", "site_channel_bindings"} {
			if present[name] && !migrated {
				return ErrRuntimeMigrationRequired
			}
		}
		if migrated && present["site_channel_bindings"] {
			var count int64
			if err := conn.Table("site_channel_bindings").Count(&count).Error; err != nil || count != 0 {
				return ErrRuntimeMigrationRequired
			}
		}
	}
	identityTables := []string{"checkin_sites", "checkin_accounts", "checkin_credentials"}
	count := 0
	for _, name := range identityTables {
		if present[name] {
			count++
		}
	}
	if (installed || migrated || count > 0) && count != len(identityTables) {
		return ErrRuntimeSchemaIncomplete
	}
	if installed || migrated {
		// Missing history or delivery state must not become empty tables: doing
		// so could discard submission evidence or allow duplicate notifications.
		for _, name := range []string{"site_checkin_logs", "site_checkin_batch_jobs", "checkin_batch_leases", "checkin_completions", "checkin_notification_deliveries", "checkin_notification_cooldowns", "checkin_notification_gates"} {
			if !present[name] {
				return ErrRuntimeSchemaIncomplete
			}
		}
	}
	return nil
}
