package relay

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

// Early rejections have no upstream attempt or usage to settle. Record a small
// diagnostic entry without retaining an invalid/unauthorized request body or
// changing billing, channel health, or retry statistics.
func recordRelayRejection(ctx context.Context, apiKeyID int, requestModel, endpoint, clientIP string, status int, code, message string, started time.Time, ws bool) {
	duration := time.Since(started).Milliseconds()
	requestModel, message = log.SafeText(requestModel), log.SafeText(message)
	log.Warnw("relay.rejected", "api_key_id", apiKeyID, "model", requestModel,
		"endpoint", endpoint, "ip", clientIP, "status", status, "reason", code,
		"message", message, "duration_ms", duration, "ws", ws)
	if apiKeyID <= 0 {
		return
	}
	entry := model.RelayLog{
		Trace: captureFromContext(ctx).snapshot(ctx),
		Time:  started.Unix(), RequestModelName: requestModel, ActualModelName: requestModel,
		RequestAPIKeyID: apiKeyID, ClientIP: clientIP, EndpointType: endpoint,
		UseTime: int(duration), Success: false, UsedWS: ws,
		Error: fmt.Sprintf("%s (HTTP %d): %s", code, status, message),
	}
	if entry.Trace != nil && entry.Trace.Client.Request != nil {
		request := entry.Trace.Client.Request
		request.Data, request.Body, request.CapturedBytes = nil, "", 0
		request.Events = nil
		request.State, request.Reason = "not_captured", "rejected_request"
	}
	if key, err := op.APIKeyGet(apiKeyID, ctx); err == nil {
		entry.RequestAPIKeyName = key.Name
	}
	if err := op.RelayLogAdd(entry); err != nil {
		log.Warnw("relay.rejection_log_failed", "api_key_id", apiKeyID, "error", log.SafeError(err))
	}
}

func recordEarlyHTTPFailure(c *gin.Context, requestModel *string, endpoint string, ready *bool, started time.Time) {
	if *ready || c.Writer.Status() < http.StatusBadRequest {
		return
	}
	details := resp.RequestError(c)
	if details.Code == "" {
		details.Code = CodeRelayInvalidRequest
	}
	message := details.Message
	if details.Cause != "" {
		message = details.Cause
	}
	if message == "" {
		message = http.StatusText(c.Writer.Status())
	}
	recordRelayRejection(c.Request.Context(), c.GetInt("api_key_id"), *requestModel, endpoint,
		middleware.ClientIP(c), c.Writer.Status(), details.Code, message, started, false)
	c.Set(middleware.RequestObservedKey, true)
}
