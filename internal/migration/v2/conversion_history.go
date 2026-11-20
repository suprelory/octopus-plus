package v2

import (
	"cmp"
	"slices"

	"github.com/suprelory/octopus-plus/internal/model"
)

func (b *conversionBuilder) convertHistory() {
	c := b.result
	if !b.input.HistoryComplete {
		c.issue("review", "history_not_included", "backup", 0, 0)
	}
	logs := map[int64]model.CheckinLog{}
	for _, old := range b.input.Logs {
		if old.ID <= 0 {
			c.issue("blocker", "invalid_history_id", "history", 0, 0)
			continue
		}
		if _, exists := logs[old.ID]; exists {
			c.issue("blocker", "duplicate_history_id", "history", 0, 0)
			continue
		}
		entry := old
		if target, exists := b.accountTargets[old.AccountID]; exists {
			account := b.accounts[target]
			if source, exists := b.planner.accounts[old.AccountID]; exists && source.SiteID != old.SiteID {
				c.issue("blocker", "history_account_site_mismatch", "account", old.AccountID, old.SiteID)
			} else {
				entry.AccountID, entry.SiteID = target, account.SiteID
			}
		} else if siteID, exists := b.siteTargets[old.SiteID]; exists {
			entry.SiteID = siteID
		}
		// Deleted entities retain historical IDs and names; no fake live account is created.
		logs[entry.ID] = entry
	}
	batchIDs := map[int64]bool{}
	for _, old := range b.input.Batches {
		if old.ID <= 0 || batchIDs[old.ID] {
			c.issue("blocker", "invalid_or_duplicate_batch_id", "history", 0, 0)
			continue
		}
		batchIDs[old.ID] = true
		job := old
		job.SiteIDs = slices.Clone(old.SiteIDs)
		job.FinishedAt = clonePointer(old.FinishedAt)
		for i, siteID := range job.SiteIDs {
			if target, exists := b.siteTargets[siteID]; exists {
				job.SiteIDs[i] = target
			}
		}
		job.SiteIDs = sortedIDs(job.SiteIDs)
		if old.SiteIDs == nil {
			job.SiteIDs = nil
		}
		if target, exists := b.accountTargets[job.CurrentAccountID]; exists {
			job.CurrentAccountID, job.CurrentSiteID = target, b.accounts[target].SiteID
		} else if target, exists := b.siteTargets[job.CurrentSiteID]; exists {
			job.CurrentSiteID = target
		}
		if job.Status == model.SiteCheckinBatchJobStatusQueued || job.Status == model.SiteCheckinBatchJobStatusRunning {
			job.Status = model.SiteCheckinBatchJobStatusInterrupted
			job.ErrorMessage = "Interrupted by v2 migration"
			job.FinishedAt = clonePointer(&b.input.CapturedAt)
			job.UpdatedAt = b.input.CapturedAt
			c.Report.InterruptedBatches++
		}
		c.Batches = append(c.Batches, job)
	}
	accountIDs := make([]int, 0, len(b.accounts))
	for id := range b.accounts {
		accountIDs = append(accountIDs, id)
	}
	slices.Sort(accountIDs)
	for _, id := range accountIDs {
		account := b.accounts[id]
		pending := account.CheckinPending
		if pending == nil {
			continue
		}
		if pending.LogID <= 0 {
			c.issue("blocker", "invalid_workflow_log_id", "account", id, 0)
			continue
		}
		if entry, exists := logs[pending.LogID]; exists {
			if entry.AccountID != id || entry.SiteID != account.SiteID {
				c.issue("blocker", "workflow_log_owner_conflict", "account", id, entry.AccountID)
				continue
			}
			if pending.BatchJobID != 0 && entry.BatchJobID != pending.BatchJobID {
				c.issue("blocker", "workflow_batch_conflict", "account", id, 0)
			}
			if pending.Status != "" && entry.Status != "" && pending.Status != entry.Status {
				c.issue("blocker", "workflow_history_state_conflict", "account", id, 0)
			}
			if pending.Source == "" {
				pending.Source = model.CheckinTrigger(entry.Source)
			}
			if pending.BatchJobID == 0 {
				pending.BatchJobID = entry.BatchJobID
			}
		} else {
			status, reason := pending.Status, pending.Reason
			if status == "" {
				status = model.SiteExecutionStatusPending
			}
			if reason == "" {
				reason = "pending_confirmation"
			}
			source := string(pending.Source)
			if source == "" {
				source = "import"
			}
			logs[pending.LogID] = model.CheckinLog{ID: pending.LogID, SiteID: account.SiteID, AccountID: id,
				SiteName: b.sites[account.SiteID].Name, AccountName: account.Name, Platform: b.sites[account.SiteID].Platform,
				Source: source, BatchJobID: pending.BatchJobID, Status: status, Reason: reason,
				Message: "Restored workflow; submission state preserved", StartedAt: pending.StartedAt, FinishedAt: b.input.CapturedAt}
			c.Report.RebuiltLogs++
			c.issue("review", "workflow_history_rebuilt", "account", id, 0)
		}
		if pending.BatchJobID != 0 && !batchIDs[pending.BatchJobID] {
			c.issue("review", "workflow_batch_history_missing", "account", id, 0)
		}
	}
	for _, log := range logs {
		c.Logs = append(c.Logs, log)
	}
	slices.SortFunc(c.Logs, func(a, c model.CheckinLog) int { return cmp.Compare(a.ID, c.ID) })
	slices.SortFunc(c.Batches, func(a, c model.CheckinBatchJob) int { return cmp.Compare(a.ID, c.ID) })
}
