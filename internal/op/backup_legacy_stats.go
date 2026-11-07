package op

import (
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func loadLegacyStatsModels(conn *gorm.DB) ([]model.StatsModel, error) {
	rows := []model.StatsModel{}
	if !conn.Migrator().HasTable(&model.StatsModel{}) {
		return rows, nil
	}
	err := conn.Order("id ASC").Find(&rows).Error
	return rows, err
}

func importLegacyStatsModels(tx *gorm.DB, rows []model.StatsModel) (int64, error) {
	// IDs change when channels and records are restored. Compare the remapped
	// channel, model name and all metrics so identical imports are idempotent,
	// while distinct historical snapshots for the same model are preserved.
	// A map includes zero-valued metrics, and database comparisons respect the
	// stored numeric precision when restoring across different SQL dialects.
	var imported int64
	for _, row := range rows {
		row.ID = 0
		result := tx.Where(map[string]any{
			"name":            row.Name,
			"channel_id":      row.ChannelID,
			"input_token":     row.InputToken,
			"output_token":    row.OutputToken,
			"input_cost":      row.InputCost,
			"output_cost":     row.OutputCost,
			"wait_time":       row.WaitTime,
			"request_success": row.RequestSuccess,
			"request_failed":  row.RequestFailed,
		}).FirstOrCreate(&row)
		if result.Error != nil {
			return 0, result.Error
		}
		imported += result.RowsAffected
	}
	return imported, nil
}
