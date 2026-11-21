package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm/clause"
)

func (s *importer) mapLog(row model.CheckinLog) (model.CheckinLog, error) {
	if row.ID <= 0 || row.SiteID < 0 || row.AccountID < 0 {
		return row, ErrInvalid
	}
	if account, ok := s.accounts[row.AccountID]; ok {
		if site, ok := s.sites[row.SiteID]; !ok || account.SiteID != site.ID {
			return row, ErrInvalid
		}
		row.AccountID, row.SiteID = account.ID, account.SiteID
	} else {
		row.AccountID, row.SiteID = 0, s.sites[row.SiteID].ID
	}
	return row, nil
}

func (s *importer) importHistory() error {
	d := s.document
	logs := make(map[int64]model.CheckinLog, len(d.SiteCheckinLogs))
	for _, row := range d.SiteCheckinLogs {
		mapped, err := s.mapLog(row)
		if err != nil {
			return err
		}
		logs[row.ID] = mapped
	}
	for _, source := range d.CheckinAccounts {
		account := s.accounts[source.ID]
		pending := account.CheckinPending
		if pending == nil {
			continue
		}
		row, exists := logs[pending.LogID]
		if exists {
			if row.AccountID != account.ID || row.SiteID != account.SiteID || row.BatchJobID != pending.BatchJobID || pending.Status != "" && row.Status != pending.Status {
				return ErrInvalid
			}
		} else {
			site := s.sites[source.SiteID]
			status := pending.Status
			if status == "" {
				status = model.SiteExecutionStatusPending
			}
			logs[pending.LogID] = model.CheckinLog{ID: pending.LogID, SiteID: site.ID, AccountID: account.ID, SiteName: site.Name, AccountName: account.Name,
				Platform: site.Platform, Source: string(pending.Source), Status: status, Reason: pending.Reason, BatchJobID: pending.BatchJobID,
				StartedAt: pending.StartedAt, FinishedAt: d.ExportedAt, Message: "Restored workflow; submission state preserved"}
			s.result.Issues = append(s.result.Issues, model.BackupIssue{Code: "workflow_history_rebuilt", Severity: "review", Entity: "account", ID: source.ID})
		}
	}
	// Notifications can outlive log retention. Recover the latest recorded
	// outcome when history was removed, retaining the recorded reward exactly.
	rebuilt := map[int64]bool{}
	for _, completion := range d.CheckinCompletions {
		entry, err := s.mapLog(completion.Entry)
		if err != nil || entry.ID != completion.LogID {
			return ErrInvalid
		}
		current, exists := logs[entry.ID]
		if exists && (current.AccountID != entry.AccountID || current.SiteID != entry.SiteID) {
			return ErrInvalid
		}
		if !exists || rebuilt[entry.ID] && entry.FinishedAt.After(current.FinishedAt) {
			logs[entry.ID] = entry
		}
		if !exists {
			rebuilt[entry.ID] = true
			s.result.Issues = append(s.result.Issues, model.BackupIssue{Code: "notification_history_rebuilt", Severity: "review", Entity: "history"})
		}
	}
	for _, source := range d.SiteCheckinBatchJobs {
		row := source
		if err := s.unusedID(&row, row.ID); err != nil {
			return err
		}
		row.SiteIDs = slices.Clone(row.SiteIDs)
		for i, id := range row.SiteIDs {
			row.SiteIDs[i] = s.sites[id].ID
		}
		row.CurrentSiteID = s.sites[row.CurrentSiteID].ID
		row.CurrentAccountID = s.accounts[row.CurrentAccountID].ID
		if row.Status == model.SiteCheckinBatchJobStatusQueued || row.Status == model.SiteCheckinBatchJobStatusRunning {
			row.Status, row.ErrorMessage, row.UpdatedAt = model.SiteCheckinBatchJobStatusInterrupted, "Interrupted by backup restore", d.ExportedAt
			finishedAt := d.ExportedAt
			row.FinishedAt = &finishedAt
		}
		if err := s.insert("site_checkin_batch_jobs", &row); err != nil {
			return err
		}
		if err := s.tx.Create(&importMapping{Fingerprint: s.fingerprint, Entity: "batch", SourceID: row.ID, TargetID: row.ID}).Error; err != nil {
			return err
		}
	}
	for _, row := range logs {
		if err := s.unusedID(&row, row.ID); err != nil {
			return err
		}
		if err := s.insert("site_checkin_logs", &row); err != nil {
			return err
		}
		if err := s.tx.Create(&importMapping{Fingerprint: s.fingerprint, Entity: "log", SourceID: row.ID, TargetID: row.ID}).Error; err != nil {
			return err
		}
	}
	if !d.IncludeLogs {
		return nil
	}
	return walkRelay(s.tx.Statement.Context, d, s.importRelayRow)
}

func (s *importer) importRelayRow(name string, data any) error {
	switch row := data.(type) {
	case model.RelayLog:
		if err := s.unusedID(&row, row.ID); err != nil {
			return err
		}
		row.ChannelId, row.RequestAPIKeyID = s.configuration.Channels[row.ChannelId], s.configuration.APIKeys[row.RequestAPIKeyID]
		row.Attempts = slices.Clone(row.Attempts)
		for i := range row.Attempts {
			row.Attempts[i].ChannelID = s.configuration.Channels[row.Attempts[i].ChannelID]
			row.Attempts[i].ChannelKeyID = s.configuration.ChannelKeys[row.Attempts[i].ChannelKeyID]
		}
		if err := s.insert(name, &row); err != nil {
			return err
		}
		return s.tx.Create(&importMapping{Fingerprint: s.fingerprint, Entity: "relay_log", SourceID: row.ID, TargetID: row.ID}).Error
	case model.RelayRequestContent:
		if err := s.relayContentOwner(row.LogID); err != nil {
			return err
		}
		return s.insert(name, &row)
	case model.RelayAttemptContent:
		if err := s.relayContentOwner(row.LogID); err != nil {
			return err
		}
		return s.insert(name, &row)
	}
	return ErrInvalid
}

func (s *importer) relayContentOwner(id int64) error {
	var count int64
	if err := s.tx.Model(&importMapping{}).Where("fingerprint = ? AND entity = ? AND source_id = ?", s.fingerprint, "relay_log", id).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrInvalid
	}
	return nil
}

func (s *importer) importArchives() error {
	if !s.document.IncludeStats {
		return nil
	}
	rows := slices.Clone(s.document.ArchivedSiteStats)
	for _, row := range s.document.StatsSiteModelHourly {
		rows = append(rows, ArchivedSiteStat{Stats: row})
	}
	for _, row := range rows {
		if row.Source == "" {
			row.Source = s.fingerprint
		}
		if len(row.Source) != 64 {
			return ErrInvalid
		}
		identity, err := json.Marshal([]any{row.Stats.Hour, row.Stats.SiteAccountID, row.Stats.GroupKey, row.Stats.ModelName})
		if err != nil {
			return ErrInvalid
		}
		digest := sha256.Sum256(identity)
		row.Key = hex.EncodeToString(digest[:])
		result := s.tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
		s.result.RowsAffected["archived_site_stats"] += result.RowsAffected
	}
	return nil
}
