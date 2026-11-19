package handlers

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/resp"
)

func checkinHistoryFilter(c *gin.Context, stats bool) (op.SiteCheckinLogFilter, bool) {
	filter := op.SiteCheckinLogFilter{}
	allowed := []string{"site_id", "account_id", "status", "source", "batch_id", "before_id", "from", "until"}
	if stats {
		allowed = append(allowed, "timezone")
	} else {
		allowed = append(allowed, "limit")
	}
	query, ok := checkinQuery(c, allowed...)
	if !ok {
		return filter, false
	}
	filter.Limit, filter.BeforeID, ok = checkinPageParams(c, query)
	if !ok {
		return filter, false
	}
	for name, target := range map[string]*int{"site_id": &filter.SiteID, "account_id": &filter.AccountID} {
		if raw := query.Get(name); raw != "" {
			id, ok := checkinPositiveID(raw, strconv.IntSize)
			if !ok {
				resp.InvalidParam(c)
				return filter, false
			}
			*target = int(id)
		}
	}
	if raw := query.Get("batch_id"); raw != "" {
		filter.BatchJobID, ok = checkinPositiveID(raw, 64)
		if !ok {
			resp.InvalidParam(c)
			return filter, false
		}
	}
	filter.Status = model.SiteExecutionStatus(query.Get("status"))
	switch filter.Status {
	case "", model.SiteExecutionStatusSuccess, model.SiteExecutionStatusFailed, model.SiteExecutionStatusSkipped, model.SiteExecutionStatusPending:
	default:
		resp.InvalidParam(c)
		return filter, false
	}
	filter.Source = query.Get("source")
	// Historical imports retain their legacy source; new execution uses batch.
	switch filter.Source {
	case "", "manual", "scheduled", "batch", "import", "auto":
	default:
		resp.InvalidParam(c)
		return filter, false
	}
	for name, target := range map[string]**time.Time{"from": &filter.From, "until": &filter.Until} {
		if raw := query.Get(name); raw != "" {
			value, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				resp.InvalidParam(c)
				return filter, false
			}
			*target = &value
		}
	}
	if filter.From != nil && filter.Until != nil && !filter.From.Before(*filter.Until) {
		resp.InvalidParam(c)
		return filter, false
	}
	if timezone := query.Get("timezone"); timezone != "" {
		location, err := time.LoadLocation(timezone)
		if err != nil {
			resp.InvalidParam(c)
			return filter, false
		}
		filter.Location = location
	}
	return filter, true
}

func (a *checkinAPI) listLogs(c *gin.Context) {
	filter, ok := checkinHistoryFilter(c, false)
	if !ok {
		return
	}
	page, err := a.services.Store.ListHistory(c.Request.Context(), filter)
	if err != nil {
		checkinError(c, err)
		return
	}
	items := make([]checkinLogResponse, 0, len(page.Items))
	for _, entry := range page.Items {
		items = append(items, checkinLogDTO(entry))
	}
	resp.Success(c, checkinPage[checkinLogResponse]{Items: items, NextBeforeID: checkinOptionalID(page.NextBeforeID)})
}

func (a *checkinAPI) stats(c *gin.Context) {
	filter, ok := checkinHistoryFilter(c, true)
	if !ok {
		return
	}
	stats, err := a.services.Store.RewardStats(c.Request.Context(), filter)
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, stats)
}
