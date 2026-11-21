package db

import (
	"errors"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PrepareCheckinSchema is used by the offline migrator and by runtime startup
// after its readiness check.
// It does not create or read subscription tables or copy legacy credentials.
func PrepareCheckinSchema(conn *gorm.DB) error {
	if conn == nil {
		return errors.New("checkin schema requires a database")
	}
	for _, table := range []any{&model.CheckinSite{}, &model.CheckinCredential{}, &model.CheckinAccount{}, &model.CheckinLog{}, &model.CheckinBatchJob{}, &model.CheckinBatchLease{}, &model.CheckinCompletion{}, &model.CheckinNotificationDelivery{}, &model.CheckinNotificationCooldown{}, &model.CheckinNotificationGate{}} {
		if !conn.Migrator().HasTable(table) {
			if err := conn.Migrator().CreateTable(table); err != nil {
				return err
			}
		}
	}
	// Upgrade the explicit P2 development schema without touching legacy tables.
	for _, field := range []string{"TokenRefreshStarted", "TokenRefreshRetryAt", "SessionCookies", "BalanceObservation"} {
		if !conn.Migrator().HasColumn(&model.CheckinCredential{}, field) {
			if err := conn.Migrator().AddColumn(&model.CheckinCredential{}, field); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"CheckinWorkflowVersion", "BalanceUpdatedAt", "BalanceVersion", "BalanceConfigVersion"} {
		if !conn.Migrator().HasColumn(&model.CheckinAccount{}, field) {
			if err := conn.Migrator().AddColumn(&model.CheckinAccount{}, field); err != nil {
				return err
			}
		}
	}
	if err := conn.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.CheckinNotificationGate{ID: 1}).Error; err != nil {
		return err
	}
	return conn.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.CheckinBatchLease{ID: 1}).Error
}
