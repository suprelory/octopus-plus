package handlers

import (
	"strconv"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

// The shared storage models retain their legacy JSON contract. New API DTOs
// serialize snowflake IDs as decimal strings and omit private workflow evidence.
type checkinSiteResponse struct {
	model.CheckinSite
	Capability model.SiteCheckinCapability `json:"capability"`
}

func checkinSiteDTO(site *model.CheckinSite) checkinSiteResponse {
	return checkinSiteResponse{*site, site.ResolveCheckinCapability()}
}

type checkinPendingResponse struct {
	LogID      string                    `json:"log_id"`
	Source     model.CheckinTrigger      `json:"source"`
	BatchJobID string                    `json:"batch_job_id,omitempty"`
	Date       string                    `json:"date"`
	StartedAt  time.Time                 `json:"started_at"`
	Deadline   time.Time                 `json:"deadline"`
	Submitted  bool                      `json:"submitted"`
	Finished   bool                      `json:"finished"`
	Status     model.SiteExecutionStatus `json:"status"`
	Reason     string                    `json:"reason"`
}

type checkinAccountResponse struct {
	model.CheckinAccount
	Pending *checkinPendingResponse `json:"checkin_pending,omitempty"`
}

func checkinAccountDTO(account *model.CheckinAccount) checkinAccountResponse {
	result := checkinAccountResponse{CheckinAccount: *account}
	if p := account.CheckinPending; p != nil {
		result.Pending = &checkinPendingResponse{LogID: strconv.FormatInt(p.LogID, 10), Source: p.Source, BatchJobID: checkinOptionalID(p.BatchJobID), Date: p.Date, StartedAt: p.StartedAt, Deadline: p.Deadline, Submitted: p.Submitted, Finished: p.Finished, Status: p.Status, Reason: p.Reason}
	}
	return result
}

type checkinLogResponse struct {
	model.CheckinLog
	ID         string `json:"id"`
	BatchJobID string `json:"batch_job_id,omitempty"`
}

func checkinLogDTO(entry model.CheckinLog) checkinLogResponse {
	return checkinLogResponse{entry, strconv.FormatInt(entry.ID, 10), checkinOptionalID(entry.BatchJobID)}
}

type checkinPage[T any] struct {
	Items        []T    `json:"items"`
	NextBeforeID string `json:"next_before_id,omitempty"`
}

func checkinOptionalID(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}
