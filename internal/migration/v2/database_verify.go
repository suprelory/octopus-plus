package v2

import (
	"errors"
	"fmt"
	"math"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

// The history tables have application-allocated int64 IDs. Only the three
// configuration tables use database sequences. Also reserve deleted owners'
// IDs so new configurations cannot accidentally inherit their history.
func sequenceFloors(input *LegacyInput, c *Conversion) map[string]int {
	var sites, accounts, credentials int
	for _, row := range input.Configuration.Sites {
		sites = max(sites, row.ID)
	}
	for _, row := range input.Configuration.SiteAccounts {
		accounts = max(accounts, row.ID)
	}
	for _, row := range c.Credentials {
		credentials = max(credentials, row.ID)
	}
	for _, row := range c.Logs {
		sites, accounts = max(sites, row.SiteID), max(accounts, row.AccountID)
	}
	for _, row := range c.Batches {
		sites, accounts = max(sites, row.CurrentSiteID), max(accounts, row.CurrentAccountID)
		for _, id := range row.SiteIDs {
			sites = max(sites, id)
		}
	}
	return map[string]int{"checkin_sites": sites, "checkin_accounts": accounts, "checkin_credentials": credentials}
}

// MySQL ALTER TABLE implicitly commits. Execute it outside the journal
// transaction. All table names come from sequenceFloors. A retry can only
// raise a counter, including when a table has previously deleted larger IDs.
func advanceMySQLSequences(conn *gorm.DB, floors map[string]int) error {
	return conn.Connection(func(db *gorm.DB) error {
		// MySQL 8 caches information_schema counters for up to a day by default.
		if err := db.Exec("SET SESSION information_schema_stats_expiry = 0").Error; err != nil {
			return err
		}
		for table, floor := range floors {
			var next int64
			if err := db.Raw("SELECT COALESCE(AUTO_INCREMENT, 1) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", table).Scan(&next).Error; err != nil {
				return err
			}
			if floor >= math.MaxInt {
				return errors.New("migration ID space exhausted")
			}
			if next <= int64(floor) {
				if err := db.Exec(fmt.Sprintf("ALTER TABLE %s AUTO_INCREMENT = %d", table, floor+1)).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func advanceSequences(tx *gorm.DB, floors map[string]int) error {
	for table, floor := range floors {
		var live int
		if err := tx.Table(table).Select("COALESCE(MAX(id), 0)").Scan(&live).Error; err != nil {
			return err
		}
		floor = max(floor, live)
		if floor == 0 {
			continue
		}
		if floor == math.MaxInt {
			return ErrMigrationVerification
		}
		switch tx.Dialector.Name() {
		case "sqlite":
			if err := tx.Exec("INSERT INTO sqlite_sequence(name, seq) SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = ?)", table, floor, table).Error; err != nil {
				return err
			}
			if err := tx.Exec("UPDATE sqlite_sequence SET seq = ? WHERE name = ? AND seq < ?", floor, table, floor).Error; err != nil {
				return err
			}
		case "postgres":
			var seq string
			if err := tx.Raw("SELECT pg_get_serial_sequence(?, 'id')", table).Scan(&seq).Error; err != nil || seq == "" {
				return ErrMigrationVerification
			}
			if err := tx.Exec("SELECT setval(?::regclass, ?, true) WHERE COALESCE(pg_sequence_last_value(?::regclass), 0) < ?", seq, floor, seq, floor).Error; err != nil {
				return err
			}
		case "mysql": // Advanced outside the transaction.
		default:
			return ErrMigrationVerification
		}
	}
	return nil
}

func verifyConversion(tx *gorm.DB, input *LegacyInput, c *Conversion) error {
	// Compare the full shared tables, including keys, presets, archived stats,
	// log rewards and interrupted batch state, with the expected post-image.
	if err := verifyLegacyState(tx, input, c, stageIndex(StageVerify)); err != nil {
		return err
	}
	exists := func(row any, id int) bool {
		var count int64
		return tx.Model(row).Where("id = ?", id).Count(&count).Error == nil && count == 1
	}
	for _, site := range c.Sites {
		if !exists(&model.CheckinSite{}, site.ID) {
			return ErrMigrationVerification
		}
	}
	for _, credential := range c.Credentials {
		if !exists(&model.CheckinCredential{}, credential.ID) {
			return ErrMigrationVerification
		}
	}
	for _, account := range c.Accounts {
		var saved model.CheckinAccount
		if err := tx.First(&saved, account.ID).Error; err != nil || saved.SiteID != account.SiteID {
			return ErrMigrationVerification
		}
		if saved.CredentialID == 0 {
			if account.CredentialID != 0 || saved.AutoCheckin || saved.NextAutoCheckinAt != nil {
				return ErrMigrationVerification
			}
		} else if !exists(&model.CheckinCredential{}, saved.CredentialID) {
			return ErrMigrationVerification
		}
		if id := saved.BalanceConfig.CredentialID; id != nil && !exists(&model.CheckinCredential{}, *id) {
			return ErrMigrationVerification
		}
		if pending := account.CheckinPending; pending != nil {
			current := saved.CheckinPending
			if current == nil || current.LogID != pending.LogID || current.Submitted != pending.Submitted || current.Fingerprint != pending.Fingerprint || current.Finished != pending.Finished {
				return ErrMigrationVerification
			}
		}
	}
	return advanceSequences(tx, sequenceFloors(input, c))
}
