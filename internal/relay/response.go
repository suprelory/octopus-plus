package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/polywire/httpio"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func protocolErrorFromAttempt(upstreamError *model.ResponseError, statusCode int, err error) *model.ResponseError {
	if upstreamError != nil {
		return model.NormalizeResponseError(upstreamError, statusCode, "api_error")
	}
	var responseError *model.ResponseError
	if errors.As(err, &responseError) {
		return model.NormalizeResponseError(responseError, statusCode, "api_error")
	}
	if statusCode <= 0 {
		return nil
	}
	return protocolErrorFromError(statusCode, err)
}

// A successful HTTP status is not a successful relay attempt when response
// processing fails. Status 0 feeds the error into the existing retry/failover
// path; non-2xx statuses retain their original retry semantics.
func responseProcessingErrorStatus(status int) int {
	if status >= 200 && status < 300 {
		return 0
	}
	return status
}

// handleResponsePassthrough handles non-streaming passthrough responses.
func (ra *relayAttempt) handleResponsePassthrough(ctx context.Context, response *http.Response, cfg model.PassthroughConfig) error {
	body, err := httpio.ReadResponseBody(response.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	// Semantic precommit: validate and normalize the complete response before
	// writing the byte-stable passthrough body downstream.
	sidecarResp := &http.Response{
		StatusCode: response.StatusCode,
		Header:     response.Header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	internalResponse, err := ra.outAdapter.TransformResponse(ctx, sidecarResp)
	if err != nil {
		return fmt.Errorf("failed to observe passthrough response: %w", err)
	}
	if internalResponse == nil {
		return fmt.Errorf("failed to observe passthrough response: empty canonical response")
	}
	if ra.emptyResponseDetection {
		if err := validateNonStreamResponse(internalResponse); err != nil {
			return err
		}
	}
	if _, err := ra.inAdapter.TransformResponse(ctx, internalResponse); err != nil {
		return fmt.Errorf("failed to validate passthrough response: %w", err)
	}
	if ctx != nil && ctx.Err() != nil {
		return contextError(ctx)
	}
	ra.collectResponse()

	ra.heartbeat.Stop()
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	ra.commitResponse()
	ra.c.Data(http.StatusOK, contentType, body)

	return nil
}

// handleResponse 处理非流式响应
func (ra *relayAttempt) handleResponse(ctx context.Context, response *http.Response) error {
	internalResponse, err := ra.outAdapter.TransformResponse(ctx, response)
	if err != nil {
		log.Warnf("failed to transform response: %v", relayErrorDiagnostic(err))
		return fmt.Errorf("failed to transform outbound response: %w", err)
	}

	// Empty response detection for non-streaming responses
	if ra.emptyResponseDetection {
		if err := validateNonStreamResponse(internalResponse); err != nil {
			log.Warnf("empty non-stream response detected from channel %s (model=%s)",
				ra.channel.Name, ra.internalRequest.Model)
			return err
		}
	}

	inResponse, err := ra.inAdapter.TransformResponse(ctx, internalResponse)
	if err != nil {
		log.Warnf("failed to transform response: %v", relayErrorDiagnostic(err))
		return fmt.Errorf("failed to transform inbound response: %w", err)
	}
	if ctx != nil && ctx.Err() != nil {
		return contextError(ctx)
	}

	ra.heartbeat.Stop()
	ra.commitResponse()
	ra.c.Data(http.StatusOK, "application/json", inResponse)
	return nil
}

// collectResponse 收集响应信息
func (ra *relayAttempt) collectResponse() {
	if ra == nil || ra.inAdapter == nil || ra.metrics == nil {
		return
	}
	if !ra.responseCollected.CompareAndSwap(false, true) {
		return
	}
	var internalResponse *model.InternalLLMResponse
	var err error
	if ra.streamConverter != nil {
		internalResponse = ra.streamConverter.Response()
	} else {
		internalResponse, err = ra.inAdapter.GetInternalResponse(ra.requestContext())
	}
	if err != nil {
		log.Debugf("collectResponse: failed to get internal response: %v", relayErrorDiagnostic(err))
		return
	}
	if internalResponse == nil {
		log.Debugf("collectResponse: internal response is nil (stream may not be complete)")
		return
	}

	actualModel := strings.TrimSpace(internalResponse.Model)
	if actualModel == "" && ra.internalRequest != nil {
		actualModel = strings.TrimSpace(ra.internalRequest.Model)
	}
	ra.metrics.SetInternalResponse(internalResponse, actualModel)
}

// writeFinalError preserves upstream Retry-After after all candidates fail.
func (r *httpRelay) writeFinalError(result attemptResult, err error, lastAttempt *relayRequest) {
	req := r.request
	req.heartbeat.Hand()
	errorAdapter := req.inAdapter
	if lastAttempt != nil {
		errorAdapter = lastAttempt.inAdapter
	}
	if result.Failure.Passthrough || isPassthroughStatus(result.StatusCode) {
		if value := retryAfterHeaderValue(result.RetryAt, time.Now()); value != "" {
			req.c.Header("Retry-After", value)
		}
	}
	writeInboundProtocolError(req.c, req.heartbeat, errorAdapter, protocolErrorForAttempt(result, err))
}
