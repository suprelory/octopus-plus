package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/resp"
)

func getSiteCheckinCapabilities(c *gin.Context) {
	resp.Success(c, model.AllPlatformCheckinDefaults())
}

func listSiteCheckinLogs(c *gin.Context) {
	filter := op.SiteCheckinLogFilter{
		Status: model.SiteExecutionStatus(c.Query("status")),
		Source: c.Query("source"),
	}
	switch filter.Status {
	case "", model.SiteExecutionStatusSuccess, model.SiteExecutionStatusFailed, model.SiteExecutionStatusSkipped:
	default:
		resp.InvalidParam(c)
		return
	}
	switch filter.Source {
	case "", "manual", "scheduled", "import", "auto":
	default:
		resp.InvalidParam(c)
		return
	}
	if raw := c.Query("batch_id"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			resp.InvalidParam(c)
			return
		}
		filter.BatchJobID = value
	}
	for name, target := range map[string]*int{"site_id": &filter.SiteID, "account_id": &filter.AccountID, "limit": &filter.Limit} {
		if raw := c.Query(name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value <= 0 || (name == "limit" && value > 100) {
				resp.InvalidParam(c)
				return
			}
			*target = value
		}
	}
	if raw := c.Query("before_id"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			resp.InvalidParam(c)
			return
		}
		filter.BeforeID = value
	}
	for name, target := range map[string]**time.Time{"from": &filter.From, "until": &filter.Until} {
		if raw := c.Query(name); raw != "" {
			value, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				resp.InvalidParam(c)
				return
			}
			*target = &value
		}
	}
	if filter.From != nil && filter.Until != nil && !filter.From.Before(*filter.Until) {
		resp.InvalidParam(c)
		return
	}
	page, err := op.SiteCheckinLogList(c.Request.Context(), filter)
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, page)
}

func getSiteCheckinStats(c *gin.Context) {
	filter := op.SiteCheckinLogFilter{}
	if timezone := c.Query("timezone"); timezone != "" {
		location, err := time.LoadLocation(timezone)
		if err != nil {
			resp.InvalidParam(c)
			return
		}
		filter.Location = location
	}
	for name, target := range map[string]*int{"site_id": &filter.SiteID, "account_id": &filter.AccountID} {
		if raw := c.Query(name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value <= 0 {
				resp.InvalidParam(c)
				return
			}
			*target = value
		}
	}
	for name, target := range map[string]**time.Time{"from": &filter.From, "until": &filter.Until} {
		if raw := c.Query(name); raw != "" {
			value, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				resp.Error(c, http.StatusBadRequest, "invalid "+name+" timestamp")
				return
			}
			*target = &value
		}
	}
	if filter.From != nil && filter.Until != nil && !filter.From.Before(*filter.Until) {
		resp.InvalidParam(c)
		return
	}
	stats, err := op.SiteCheckinStats(c.Request.Context(), filter)
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, stats)
}
