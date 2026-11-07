package relay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func relayProtocolError(status int, code, message string) *model.ResponseError {
	errorType := "api_error"
	if status >= 400 && status < 500 {
		errorType = "invalid_request_error"
	}
	return &model.ResponseError{
		StatusCode: status,
		Detail: model.ErrorDetail{
			Code:    code,
			Message: message,
			Type:    errorType,
		},
	}
}

func protocolErrorFromError(status int, err error) *model.ResponseError {
	var responseError *model.ResponseError
	if errors.As(err, &responseError) && responseError != nil {
		return model.NormalizeResponseError(responseError, status, "api_error")
	}
	message := http.StatusText(status)
	if err != nil && err.Error() != "" {
		message = err.Error()
	}
	return relayProtocolError(status, CodeRelayUpstreamFailed, message)
}

func protocolErrorForAttempt(result attemptResult, err error) *model.ResponseError {
	var budgetErr *costBudgetError
	if errors.As(err, &budgetErr) {
		return budgetErr.response
	}
	if result.Failure.Class == FailureConfiguration {
		message := "relay configuration is invalid"
		if err != nil && strings.TrimSpace(err.Error()) != "" {
			message = err.Error()
		}
		return relayProtocolError(http.StatusInternalServerError, CodeRelayConfiguration, message)
	}
	if result.Failure.Class == FailureBudgetExceeded {
		return relayProtocolError(http.StatusGatewayTimeout, CodeRelayTimeout, "relay failover budget exceeded")
	}
	if isUpstreamStreamInterrupted(err) {
		status, _ := defaultFailureProtocol(FailureTransient, result.StatusCode)
		message := "upstream stream ended before completion"
		if err != nil && strings.TrimSpace(err.Error()) != "" {
			message = err.Error()
		}
		return relayProtocolError(status, CodeRelayUpstreamStreamInterrupted, message)
	}
	if result.ProtocolError != nil {
		return model.NormalizeResponseError(result.ProtocolError, result.StatusCode, "api_error")
	}

	status, code := defaultFailureProtocol(result.Failure.Class, result.StatusCode)
	if result.Failure.Class != FailureNone {
		message := http.StatusText(status)
		if err != nil && strings.TrimSpace(err.Error()) != "" {
			message = err.Error()
		}
		return relayProtocolError(status, code, message)
	}
	return result.ProtocolError
}

func isUpstreamStreamInterrupted(err error) bool {
	return errors.Is(err, model.ErrStreamIncomplete) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

func defaultFailureProtocol(class FailureClass, status int) (int, string) {
	if class == FailureBudgetExceeded {
		status = http.StatusGatewayTimeout
	} else if status < 400 || status > 599 {
		switch class {
		case FailureRequest:
			status = http.StatusBadRequest
		case FailureConfiguration, FailureTransient:
			status = http.StatusBadGateway
		case FailureAuthentication:
			status = http.StatusUnauthorized
		case FailurePermission:
			status = http.StatusForbidden
		case FailureQuota, FailureRateLimit:
			status = http.StatusTooManyRequests
		case FailureModelUnsupported:
			status = http.StatusBadRequest
		default:
			status = http.StatusBadGateway
		}
	}
	code := CodeRelayUpstreamFailed
	switch class {
	case FailureConfiguration:
		code = CodeRelayConfiguration
	case FailureRequest:
		code = CodeRelayInvalidRequest
	case FailureAuthentication:
		code = CodeRelayAuthentication
	case FailurePermission:
		code = CodeRelayPermission
	case FailureQuota:
		code = CodeRelayQuota
	case FailureRateLimit:
		code = CodeRelayRateLimit
	case FailureModelUnsupported:
		code = CodeRelayModelNotSupported
	case FailureBudgetExceeded:
		code = CodeRelayTimeout
	}
	return status, code
}

func writeInboundProtocolError(c *gin.Context, heartbeat *earlyHeartbeat, inbound model.Inbound, responseError *model.ResponseError) {
	if c == nil || inbound == nil {
		return
	}
	normalized := model.NormalizeResponseError(responseError, http.StatusBadGateway, "api_error")
	resp.RecordProtocolError(c, normalized.Detail.Code, normalized.Detail.Message)
	transformed, err := inbound.TransformError(c.Request.Context(), normalized)
	if err != nil || transformed == nil || len(transformed.Body) == 0 {
		if heartbeat != nil && heartbeat.Handoff() {
			heartbeat.WriteSSEError(normalized.StatusCode, normalized.Detail.Message)
			return
		}
		resp.ErrorWithCode(c, normalized.StatusCode, normalized.Detail.Code, normalized.Detail.Message)
		return
	}

	if heartbeat != nil && heartbeat.Handoff() {
		body := transformed.StreamBody
		if len(body) == 0 {
			body = transformed.Body
		}
		heartbeat.mu.Lock()
		defer heartbeat.mu.Unlock()
		_, _ = c.Writer.Write(body)
		c.Writer.Flush()
		return
	}

	for key, values := range transformed.Headers {
		for _, value := range values {
			c.Header(key, value)
		}
	}
	c.Data(transformed.StatusCode, transformed.Headers.Get("Content-Type"), transformed.Body)
}

func writeStreamProtocolError(ctx context.Context, writer StreamWriter, inbound model.Inbound, responseError *model.ResponseError) {
	if writer == nil || inbound == nil || responseError == nil {
		return
	}
	transformed, err := inbound.TransformError(ctx, responseError)
	if err != nil || transformed == nil || len(transformed.StreamBody) == 0 {
		return
	}
	_, _ = writer.Write(transformed.StreamBody)
	writer.Flush()
}
