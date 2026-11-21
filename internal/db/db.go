package db

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var db *gorm.DB

// Preview opens an existing SQLite file read-only without changing its journal
// mode. Server databases use read-only transactions for snapshot inspection.
func OpenMigrationConnection(dbType, dsn string, preview bool) (*gorm.DB, error) {
	if dbType != "sqlite" {
		return openConnection(dbType, dsn, false)
	}
	path, err := filepath.Abs(dsn)
	if err != nil {
		return nil, err
	}
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := url.URL{Scheme: "file", Path: path}
	params := u.Query()
	mode := "rw"
	if preview {
		mode = "ro"
	}
	params.Set("mode", mode)
	params.Add("_pragma", "busy_timeout(5000)")
	params.Add("_pragma", "foreign_keys(ON)")
	u.RawQuery = params.Encode()
	conn, err := gorm.Open(sqlite.Open(u.String()), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, err
	}
	pool, err := conn.DB()
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(1)
	return conn, nil
}

func openConnection(dbType, dsn string, debug bool) (*gorm.DB, error) {
	var db *gorm.DB
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
		return nil, fmt.Errorf("unsupported database type: %s", dbType)
	}

	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
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

	return db, nil
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
