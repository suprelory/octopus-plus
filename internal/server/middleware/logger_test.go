package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func captureRequestLogs(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	core, entries := observer.New(zap.DebugLevel)
	previous := log.Logger
	log.Logger = zap.New(core).Sugar()
	t.Cleanup(func() { log.Logger = previous })
	return entries
}

func TestRequestLoggerRecordsWrappedFailureOnce(t *testing.T) {
	entries := captureRequestLogs(t)
	router := gin.New()
	router.Use(Logger(LoggerConfig{}))
	router.POST("/api/v1/channel/update", func(c *gin.Context) {
		c.Set(adminAuthenticatedKey, true)
		resp.ErrorWithAppError(c, 500, apperror.Wrap("channel.update_failed", "channel update failed", errors.New("database unavailable password=private-password")))
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/channel/update", nil))
	if recorder.Code != 500 || entries.Len() != 1 {
		t.Fatalf("status=%d logs=%d", recorder.Code, entries.Len())
	}
	entry := entries.All()[0]
	fields := entry.ContextMap()
	if entry.Level != zap.ErrorLevel || entry.Message != "admin.operation" || fields["error_code"] != "channel.update_failed" {
		t.Fatalf("missing failure context: %+v", entry)
	}
	if text := fmt.Sprint(fields["error"]); !strings.Contains(text, "database unavailable") || strings.Contains(text, "private-password") {
		t.Fatalf("unsafe or missing cause: %s", text)
	}
	if strings.Contains(recorder.Body.String(), "database unavailable") {
		t.Fatal("internal cause leaked to response")
	}
}

func TestAuditRecordsZeroValueChangesWithoutRequestValues(t *testing.T) {
	entries := captureRequestLogs(t)
	router := gin.New()
	router.Use(Logger(LoggerConfig{}))
	router.POST("/api/v1/site/account/update", func(c *gin.Context) {
		c.Set(adminAuthenticatedKey, true)
		var request struct {
			ID       int     `json:"id"`
			Enabled  *bool   `json:"enabled"`
			Password *string `json:"password"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			t.Fatal(err)
		}
		AuditChanges(c, request.ID, &request)
		resp.Success(c, nil)
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/site/account/update", strings.NewReader(`{"id":42,"enabled":false,"password":"opaque-private-value"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(httptest.NewRecorder(), request)
	if entries.Len() != 1 {
		t.Fatalf("logs=%d", entries.Len())
	}
	fields := entries.All()[0].ContextMap()
	if fields["resource_id"] != int64(42) || fmt.Sprint(fields["changed_fields"]) != "[enabled password]" || strings.Contains(fmt.Sprint(fields), "opaque-private-value") {
		t.Fatalf("unexpected audit metadata: %+v", fields)
	}
}

func TestLoggerKeepsPollingQuietAndRecordsBusinessTestFailure(t *testing.T) {
	entries := captureRequestLogs(t)
	router := gin.New()
	router.Use(Logger(LoggerConfig{}))
	router.Use(func(c *gin.Context) { c.Set(adminAuthenticatedKey, true); c.Next() })
	router.GET("/api/v1/site/list", func(c *gin.Context) { resp.Success(c, nil) })
	router.POST("/api/v1/proxy-pool/test", func(c *gin.Context) {
		AuditResult(c, false, "reason", "connection refused")
		resp.Success(c, nil)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/site/list", nil))
	if entries.Len() != 0 {
		t.Fatal("polling produced an access log")
	}
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/proxy-pool/test", nil))
	if entries.Len() != 1 || entries.All()[0].Level != zap.WarnLevel || entries.All()[0].ContextMap()["success"] != false {
		t.Fatalf("missing business failure: %+v", entries.All())
	}
}

func TestAuthLogLimiterCountsSuppressedRequestsAndBoundsMemory(t *testing.T) {
	limiter := authLogLimiter{windows: make(map[string]authLogWindow)}
	now := time.Now()
	if allowed, _ := limiter.allow("same-source", now); !allowed {
		t.Fatal("first rejection was suppressed")
	}
	for i := 0; i < 5; i++ {
		if allowed, _ := limiter.allow("same-source", now); allowed {
			t.Fatal("repeated rejection was logged")
		}
	}
	if allowed, suppressed := limiter.allow("same-source", now.Add(time.Minute)); !allowed || suppressed != 5 {
		t.Fatalf("allowed=%t suppressed=%d", allowed, suppressed)
	}
	for i := 0; i < 2000; i++ {
		limiter.allow(fmt.Sprint(i), now)
	}
	if len(limiter.windows) > 1024 {
		t.Fatalf("unbounded source map: %d", len(limiter.windows))
	}
}
