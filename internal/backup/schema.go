package backup

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"time"

	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type importGate struct {
	ID int `gorm:"primaryKey;autoIncrement:false"`
}

func (importGate) TableName() string { return "v2_backup_gates" }

// PrepareSchema is explicit until production cutover. Ordinary relay tables are
// prepared by the caller. No subscription tables are required or created here.
func (s *Service) PrepareSchema(ctx context.Context) error {
	return s.prepareSchema(ctx, false)
}

func (s *Service) prepareSchema(ctx context.Context, legacyStats bool) error {
	conn := s.conn.WithContext(ctx)
	if err := conn.Exec("CREATE TABLE IF NOT EXISTS v2_backup_gates (id INTEGER PRIMARY KEY)").Error; err != nil {
		return ErrStorage
	}
	if err := conn.Clauses(clause.OnConflict{DoNothing: true}).Create(&importGate{ID: 1}).Error; err != nil {
		return ErrStorage
	}
	prepare := func(tx *gorm.DB) error {
		if err := dbpkg.PrepareCheckinSchema(tx); err != nil {
			return err
		}
		for _, table := range []any{&importRun{}, &importMapping{}, &ArchivedSiteStat{}} {
			if !tx.Migrator().HasTable(table) {
				if err := tx.Migrator().CreateTable(table); err != nil {
					return err
				}
			}
		}
		if legacyStats && !tx.Migrator().HasTable(&model.StatsModel{}) {
			return tx.Migrator().CreateTable(&model.StatsModel{})
		}
		return nil
	}
	var err error
	if conn.Dialector.Name() == "mysql" {
		err = conn.Connection(func(db *gorm.DB) (err error) {
			pinned := db.Statement.ConnPool.(*sql.Conn)
			const name = "CONCAT('octopus-v2-backup-', MD5(DATABASE()))"
			var acquired int
			if err := pinned.QueryRowContext(ctx, "SELECT GET_LOCK("+name+", 30)").Scan(&acquired); err != nil || acquired != 1 {
				return ErrStorage
			}
			defer func() {
				releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				var released int
				if releaseErr := pinned.QueryRowContext(releaseCtx, "SELECT RELEASE_LOCK("+name+")").Scan(&released); releaseErr != nil || released != 1 {
					_ = pinned.Raw(func(any) error { return driver.ErrBadConn })
					if err == nil {
						err = ErrStorage
					}
				}
			}()
			return prepare(db.Session(&gorm.Session{NewDB: true}))
		})
	} else {
		err = conn.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&importGate{}).Where("id = 1").Update("id", gorm.Expr("id")).Error; err != nil {
				return err
			}
			return prepare(tx)
		})
	}
	if err != nil {
		return ErrStorage
	}
	return nil
}
