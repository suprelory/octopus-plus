package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/sitesync"
)

func testCheckinRewardExtractor(c *gin.Context) {
	// Testing accepts a sample response and executes no check-in or balance request.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	var request struct {
		Code           string          `json:"code"`
		Response       json.RawMessage `json:"response"`
		StatusResponse json.RawMessage `json:"status_response"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if strings.TrimSpace(request.Code) == "" {
		resp.Error(c, http.StatusBadRequest, "reward extractor code is required")
		return
	}
	var reward string
	var err error
	if len(request.StatusResponse) > 0 {
		reward, err = sitesync.ExtractCheckinRewardResponses(c.Request.Context(), request.Code, request.Response, request.StatusResponse)
	} else {
		reward, err = sitesync.ExtractCheckinReward(c.Request.Context(), request.Code, request.Response)
	}
	if err != nil {
		resp.ErrorWithAppError(c, http.StatusBadRequest, err)
		return
	}
	resp.Success(c, struct {
		Reward string `json:"reward"`
		Found  bool   `json:"found"`
	}{Reward: reward, Found: reward != ""})
}

func testCheckinStatusRule(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	var request struct {
		Config     model.SiteCheckinStatusConfig `json:"config"`
		HTTPStatus int                           `json:"http_status"`
		Response   json.RawMessage               `json:"response"`
		Date       string                        `json:"date"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.InvalidJSON(c)
		return
	}
	state, err := sitesync.TestCheckinStatusRule(c.Request.Context(), &request.Config, request.HTTPStatus, request.Response, request.Date)
	if err != nil {
		resp.ErrorWithAppError(c, http.StatusBadRequest, err)
		return
	}
	resp.Success(c, gin.H{"state": state})
}
