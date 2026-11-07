package db

import (
	"errors"
	"fmt"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

// Subscription and check-in sites can share a name, while names within each
// kind remain unique, including archived sites.
func ensureSiteNameScope(database *gorm.DB) error {
	const indexName = "idx_sites_kind_name"
	legacyConstraint := database.NamingStrategy.UniqueName("sites", "name")
	hasLegacy := database.Migrator().HasConstraint(&model.Site{}, legacyConstraint)
	hasIndex := database.Migrator().HasIndex(&model.Site{}, indexName)
	if !hasLegacy && hasIndex {
		return nil
	}
	migrate := func(tx *gorm.DB) error {
		// Install the narrower constraint before removing the old one.
		if !hasIndex {
			if err := tx.Migrator().CreateIndex(&model.Site{}, indexName); err != nil {
				return fmt.Errorf("create site name index by kind: %w", err)
			}
		}
		if !hasLegacy {
			return nil
		}
		if tx.Dialector.Name() == "sqlite" {
			return dropSQLiteSiteNameConstraint(tx, legacyConstraint)
		}
		if err := tx.Migrator().DropConstraint(&model.Site{}, legacyConstraint); err != nil {
			return fmt.Errorf("drop global site name constraint: %w", err)
		}
		return nil
	}
	if database.Dialector.Name() != "sqlite" || !hasLegacy {
		return database.Transaction(migrate)
	}
	// SQLite rebuilds the table to drop a UNIQUE constraint. Foreign keys must
	// be disabled outside the transaction, on the same pinned connection.
	return database.Connection(func(conn *gorm.DB) (err error) {
		var foreignKeys int
		if err = conn.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error; err != nil {
			return err
		}
		if foreignKeys != 0 {
			if err = conn.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
				return err
			}
			defer func() {
				err = errors.Join(err, conn.Exec("PRAGMA foreign_keys = ON").Error)
			}()
		}
		return conn.Transaction(migrate)
	})
}

func dropSQLiteSiteNameConstraint(tx *gorm.DB, constraint string) error {
	// GORM's table rebuild drops indexes and triggers. Keep their original SQL
	// and the autoincrement high-water mark, including IDs of deleted sites.
	var definitions []string
	if err := tx.Raw("SELECT sql FROM sqlite_master WHERE tbl_name = ? AND type IN ? AND sql IS NOT NULL ORDER BY type, name",
		"sites", []string{"index", "trigger"}).Scan(&definitions).Error; err != nil {
		return err
	}
	var sequence int64
	if tx.Migrator().HasTable("sqlite_sequence") {
		if err := tx.Raw("SELECT seq FROM sqlite_sequence WHERE name = ?", "sites").Scan(&sequence).Error; err != nil {
			return err
		}
	}
	if err := tx.Migrator().DropConstraint(&model.Site{}, constraint); err != nil {
		return fmt.Errorf("drop global site name constraint: %w", err)
	}
	for _, definition := range definitions {
		if err := tx.Exec(definition).Error; err != nil {
			return fmt.Errorf("restore site schema after name migration: %w", err)
		}
	}
	if sequence > 0 {
		if err := tx.Exec("UPDATE sqlite_sequence SET seq = ? WHERE name = ? AND seq < ?", sequence, "sites", sequence).Error; err != nil {
			return fmt.Errorf("restore site ID sequence: %w", err)
		}
	}
	return nil
}
