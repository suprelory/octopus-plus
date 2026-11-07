package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/suprelory/octopus-plus/internal/helper"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

// maxUpstreamErrorBodySize bounds provider-controlled error responses. Some
// providers echo the entire request in validation errors, which can otherwise
// turn a single failed request into an unbounded allocation and oversized log.
const maxUpstreamErrorBodySize = 1 << 20 // 1 MiB

func readUpstreamErrorBody(reader io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(reader, maxUpstreamErrorBodySize))
}

// forwardViaHTTP forwards the request using traditional HTTP.
func (ra *relayAttempt) forwardViaHTTP(ctx context.Context) (int, error) {
	// Check for passthrough capability using interface
	if pt, ok := ra.outAdapter.(model.PassthroughCapable); ok && ra.shouldUseHTTPPassthrough(pt) {
		// Additional checks for OpenAI Responses edge cases
		if ra.internalRequest.RawAPIFormat == model.APIFormatOpenAIResponse {
			if ra.c == nil || ra.internalRequest.IsOpenAIExactReplayRequest() || requiresUpstreamWSContinuation(ra.internalRequest) {
				// Fall through to standard path
			} else {
				return ra.forwardViaHTTPPassthrough(ctx, pt)
			}
		} else {
			return ra.forwardViaHTTPPassthrough(ctx, pt)
		}
	}

	return ra.forwardViaHTTPStandard(ctx)
}

func (ra *relayAttempt) shouldUseHTTPPassthrough(pt model.PassthroughCapable) bool {
	if ra == nil || pt == nil || ra.channel == nil || ra.internalRequest == nil || len(ra.rawBody) == 0 {
		return false
	}
	if channelParamOverrideActive(ra.channel) {
		return false
	}
	if !pt.CanPassthrough(ra.internalRequest.RawAPIFormat) {
		return false
	}
	return ra.channel.AllowsPassthrough() || ra.internalRequest.HasOpenAIResponsesPassthrough()
}

// forwardViaHTTPPassthrough handles unified passthrough for any PassthroughCapable transformer.
func (ra *relayAttempt) forwardViaHTTPPassthrough(ctx context.Context, pt model.PassthroughCapable) (int, error) {
	// Build request via TransformRequestRaw
	outboundRequest, err := pt.TransformRequestRaw(
		ctx,
		ra.rawBody,
		ra.internalRequest.Model,
		ra.channel.GetBaseUrl(),
		ra.usedKey.ChannelKey,
		ra.internalRequest.Query,
	)
	if err != nil {
		log.Warnf("failed to create passthrough request: %v", relayErrorDiagnostic(err))
		return 0, classifyLocalRelayError(FailureConfiguration, fmt.Errorf("failed to create request: %w", err))
	}

	// Apply param overrides
	if err := ra.applyParamOverride(outboundRequest); err != nil {
		return 0, err
	}

	// Copy headers
	ra.copyHeaders(outboundRequest)
	if ra.channel.Type == outbound.OutboundTypeOpenAIResponse {
		outboundRequest.Header.Set("Content-Type", "application/json")
	}

	// Send request
	response, err := ra.sendRequest(outboundRequest)
	if err != nil {
		return 0, fmt.Errorf("failed to send request: %w", err)
	}
	defer response.Body.Close()

	// Check status
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		ra.captureRetryAfter(response.Header.Get("Retry-After"))
		body, _ := readUpstreamErrorBody(response.Body)
		statusCode := normalizeUpstreamStatusCode(response.StatusCode, string(body))
		ra.upstreamError = ra.outAdapter.TransformError(ctx, statusCode, response.Header, body)
		if ra.upstreamError == nil {
			ra.upstreamError = model.NormalizeHTTPError(statusCode, response.Header, body, "api_error")
		}
		ra.logUpstreamHTTPError(response.StatusCode)
		return statusCode, ra.upstreamError
	}

	// Get passthrough config
	cfg := pt.PassthroughConfig()

	// Branch: streaming vs non-streaming
	if ra.internalRequest.Stream != nil && *ra.internalRequest.Stream {
		if err := ra.handleStreamResponsePassthroughV2(ctx, response, cfg); err != nil {
			return 0, err
		}
		return response.StatusCode, nil
	}
	if err := ra.handleResponsePassthrough(ctx, response, cfg); err != nil {
		return responseProcessingErrorStatus(response.StatusCode), err
	}
	return response.StatusCode, nil
}

