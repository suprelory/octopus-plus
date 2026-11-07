package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/server/router"
)

func init() {
	router.NewGroupRouter("/api/v1/group/auto-group").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(router.NewRoute("/config", http.MethodGet).Handle(getGroupAutoGroupConfig)).
		AddRoute(router.NewRoute("/config", http.MethodPut).Handle(updateGroupAutoGroupConfig)).
		AddRoute(router.NewRoute("/run", http.MethodPost).Handle(runGroupAutoGroup))
}

func getGroupAutoGroupConfig(c *gin.Context) {
	config, err := op.GroupAutoGroupConfigGet(c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, config)
}

func updateGroupAutoGroupConfig(c *gin.Context) {
	var req model.GroupAutoGroupConfigUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	config, err := op.GroupAutoGroupConfigUpdate(&req, c.Request.Context())
	if err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, err)
		return
	}
	resp.Success(c, config)
}

func runGroupAutoGroup(c *gin.Context) {
	var req model.GroupAutoGroupRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if err := op.RunGroupAutoGroup(req.ChannelIDs, c.Request.Context()); err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, err)
		return
	}
	resp.Success(c, nil)
}
