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
	accountIDMap := s.accountIDs
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

		// StatsSiteModelHourly: remap SiteAccountID (composite PK)
		filteredSiteModelHourly := make([]model.StatsSiteModelHourly, 0, len(dump.StatsSiteModelHourly))
		for _, row := range dump.StatsSiteModelHourly {
			newID, ok := accountIDMap[row.SiteAccountID]
			if !ok {
				continue
			}
			row.SiteAccountID = newID
			filteredSiteModelHourly = append(filteredSiteModelHourly, row)
		}
		if n, err := createUpsertAll(tx, filteredSiteModelHourly, []clause.Column{
			{Name: "hour"}, {Name: "site_account_id"}, {Name: "group_key"}, {Name: "model_name"},
		}); err != nil {
			return fmt.Errorf("import stats_site_model_hourly: %w", err)
		} else {
			res.RowsAffected["stats_site_model_hourly"] = n
		}
	}
	return nil
}

func (s *dbImportState) importLogs() error {
	tx := s.tx
	dump := s.dump
	res := s.result
	// 16. RelayLogs (Snowflake IDs - keep createDoNothing)
	if dump.IncludeLogs {
		if n, err := createDoNothing(tx, dump.RelayLogs); err != nil {
			return fmt.Errorf("import relay_logs: %w", err)
		} else {
			res.RowsAffected["relay_logs"] = n
		}
		if n, err := createDoNothing(tx, dump.RelayRequestContents); err != nil {
			return fmt.Errorf("import relay_request_contents: %w", err)
		} else {
			res.RowsAffected["relay_request_contents"] = n
		}
		if n, err := createDoNothing(tx, dump.RelayAttemptContents); err != nil {
			return fmt.Errorf("import relay_attempt_contents: %w", err)
		} else {
			res.RowsAffected["relay_attempt_contents"] = n
		}
		if n, err := createDoNothing(tx, dump.SiteCheckinBatchJobs); err != nil {
			return fmt.Errorf("import site_checkin_batch_jobs: %w", err)
		} else {
			res.RowsAffected["site_checkin_batch_jobs"] = n
		}
		checkinLogs := make([]model.SiteCheckinLog, 0, len(dump.SiteCheckinLogs))
		for _, entry := range dump.SiteCheckinLogs {
			// Deleted sites/accounts have no mapping. Retain their name snapshots,
			// but never associate their old IDs with unrelated destination rows.
			entry.SiteID = s.siteIDs[entry.SiteID]
			entry.AccountID = s.accountIDs[entry.AccountID]
			checkinLogs = append(checkinLogs, entry)
		}
		if n, err := createDoNothing(tx, checkinLogs); err != nil {
			return fmt.Errorf("import site_checkin_logs: %w", err)
		} else {
			res.RowsAffected["site_checkin_logs"] = n
		}
	}
	return nil
}
