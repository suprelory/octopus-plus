package db

import (
	"fmt"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func ensureSiteAccountSessionColumns(conn *gorm.DB) error {
	for _, field := range []string{"CredentialVersion", "TokenRefreshLease", "TokenRefreshLeaseUntil"} {
		if !conn.Migrator().HasColumn(&model.SiteAccount{}, field) {
			if err := conn.Migrator().AddColumn(&model.SiteAccount{}, field); err != nil {
				return fmt.Errorf("add site account session column %s: %w", field, err)
			}
		}
	}
	return nil
}
