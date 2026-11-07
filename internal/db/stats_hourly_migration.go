package db

import (
	"fmt"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func ensureStatsHourlyHourKey(database *gorm.DB) error {
	// SQLite and PostgreSQL preserve an explicitly supplied zero even with the
	// legacy generated-ID schema. MySQL AUTO_INCREMENT substitutes a new ID, so
	// existing MySQL tables also need the column definition corrected.
	if database.Dialector.Name() != "mysql" {
		return nil
	}
	migrator := database.Migrator()
	columns, err := migrator.ColumnTypes(&model.StatsHourly{})
	if err != nil {
		return fmt.Errorf("inspect hourly stats hour key: %w", err)
	}
	for _, column := range columns {
		if column.Name() != "hour" {
			continue
		}
		if autoIncrement, _ := column.AutoIncrement(); autoIncrement {
			if err := migrator.AlterColumn(&model.StatsHourly{}, "Hour"); err != nil {
				return fmt.Errorf("disable hourly stats hour auto increment: %w", err)
			}
		}
		return nil
	}
	return fmt.Errorf("hourly stats hour column is missing")
}
