package op

import (
	"fmt"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm/clause"
)

func (s *dbImportState) importStats() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	apiKeyIDMap := s.apiKeyIDs
	resolveImportedChannel := s.resolveChannel
	// 15. Stats (remap FK IDs, then upsert)
	if dump.IncludeStats {
		if n, err := createUpsertAll(tx, dump.StatsTotal, []clause.Column{{Name: "id"}}); err != nil {
			return fmt.Errorf("import stats_total: %w", err)
		} else {
			res.RowsAffected["stats_total"] = n
		}
		if n, err := createUpsertAll(tx, dump.StatsDaily, []clause.Column{{Name: "date"}}); err != nil {
			return fmt.Errorf("import stats_daily: %w", err)
		} else {
			res.RowsAffected["stats_daily"] = n
		}
		if n, err := createUpsertAll(tx, dump.StatsHourly, []clause.Column{{Name: "hour"}}); err != nil {
			return fmt.Errorf("import stats_hourly: %w", err)
		} else {
			res.RowsAffected["stats_hourly"] = n
		}

		// StatsModel: remap ChannelID and retain statistics for historical channels.
		// Only truly orphaned rows are skipped.
		filteredStatsModel := make([]model.StatsModel, 0, len(dump.StatsModel))
		for _, row := range dump.StatsModel {
			resolved, err := resolveImportedChannel(row.ChannelID)
			if err != nil {
				return fmt.Errorf("import stats_model: resolve channel: %w", err)
			}
			if !resolved.Exists {
				continue
			}
			row.ChannelID = resolved.ID
			filteredStatsModel = append(filteredStatsModel, row)
		}
		if n, err := importLegacyStatsModels(tx, filteredStatsModel); err != nil {
			return fmt.Errorf("import stats_model: %w", err)
		} else {
			res.RowsAffected["stats_model"] = n
		}

		// StatsChannel: remap ChannelID (which is the PK) while retaining historical
		// statistics for retired channels.
		filteredStatsChannel := make([]model.StatsChannel, 0, len(dump.StatsChannel))
		for _, row := range dump.StatsChannel {
			resolved, err := resolveImportedChannel(row.ChannelID)
			if err != nil {
				return fmt.Errorf("import stats_channel: resolve channel: %w", err)
			}
			if !resolved.Exists {
				continue
			}
			row.ChannelID = resolved.ID
			filteredStatsChannel = append(filteredStatsChannel, row)
		}
		if n, err := createUpsertAll(tx, filteredStatsChannel, []clause.Column{{Name: "channel_id"}}); err != nil {
			return fmt.Errorf("import stats_channel: %w", err)
		} else {
			res.RowsAffected["stats_channel"] = n
		}

		// StatsAPIKey: remap APIKeyID (which is the PK). Skip orphaned rows whose
		// API key is not present in the dump, otherwise SQLite foreign keys can fail.
		filteredStatsAPIKey := make([]model.StatsAPIKey, 0, len(dump.StatsAPIKey))
		for _, row := range dump.StatsAPIKey {
			newID, ok := apiKeyIDMap[row.APIKeyID]
			if !ok {
				continue
			}
			row.APIKeyID = newID
			filteredStatsAPIKey = append(filteredStatsAPIKey, row)
		}
		if n, err := createUpsertAll(tx, filteredStatsAPIKey, []clause.Column{{Name: "api_key_id"}}); err != nil {
			return fmt.Errorf("import stats_api_key: %w", err)
		} else {
			res.RowsAffected["stats_api_key"] = n
		}

	}
	return nil
}
