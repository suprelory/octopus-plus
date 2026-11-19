package handlers

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/checkin/rules"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/server/resp"
)

func (a *checkinAPI) execute(c *gin.Context) {
	id, ok := checkinPathID(c)
	if !ok || !checkinEmptyAction(c) {
		return
	}
	result, err := a.services.Engine.Execute(c.Request.Context(), id, model.CheckinTriggerManual)
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, result)
}

func (a *checkinAPI) confirm(c *gin.Context) {
	id, ok := checkinPathID(c)
	if !ok || !checkinEmptyAction(c) {
		return
	}
	result, err := a.services.Engine.Confirm(c.Request.Context(), id)
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, result)
}

func (a *checkinAPI) balance(c *gin.Context) {
	id, ok := checkinPathID(c)
	if !ok || !checkinEmptyAction(c) {
		return
	}
	result, err := a.services.Balances.Query(c.Request.Context(), id, true)
	if err != nil {
		checkinError(c, err)
		return
	}
	var used, income *float64
	if result.UsedKnown {
		used = &result.Used
	}
	if result.IncomeKnown {
		income = &result.Income
	}
	resp.Success(c, struct {
		AccountID   int      `json:"account_id"`
		Balance     float64  `json:"balance"`
		BalanceUsed *float64 `json:"balance_used"`
		TodayIncome *float64 `json:"today_income"`
	}{id, result.Value, used, income})
}

func (a *checkinAPI) startBatch(c *gin.Context) {
	if _, ok := checkinQuery(c); !ok {
		return
	}
	var request model.CheckinBatchSelection
	if !bindCheckinJSON(c, &request, false, 1<<20) {
		return
	}
	if len(request.SiteIDs) > 1000 {
		resp.InvalidParam(c)
		return
	}
	job, err := a.services.Jobs.StartBatch(c.Request.Context(), request)
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, job)
}

func (a *checkinAPI) getBatch(c *gin.Context) {
	id, ok := checkinLongPathID(c)
	if !ok {
		return
	}
	if _, ok := checkinQuery(c); !ok {
		return
	}
	job, err := a.services.Store.GetBatch(c.Request.Context(), id)
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, job)
}

func (a *checkinAPI) listBatches(c *gin.Context) {
	query, ok := checkinQuery(c, "limit", "before_id")
	if !ok {
		return
	}
	limit, before, ok := checkinPageParams(c, query)
	if !ok {
		return
	}
	page, err := a.services.Store.ListBatches(c.Request.Context(), limit, before)
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, checkinPage[model.CheckinBatchJob]{Items: page.Items, NextBeforeID: checkinOptionalID(page.NextBeforeID)})
}

func (a *checkinAPI) capabilities(c *gin.Context) {
	if _, ok := checkinQuery(c); !ok {
		return
	}
	resp.Success(c, model.AllPlatformCheckinDefaults())
}

func (a *checkinAPI) testReward(c *gin.Context) {
	if _, ok := checkinQuery(c); !ok {
		return
	}
	var request struct {
		Code           string          `json:"code"`
		Response       json.RawMessage `json:"response"`
		StatusResponse json.RawMessage `json:"status_response"`
	}
	if !bindCheckinJSON(c, &request, false, 2<<20) {
		return
	}
	if strings.TrimSpace(request.Code) == "" || !json.Valid(request.Response) {
		resp.InvalidParam(c)
		return
	}
	var reward string
	var err error
	if len(request.StatusResponse) > 0 {
		reward, err = rules.ExtractCheckinRewardResponses(c.Request.Context(), request.Code, request.Response, request.StatusResponse)
	} else {
		reward, err = rules.ExtractCheckinReward(c.Request.Context(), request.Code, request.Response)
	}
	if err != nil {
		resp.ErrorWithCode(c, 400, "checkin.invalid_reward_rule", "reward rule failed; check the sample, code and resource limits")
		return
	}
	resp.Success(c, gin.H{"reward": reward, "found": reward != ""})
}

func (a *checkinAPI) testStatus(c *gin.Context) {
	if _, ok := checkinQuery(c); !ok {
		return
	}
	var request struct {
		Config     *model.SiteCheckinStatusConfig `json:"config"`
		HTTPStatus int                            `json:"http_status"`
		Response   json.RawMessage                `json:"response"`
		Date       string                         `json:"date"`
	}
	if !bindCheckinJSON(c, &request, false, 2<<20) {
		return
	}
	if request.Config == nil {
		resp.InvalidParam(c)
		return
	}
	state, err := rules.TestCheckinStatusRule(c.Request.Context(), request.Config, request.HTTPStatus, request.Response, request.Date)
	if err != nil {
		resp.ErrorWithCode(c, 400, "checkin.invalid_status_rule", "status rule failed; check the configuration, sample and resource limits")
		return
	}
	resp.Success(c, gin.H{"state": state})
}

func (a *checkinAPI) listNotifications(c *gin.Context) {
	query, ok := checkinQuery(c, "limit", "before_id")
	if !ok {
		return
	}
	limit, before, ok := checkinPageParams(c, query)
	if !ok {
		return
	}
	page, err := a.services.Store.NotificationPage(c.Request.Context(), before, limit)
	if err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, page)
}

func (a *checkinAPI) retryNotification(c *gin.Context) {
	id, ok := checkinLongPathID(c)
	if !ok || !checkinEmptyAction(c) {
		return
	}
	if err := a.services.Store.RetryNotification(c.Request.Context(), id); err != nil {
		checkinError(c, err)
		return
	}
	resp.Success(c, gin.H{"id": strconv.FormatInt(id, 10), "state": "queued"})
	a.services.Notifications.Wake()
}
