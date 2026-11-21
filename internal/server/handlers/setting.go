package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/backup"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/server/router"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/utils/safe"
)

var projectedAutoGroupQueued atomic.Bool

func init() {
	router.NewGroupRouter("/api/v1/setting").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(getSettingList),
		).
		AddRoute(
			router.NewRoute("/set", http.MethodPost).
				Use(middleware.RequireJSON()).
				Handle(setSetting),
		).
		AddRoute(
			router.NewRoute("/export", http.MethodGet).
				Handle(exportDB),
		).
		AddRoute(
			router.NewRoute("/import", http.MethodPost).
				Handle(importDB),
		)
}

func getSettingList(c *gin.Context) {
	settings, err := op.SettingList(c.Request.Context())
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	resp.Success(c, settings)
}

func setSetting(c *gin.Context) {
	var setting model.Setting
	if err := c.ShouldBindJSON(&setting); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := setting.Validate(); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	middleware.AuditFields(c, "setting_key", string(setting.Key))
	if err := op.SettingSetString(setting.Key, setting.Value); err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}
	switch setting.Key {
	case model.SettingKeyProjectedChannelAutoGroupEnabled:
		mode, _ := model.ParseAutoGroupSettingValue(setting.Value)
		if mode != model.AutoGroupTypeNone && projectedAutoGroupQueued.CompareAndSwap(false, true) {
			safe.Go("projected-channel-auto-group-all", func() {
				defer projectedAutoGroupQueued.Store(false)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				if err := op.AutoGroupAllProjectedChannels(ctx); err != nil {
					log.Warnf("failed to auto group all projected channels: %v", err)
				}
			})
		}
	}
	resp.Success(c, setting)
}

func exportDB(c *gin.Context) {
	version, ok := backupVersion(c)
	if !ok {
		return
	}
	includeLogs, _ := strconv.ParseBool(c.DefaultQuery("include_logs", "false"))
	includeStats, _ := strconv.ParseBool(c.DefaultQuery("include_stats", "false"))
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "json")))
	middleware.AuditFields(c, "format", format, "include_logs", includeLogs, "include_stats", includeStats)
	if format != "json" && format != "zip" {
		resp.Error(c, http.StatusBadRequest, "invalid format")
		return
	}

	if format == "zip" {
		filename := "octopus-export-" + time.Now().Format("20060102150405") + ".zip"
		c.Header("Content-Type", "application/zip")
		c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
		wrapper := &countingResponseWriter{ResponseWriter: c.Writer}
		export := op.DBExportZip
		if version == 2 {
			export = backup.NewService(db.GetDB()).ExportZIP
		}
		if err := export(c.Request.Context(), wrapper, includeLogs, includeStats); err != nil {
			if wrapper.bytesWritten == 0 {
				c.Header("Content-Type", "application/json")
				c.Header("Content-Disposition", "")
				resp.InternalErrorWithLog(c, err)
				return
			}
			// Headers already sent; we can't switch to a JSON error. Log it and
			// let the client surface the truncated download.
			middleware.AuditResult(c, false, "error", log.SafeError(err), "bytes_written", wrapper.bytesWritten)
		}
		return
	}

	var dump any
	var err error
	if version == 2 {
		dump, err = backup.NewService(db.GetDB()).Export(c.Request.Context(), includeLogs, includeStats)
	} else {
		dump, err = op.DBExportAll(c.Request.Context(), includeLogs, includeStats)
	}
	if err != nil {
		resp.InternalErrorWithLog(c, err)
		return
	}

	c.Header("Content-Type", "application/json")
	c.Header("Content-Disposition", "attachment; filename=\"octopus-export-"+time.Now().Format("20060102150405")+".json\"")
	c.JSON(http.StatusOK, dump)
}

func importDB(c *gin.Context) {
	version, ok := backupVersion(c)
	if !ok {
		return
	}
	var reader io.Reader = c.Request.Body

	contentType := c.GetHeader("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		fh, err := c.FormFile("file")
		if err != nil {
			resp.Error(c, http.StatusBadRequest, "missing upload file field 'file'")
			return
		}
		f, err := fh.Open()
		if err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		defer f.Close()
		defer c.Request.MultipartForm.RemoveAll()
		reader = f
	}
	document, err := backup.Read(c.Request.Context(), reader)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	defer document.Close()
	var result *model.DBImportResult
	if version == 2 {
		result, err = backup.NewService(db.GetDB()).Import(c.Request.Context(), document)
	} else {
		var dump *model.DBDump
		dump, err = document.LegacyDump(c.Request.Context())
		if err == nil {
			result, err = op.DBImportIncremental(c.Request.Context(), dump)
		}
	}
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	middleware.AuditFields(c, "rows_affected", result.RowsAffected)

	if err := op.RefreshCacheAfterImport(); err != nil {
		resp.InternalErrorWithLog(c, fmt.Errorf("database imported but runtime refresh failed: %w", err))
		return
	}

	resp.Success(c, result)
}

// The v2 UI opts into the new service until P5.4 switches production defaults.
func backupVersion(c *gin.Context) (int, bool) {
	version := c.DefaultQuery("version", "1")
	if version == "1" {
		return 1, true
	}
	if version == "2" {
		return 2, true
	}
	resp.Error(c, http.StatusBadRequest, "unsupported backup version")
	return 0, false
}

// countingResponseWriter wraps gin.ResponseWriter to track whether the body
// has started, so callers can choose to emit a JSON error if the underlying
// stream failed before any bytes were committed.
type countingResponseWriter struct {
	gin.ResponseWriter
	bytesWritten int64
}

func (w *countingResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += int64(n)
	return n, err
}

func (w *countingResponseWriter) WriteString(s string) (int, error) {
	n, err := w.ResponseWriter.WriteString(s)
	w.bytesWritten += int64(n)
	return n, err
}
