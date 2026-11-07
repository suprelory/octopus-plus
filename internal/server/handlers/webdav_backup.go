package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/server/router"
	"github.com/suprelory/octopus-plus/internal/webdav"
)

func init() {
	router.NewGroupRouter("/api/v1/webdav-backup").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/test", http.MethodPost).
				Handle(testWebDAVConnection),
		).
		AddRoute(
			router.NewRoute("/trigger", http.MethodPost).
				Handle(triggerWebDAVBackup),
		).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(listWebDAVBackups),
		).
		AddRoute(
			router.NewRoute("/restore", http.MethodPost).
				Use(middleware.RequireJSON()).
				Handle(restoreWebDAVBackup),
		)
}

func testWebDAVConnection(c *gin.Context) {
	cfg, err := webdav.LoadConfig()
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	if err := webdav.TestConnection(cfg); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	resp.Success(c, gin.H{"ok": true})
}

func triggerWebDAVBackup(c *gin.Context) {
	if err := webdav.RunBackup(c.Request.Context()); err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}

	resp.Success(c, gin.H{"message": "backup completed"})
}

func listWebDAVBackups(c *gin.Context) {
	backups, err := webdav.ListBackups(c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}

	resp.Success(c, backups)
}

func restoreWebDAVBackup(c *gin.Context) {
	var req struct {
		Filename string `json:"filename" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	middleware.AuditFields(c, "filename", req.Filename)
	result, err := webdav.RestoreFromBackup(c.Request.Context(), req.Filename)
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}

	middleware.AuditFields(c, "rows_affected", result.RowsAffected)
	resp.Success(c, result)
}
