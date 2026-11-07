package handlers

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/client"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/server/router"
)

func init() {
	router.NewGroupRouter("/api/v1/proxy-pool").
		Use(middleware.Auth()).
		AddRoute(router.NewRoute("/list", http.MethodGet).Handle(listProxyConfigurations)).
		AddRoute(router.NewRoute("/references/:id", http.MethodGet).Handle(listProxyConfigurationReferences)).
		AddRoute(router.NewRoute("/delete/:id", http.MethodDelete).Handle(deleteProxyConfiguration))

	router.NewGroupRouter("/api/v1/proxy-pool").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(router.NewRoute("/create", http.MethodPost).Handle(createProxyConfiguration)).
		AddRoute(router.NewRoute("/update", http.MethodPost).Handle(updateProxyConfiguration)).
		AddRoute(router.NewRoute("/test", http.MethodPost).Handle(testProxyConfiguration))
}

func listProxyConfigurations(c *gin.Context) {
	items, err := op.ProxyConfigurationList(c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, items)
}

func listProxyConfigurationReferences(c *gin.Context) {
	idNum, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.InvalidParam(c)
		return
	}
	items, err := op.ProxyConfigurationReferences(idNum, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	resp.Success(c, items)
}

func createProxyConfiguration(c *gin.Context) {
	type proxyConfigurationCreateRequest struct {
		Name    string `json:"name" binding:"required"`
		URL     string `json:"url" binding:"required"`
		Enabled *bool  `json:"enabled,omitempty"`
		Remark  string `json:"remark,omitempty"`
	}

	var req proxyConfigurationCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	item := model.ProxyConfiguration{
		Name:    req.Name,
		URL:     req.URL,
		Enabled: enabled,
		Remark:  req.Remark,
	}
	middleware.AuditChanges(c, 0, &req)
	if err := op.ProxyConfigurationCreate(&item, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	resp.Success(c, item)
}

func updateProxyConfiguration(c *gin.Context) {
	var req model.ProxyConfigurationUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	middleware.AuditChanges(c, req.ID, &req)
	existing, err := op.ProxyConfigurationGet(req.ID, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	item, err := op.ProxyConfigurationUpdate(&req, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	// Drop the cached client for a URL that is no longer configured so its
	// idle connections close instead of lingering on the old proxy.
	if existing != nil && existing.URL != item.URL {
		client.InvalidateProxyClient(existing.URL)
	}
	resp.Success(c, item)
}

func deleteProxyConfiguration(c *gin.Context) {
	idNum, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.InvalidParam(c)
		return
	}
	existing, err := op.ProxyConfigurationGet(idNum, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := op.ProxyConfigurationDelete(idNum, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	client.InvalidateProxyClient(existing.URL)
	resp.Success(c, nil)
}

func testProxyConfiguration(c *gin.Context) {
	var req model.ProxyTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if req.ProxyConfigID != nil {
		middleware.AuditFields(c, "proxy_config_id", *req.ProxyConfigID)
	}
	if target, err := url.Parse(req.URL); err == nil && target.Hostname() != "" {
		middleware.AuditFields(c, "target_host", target.Hostname())
	}
	if proxy, err := url.Parse(req.ProxyURL); err == nil && proxy.Hostname() != "" {
		middleware.AuditFields(c, "proxy_host", proxy.Hostname())
	}
	result, err := op.ProxyConfigurationTest(req, c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	middleware.AuditResult(c, result.Success, "reason", result.Message, "test_duration_ms", result.DurationMS, "upstream_status", result.StatusCode)
	resp.Success(c, result)
}
