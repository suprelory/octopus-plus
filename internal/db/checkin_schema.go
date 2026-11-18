package db

import (
	"errors"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

// PrepareCheckinSchema is an explicit migration building block. Runtime startup
// will call it at cutover, after preflight and backup support have been completed.
// It does not create or read subscription tables or copy legacy credentials.
func PrepareCheckinSchema(conn *gorm.DB) error {
	if conn == nil {
		return errors.New("checkin schema requires a database")
	}
	for _, table := range []any{&model.CheckinSite{}, &model.CheckinCredential{}, &model.CheckinAccount{}, &model.CheckinLog{}} {
		if !conn.Migrator().HasTable(table) {
			if err := conn.Migrator().CreateTable(table); err != nil {
				return err
			}
		}
	}
	// Upgrade the explicit P2 development schema without touching legacy tables.
	for _, field := range []string{"TokenRefreshStarted", "TokenRefreshRetryAt", "SessionCookies"} {
		if !conn.Migrator().HasColumn(&model.CheckinCredential{}, field) {
			if err := conn.Migrator().AddColumn(&model.CheckinCredential{}, field); err != nil {
				return err
			}
		}
	}
	if !conn.Migrator().HasColumn(&model.CheckinAccount{}, "CheckinWorkflowVersion") {
		if err := conn.Migrator().AddColumn(&model.CheckinAccount{}, "CheckinWorkflowVersion"); err != nil {
			return err
		}
	}
	return nil
}