// forwardViaHTTPStandard 是 forwardViaHTTP 的原路径（直通判定失败时的兜底）。
// 留作显式出口，避免 passthrough 失败时的递归。
func (ra *relayAttempt) forwardViaHTTPStandard(ctx context.Context) (int, error) {
	outboundRequest, report, err := ra.buildOutboundRequest(ctx)
	if err != nil {
		log.Warnf("failed to create request: %v", relayErrorDiagnostic(err))
		return 0, classifyLocalRelayError(FailureConfiguration, fmt.Errorf("failed to create request: %w", err))
	}
	ra.capabilityDecision = outbound.ApplyConversionReport(ra.capabilityDecision, report)
	if reject, code := evaluateCapabilityPolicy(ra.capabilityDecision, ra.capabilityPolicy); reject {
		outboundRequest.Body.Close()
		return http.StatusBadRequest, classifyLocalRelayError(FailureConfiguration, fmt.Errorf("%s: %s", code, ra.capabilityDecision.Summary()))
	}
	if err := ra.applyParamOverride(outboundRequest); err != nil {
		return 0, err
	}

	// 复制请求头
	ra.copyHeaders(outboundRequest)
	if ra.channel.Type == outbound.OutboundTypeOpenAIResponse {
		outboundRequest.Header.Set("Content-Type", "application/json")
	}

	// 发送请求
	response, err := ra.sendRequest(outboundRequest)
	if err != nil {
		return 0, fmt.Errorf("failed to send request: %w", err)
	}
	defer response.Body.Close()

	// 检查响应状态
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		ra.captureRetryAfter(response.Header.Get("Retry-After"))
		body, err := readUpstreamErrorBody(response.Body)
		if err != nil {
			return response.StatusCode, fmt.Errorf("failed to read response body: %w", err)
		}
		statusCode := normalizeUpstreamStatusCode(response.StatusCode, string(body))
		ra.upstreamError = ra.outAdapter.TransformError(ctx, statusCode, response.Header, body)
		if ra.upstreamError == nil {
			ra.upstreamError = model.NormalizeHTTPError(statusCode, response.Header, body, "api_error")
		}
		ra.logUpstreamHTTPError(response.StatusCode)
		return statusCode, ra.upstreamError
	}

	// 处理响应
	if ra.internalRequest.Stream != nil && *ra.internalRequest.Stream {
		// Use V2 StreamProcessor-based implementation
		if err := ra.handleStreamResponseV2(ctx, response); err != nil {
			return 0, err
		}
		return response.StatusCode, nil
	}
	if err := ra.handleResponse(ctx, response); err != nil {
		return 0, err
	}
	return response.StatusCode, nil
}

// sendRequest 发送 HTTP 请求
func (ra *relayAttempt) sendRequest(req *http.Request) (*http.Response, error) {
	httpClient, err := helper.ChannelHTTPClientWithContext(req.Context(), ra.channel)
	if err != nil {
		log.Warnf("failed to get http client: %v", relayErrorDiagnostic(err))
		return nil, classifyLocalRelayError(FailureConfiguration, err)
	}

	req = ra.attachFirstTokenBudget(req)
	if err := ra.reserveSubmission(req.Context()); err != nil {
		return nil, err
	}

	ra.upstreamTransport = "http"
	ra.metrics.capture.upstreamRequest(ra.capture, req)
	response, err := httpClient.Do(req)
	ra.metrics.capture.upstreamResponse(ra.capture, response)
	if err != nil {
		if timeoutErr := ra.firstTokenTimeoutIfNeeded(req.Context(), err); timeoutErr != nil {
			ra.closeFirstTokenBudget()
			return nil, timeoutErr
		}
		if isClientCancellation(req.Context(), err) {
			log.Infof("request canceled before upstream response: %v", relayErrorDiagnostic(err))
		} else {
			log.Warnf("failed to send request: %v", relayErrorDiagnostic(err))
		}
		ra.closeFirstTokenBudget()
		return nil, err
	}

	if response != nil && response.Body != nil && ra.firstTokenBudget != nil {
		response.Body = &closeWithFuncReadCloser{
			ReadCloser: response.Body,
			onClose:    ra.closeFirstTokenBudget,
		}
	}

	return response, nil
}
