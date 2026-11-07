package db

import (
	"fmt"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var db *gorm.DB

func InitDB(dbType, dsn string, debug bool) error {
	var err error
	gormConfig := gorm.Config{Logger: logger.Discard}
	if debug {
		gormConfig.Logger = logger.Default.LogMode(logger.Info)
	}

	switch dbType {
	case "sqlite":
		db, err = initSQLite(dsn, &gormConfig)
	case "mysql":
		db, err = initMySQL(dsn, &gormConfig)
	case "postgres", "postgresql":
		db, err = initPostgres(dsn, &gormConfig)
	default:
		return fmt.Errorf("unsupported database type: %s", dbType)
	}

	if err != nil {
		return err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}

	switch dbType {
	case "sqlite":
		// SQLite 单写模型：限制为单连接，避免连接池内自相竞争 SQLITE_BUSY；
		// 读操作也共享该连接；事务内的所有查询必须复用事务连接。
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
		sqlDB.SetConnMaxLifetime(0)
		sqlDB.SetConnMaxIdleTime(0)
	default:
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetMaxOpenConns(100)
		sqlDB.SetConnMaxLifetime(time.Hour)
		sqlDB.SetConnMaxIdleTime(10 * time.Minute)
	}

	return initializeSchema(db)
}

// Create missing tables and apply only targeted compatibility migrations to
// existing schemas.
func initializeSchema(db *gorm.DB) error {
	models := []interface{}{
		&model.User{},
		&model.Channel{},
		&model.ChannelKey{},
		&model.ProxyConfiguration{},
		&model.Site{},
		&model.SiteAccount{},
		&model.SiteCheckinLog{},
		&model.SiteCheckinBatchJob{},
		&model.SiteToken{},
		&model.SiteUserGroup{},
		&model.SiteModel{},
		&model.SiteChannelBinding{},
		&model.Group{},
		&model.GroupItem{},
		&model.GroupPreset{},
		&model.LLMInfo{},
		&model.APIKey{},
		&model.Setting{},
		&model.StatsTotal{},
		&model.StatsDaily{},
		&model.StatsHourly{},
		&model.StatsChannel{},
		&model.StatsAPIKey{},
		&model.StatsSiteModelHourly{},
		&model.WSResponseAffinity{},
		&model.RelayLog{},
		&model.RelayRequestContent{},
		&model.RelayAttemptContent{},
	}
	for _, table := range models {
		if !db.Migrator().HasTable(table) {
			if err := db.Migrator().CreateTable(table); err != nil {
				return err
			}
		}
	}
	if err := ensureStatsHourlyHourKey(db); err != nil {
		return err
	}
	if err := ensureSiteCheckinHTTPColumns(db); err != nil {
		return err
	}
	if err := ensureSiteCheckinCapabilityColumns(db); err != nil {
		return err
	}
	if err := ensureSiteKindColumns(db); err != nil {
		return err
	}
	if err := ensureSiteCheckinCredentialColumns(db); err != nil {
		return err
	}
	if err := ensureSiteNameScope(db); err != nil {
		return err
	}
	if err := ensureSiteCheckinBatchJobSchema(db); err != nil {
		return err
	}
	return SeparateLegacySiteCheckins(db)
}

func ensureSiteCheckinBatchJobSchema(db *gorm.DB) error {
	if !db.Migrator().HasColumn(&model.SiteCheckinLog{}, "BatchJobID") {
		if err := db.Migrator().AddColumn(&model.SiteCheckinLog{}, "BatchJobID"); err != nil {
			return fmt.Errorf("add site checkin log batch job id: %w", err)
		}
	}
	if !db.Migrator().HasIndex(&model.SiteCheckinLog{}, "idx_site_checkin_batch_job_id") {
		if err := db.Migrator().CreateIndex(&model.SiteCheckinLog{}, "BatchJobID"); err != nil {
			return fmt.Errorf("create site checkin log batch job index: %w", err)
		}
	}

	var interruptedCount int64
	if err := db.Model(&model.SiteCheckinBatchJob{}).
		Where("status IN ?", []model.SiteCheckinBatchJobStatus{model.SiteCheckinBatchJobStatusQueued, model.SiteCheckinBatchJobStatusRunning}).
		Count(&interruptedCount).Error; err != nil {
		return fmt.Errorf("count unfinished site checkin batch jobs: %w", err)
	}
	if interruptedCount == 0 {
		return nil
	}
	now := time.Now().UTC()
	if err := db.Model(&model.SiteCheckinBatchJob{}).
		Where("status IN ?", []model.SiteCheckinBatchJobStatus{model.SiteCheckinBatchJobStatusQueued, model.SiteCheckinBatchJobStatusRunning}).
		Updates(map[string]any{
			"status":        model.SiteCheckinBatchJobStatusInterrupted,
			"error_message": "server restarted before the batch finished",
			"updated_at":    now,
			"finished_at":   now,
		}).Error; err != nil {
		return fmt.Errorf("mark unfinished site checkin batch jobs interrupted: %w", err)
	}
	return nil
}

func ensureSiteCheckinHTTPColumns(db *gorm.DB) error {
	for _, column := range []string{"CheckinHTTPEnabled", "CheckinHTTPMethod", "CheckinHTTPPath", "CheckinHTTPBody", "CheckinHTTPHeaders", "CheckinRewardExtractor"} {
		if db.Migrator().HasColumn(&model.Site{}, column) {
			continue
		}
		if err := db.Migrator().AddColumn(&model.Site{}, column); err != nil {
			return fmt.Errorf("add site column %s: %w", column, err)
		}
	}
	return nil
}

func ensureSiteCheckinCapabilityColumns(db *gorm.DB) error {
	for _, column := range []string{"CheckinMode", "CheckinVerificationStatus", "CheckinVerificationFingerprint", "CheckinVerifiedAt"} {
		if db.Migrator().HasColumn(&model.Site{}, column) {
			continue
		}
		if err := db.Migrator().AddColumn(&model.Site{}, column); err != nil {
			return fmt.Errorf("add site column %s: %w", column, err)
		}
	}
	return nil
}

// Existing sites become subscription (relay) sites through the column default.
func ensureSiteKindColumns(db *gorm.DB) error {
	for _, column := range []string{"Kind", "LinkedSiteID"} {
		if db.Migrator().HasColumn(&model.Site{}, column) {
			continue
		}
		if err := db.Migrator().AddColumn(&model.Site{}, column); err != nil {
			return fmt.Errorf("add site column %s: %w", column, err)
		}
	}
	if !db.Migrator().HasColumn(&model.SiteAccount{}, "CheckinSourceAccountID") {
		if err := db.Migrator().AddColumn(&model.SiteAccount{}, "CheckinSourceAccountID"); err != nil {
			return fmt.Errorf("add check-in account migration reference: %w", err)
		}
	}
	if !db.Migrator().HasIndex(&model.SiteAccount{}, "CheckinSourceAccountID") {
		if err := db.Migrator().CreateIndex(&model.SiteAccount{}, "CheckinSourceAccountID"); err != nil {
			return fmt.Errorf("index check-in account migration reference: %w", err)
		}
	}
	return nil
}

func initSQLite(path string, config *gorm.Config) (*gorm.DB, error) {
	// glebarez/sqlite (modernc.org/sqlite) 只识别 _pragma=NAME(VALUE) 形式参数，
	// 旧的下划线参数会被静默忽略（导致 WAL/busy_timeout 实际未生效）。
	params := []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=foreign_keys(ON)",
		"_pragma=cache_size(-10000)",
		"_pragma=mmap_size(268435456)",
		"_pragma=temp_store(MEMORY)",
	}
	return gorm.Open(sqlite.Open(path+"?"+strings.Join(params, "&")), config)
}

func initMySQL(dsn string, config *gorm.Config) (*gorm.DB, error) {
	// DSN 格式: user:password@tcp(host:port)/dbname?charset=utf8mb4&parseTime=True&loc=Local
	if !strings.Contains(dsn, "?") {
		dsn += "?charset=utf8mb4&parseTime=True&loc=Local"
	}
	return gorm.Open(mysql.Open(dsn), config)
}

func initPostgres(dsn string, config *gorm.Config) (*gorm.DB, error) {
	// DSN 格式: host=localhost user=postgres password=xxx dbname=octopus port=5432 sslmode=disable
	return gorm.Open(postgres.Open(dsn), config)
}

func Close() error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func GetDB() *gorm.DB {
	return db
}
