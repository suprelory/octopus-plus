// Package v1 contains schema upgrades needed to read and convert historical databases.
package v1

import (
	"fmt"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

// PrepareCheckinHistorySchema upgrades the two history tables shared with v2
// without changing job state. The offline migrator may retry this after partial DDL.
func PrepareCheckinHistorySchema(conn *gorm.DB) error {
	if conn == nil || !conn.Migrator().HasTable(&model.CheckinLog{}) || !conn.Migrator().HasTable(&model.CheckinBatchJob{}) {
		return fmt.Errorf("legacy checkin history tables are required")
	}
	if !conn.Migrator().HasColumn(&model.CheckinBatchJob{}, "Pending") {
		if err := conn.Migrator().AddColumn(&model.CheckinBatchJob{}, "Pending"); err != nil {
			return fmt.Errorf("add checkin batch pending count: %w", err)
		}
	}
	if !conn.Migrator().HasColumn(&model.CheckinLog{}, "BatchJobID") {
		if err := conn.Migrator().AddColumn(&model.CheckinLog{}, "BatchJobID"); err != nil {
			return fmt.Errorf("add site checkin log batch job id: %w", err)
		}
	}
	if !conn.Migrator().HasIndex(&model.CheckinLog{}, "idx_site_checkin_batch_job_id") {
		if err := conn.Migrator().CreateIndex(&model.CheckinLog{}, "BatchJobID"); err != nil {
			return fmt.Errorf("create site checkin log batch job index: %w", err)
		}
	}
	return nil
}
