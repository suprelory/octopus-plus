package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/helper"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/internal/relay/capture"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/polywire/httpio"
	transformerModel "github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
	openaiOutbound "github.com/suprelory/octopus-plus/polywire/outbound/openai"
)

type responsesCompactRequest struct {
	Model              string          `json:"model"`
	Input              json.RawMessage `json:"input,omitempty"`
	PreviousResponseID *string         `json:"previous_response_id,omitempty"`
}

type responsesCompactResponse struct {
	ID        string                         `json:"id"`
	Object    string                         `json:"object"`
	CreatedAt int64                          `json:"created_at"`
	Output    []openaiOutbound.ResponsesItem `json:"output"`
	Usage     *openaiOutbound.ResponsesUsage `json:"usage,omitempty"`
	Error     *transformerModel.ErrorDetail  `json:"error,omitempty"`
}

// HandleResponsesCompact proxies OpenAI-compatible /responses/compact requests upstream.
func HandleResponsesCompact(c *gin.Context) {
	captureTrace := startHTTPRelayCapture(c)
	requestModel, ready := "", false
	defer recordEarlyHTTPFailure(c, &requestModel, "responses", &ready, time.Now())
	if op.APIKeyCostReservationFromContext(c.Request.Context()).Limited() {
		err := newCostBudgetError("compact does not support a reliable maximum cost estimate for cost-limited API keys")
		response := protocolErrorFromError(http.StatusBadRequest, err)
		resp.ErrorWithCode(c, response.StatusCode, response.Detail.Code, response.Detail.Message)
		return
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		// middleware.MaxBodySize 命中时要回 413，别把限流当成内部错误。
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			resp.ErrorWithCode(c, http.StatusRequestEntityTooLarge, CodeRelayRequestTooLarge, "request body too large")
			return
		}
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	var compactReq responsesCompactRequest
	if err := json.Unmarshal(body, &compactReq); err != nil {
		resp.Error(c, http.StatusBadRequest, fmt.Sprintf("failed to decode responses compact request: %v", err))
		return
	}
	requestModel = compactReq.Model
	if strings.TrimSpace(compactReq.Model) == "" {
		resp.Error(c, http.StatusBadRequest, "model is required")
		return
	}
	if len(compactReq.Input) == 0 && compactReq.PreviousResponseID == nil {
		resp.Error(c, http.StatusBadRequest, "either input or previous_response_id is required")
		return
	}

	supportedModels := c.GetString("supported_models")
	if supportedModels != "" {
		supportedModelsArray := strings.Split(supportedModels, ",")
		if !slices.Contains(supportedModelsArray, compactReq.Model) {
			resp.ErrorWithCode(c, http.StatusBadRequest, CodeRelayModelNotSupported, "model not supported")
			return
		}
	}

	apiKeyID := c.GetInt("api_key_id")

	group, err := op.GroupGetEnabledMap(requestModel, c.Request.Context())
	if err != nil {
		resp.ErrorWithCode(c, http.StatusNotFound, CodeRelayModelNotFound, "model not found")
		return
	}
	candidateSnapshot := newCandidateSnapshot(c.Request.Context(), group)

	iter := balancer.NewIterator(group, apiKeyID, requestModel, resolveRequestAffinity(c.Request.Header, body))
	if iter.Len() == 0 {
		resp.ErrorWithCode(c, http.StatusServiceUnavailable, CodeRelayNoAvailableChannel, "no available channel")
		return
	}
	defer iter.Close()

	metricsReq := &transformerModel.InternalLLMRequest{Model: requestModel, RawRequest: body}
	ready = true
	resp.UseSafeErrorLogDetails(c, CodeRelayUpstreamFailed, "relay response details omitted from console")
	metrics := NewRelayMetrics(apiKeyID, requestModel, "responses", middleware.ClientIP(c), body, metricsReq)
	metrics.capture, metrics.StartTime = captureTrace, captureTrace.started
	captureTrace.recordClientRequest(body)
	hb := startEarlyHeartbeat(c, false)
	defer hb.Stop()
	var savedSuccess bool
	var savedErr error
	defer func() {
		hb.Stop()
		metrics.SaveWithChannelStats(c.Request.Context(), savedSuccess, savedErr, iter.Attempts(), false)
	}()

	var lastErr error
	var capabilityErr error
	var sawSupportedCapability bool
	var lastStatusCode int
	var lastRetryAt time.Time
	var lastFailure FailureClassification
	var capabilityErrorCode string
	var capabilityErrorMessage string
	capabilityPolicy := getCapabilityDegradationPolicy()

	maxSameChannelAttempts := sameChannelMaxAttempts(group.RetryEnabled, group.MaxRetries)
	execution := newOperationExecution(group, "compact")
	metrics.execution, metrics.affinity = execution, iter.Affinity()
	execution.strictAffinity = iter.StrictAffinity()
	rateLimitedChannels := make(map[int]struct{})

	for iter.Next() {
		select {
		case <-c.Request.Context().Done():
			log.Infof("compact request context canceled, stopping retry")
			savedErr = context.Canceled
			return
		default:
		}
		if err := execution.attemptError(time.Now()); err != nil {
			lastErr, lastFailure = err, relayBudgetAttemptResult(err).Failure
			break
		}

		item := iter.Item()
		if _, rateLimited := rateLimitedChannels[item.ChannelID]; rateLimited {
			iter.Skip(item.ChannelID, 0, fmt.Sprintf("channel_%d", item.ChannelID), "channel rate limited earlier in this request")
			continue
		}
		channel, err := candidateSnapshot.Channel(item.ChannelID)
		if err != nil {
			iter.Skip(item.ChannelID, 0, fmt.Sprintf("channel_%d", item.ChannelID), fmt.Sprintf("channel not found: %v", err))
			lastErr = err
			continue
		}
		if !channel.Enabled {
			iter.Skip(channel.ID, 0, channel.Name, "channel disabled")
			continue
		}
		decision := outbound.PlanRelayOperation(channel.Type, outbound.RelayOperationResponsesCompact)
		decorateParamOverrideDecision(&decision, helper.InspectParamOverride(channel.ParamOverride), channelParamOverrideActive(channel))
		logRelayCapability(channel, item.ModelName, decision, capabilityPolicy)
		if reject, errorCode := evaluateCapabilityPolicy(decision, capabilityPolicy); reject {
			message := capabilityRejectionMessage(decision, channel.Type.String())
			iter.SkipWithCapability(channel.ID, 0, channel.Name, message, capabilityTrace(decision, capabilityPolicy, channel.Type.String()))
			capabilityErr = fmt.Errorf("capability rejected: %s", message)
			capabilityErrorCode = errorCode
			capabilityErrorMessage = message
			continue
		}
		sawSupportedCapability = true
		if !execution.canAttemptChannel(channel.ID, time.Now()) {
			iter.Skip(channel.ID, 0, channel.Name, "candidate channel budget exhausted")
			lastErr = newRelayBudgetError("candidate channel budget exhausted")
			lastFailure = relayBudgetAttemptResult(lastErr).Failure
			continue
		}

		excludedKeyIDs := make(map[int]struct{})
		usedKey, releaseKey := selectAndReserveRelayKey(iter, channel, excludedKeyIDs)
		if usedKey.ChannelKey == "" {
			if len(excludedKeyIDs) == 0 {
				iter.Skip(channel.ID, 0, channel.Name, "no available key")
			} else {
				iter.InvalidateCurrentPreference()
			}
			continue
		}

		for {
			var attemptErr error
			var statusCode int
			var retryAt time.Time
			var failure FailureClassification
			var success bool

			for attemptNum := 0; attemptNum < maxSameChannelAttempts; attemptNum++ {
				if attemptNum > 0 {
					delay := computeBackoffUntil(attemptNum, retryAt)
					if err := execution.wait(c.Request.Context(), delay); err != nil {
						attemptErr = err
						failure = classifyRelayFailureContext(c.Request.Context(), 0, err, time.Time{})
						break
					}
				}

				statusCode, retryAt, attemptErr = forwardResponsesCompactWithRetryAt(
					c,
					metrics,
					iter,
					channel,
					usedKey,
					item.ModelName,
					body,
					capabilityTrace(decision, capabilityPolicy, channel.Type.String()),
					execution,
					hb,
				)
				if attemptErr == nil {
					success = true
					break
				}
				failure = classifyRelayFailureContext(c.Request.Context(), statusCode, attemptErr, retryAt)
				if failure.Class == FailureRateLimit && iter.HasRemainingDifferentChannelExcept(channel.ID, rateLimitedChannels) {
					rateLimitedChannels[channel.ID] = struct{}{}
					break
				}
				if failure.Scope == "key" || !failure.Retryable {
					break
				}
			}
			releaseKey()

			usedKey.StatusCode = statusCode
			usedKey.LastUseTimeStamp = time.Now().Unix()
			op.ChannelKeyUpdateWithDelta(usedKey, 0)

			if success {
				op.StatsChannelUpdate(channel.ID, dbmodel.StatsMetrics{RequestSuccess: 1})
				balancer.RecordSuccess(channel.ID, usedKey.ID, item.ModelName)
				iter.RecordAffinity(channel.ID, usedKey.ID)
				savedSuccess = true
				return
			}

			op.StatsChannelUpdate(channel.ID, dbmodel.StatsMetrics{RequestFailed: 1})
			if failure.Record {
				retryAt = recordFailureAndResolveRetryAt(channel.ID, usedKey.ID, item.ModelName, failure, retryAt)
				failure.RetryAt = retryAt
			}
			iter.InvalidateCurrentPreference()
			lastErr = attemptErr
			lastStatusCode = statusCode
			lastRetryAt = retryAt
			lastFailure = failure
			if failure.Class == FailureBudgetExceeded || failure.Class == FailureClientCanceled {
				break
			}
			if failure.Scope == "key" && (compactReq.PreviousResponseID == nil || strings.TrimSpace(*compactReq.PreviousResponseID) == "") {
				excludedKeyIDs[usedKey.ID] = struct{}{}
				usedKey, releaseKey = selectAndReserveRelayKey(iter, channel, excludedKeyIDs)
				if usedKey.ChannelKey != "" {
					continue
				}
			}
			break
		}
		if lastFailure.Class == FailureBudgetExceeded || lastFailure.Class == FailureClientCanceled {
			break
		}
	}

	finalErr := lastErr
	if !sawSupportedCapability && capabilityErr != nil {
		finalErr = capabilityErr
	}
	savedErr = finalErr
	hb.Hand()
	if !sawSupportedCapability && lastStatusCode == 0 && capabilityErrorCode != "" {
		resp.ErrorWithCode(c, http.StatusBadRequest, capabilityErrorCode, capabilityErrorMessage)
		return
	}
	if lastErr == nil && lastStatusCode == 0 {
		resp.ErrorWithCode(c, http.StatusServiceUnavailable, CodeRelayNoAvailableChannel, "no available channel")
		return
	}
	finalResult := attemptResult{
		Err:        finalErr,
		StatusCode: lastStatusCode,
		RetryAt:    lastRetryAt,
		Failure:    lastFailure,
	}
	if lastFailure.Passthrough || isPassthroughStatus(lastStatusCode) {
		if value := retryAfterHeaderValue(lastRetryAt, time.Now()); value != "" {
			c.Header("Retry-After", value)
		}
		writeCompactFailure(c, finalResult, finalErr)
		return
	}
	writeCompactFailure(c, finalResult, finalErr)
}

func writeCompactFailure(c *gin.Context, result attemptResult, err error) {
	responseError := protocolErrorForAttempt(result, err)
	if responseError == nil {
		responseError = relayProtocolError(http.StatusBadGateway, CodeRelayUpstreamFailed, "channel failed")
	}
	resp.ErrorWithCode(c, responseError.StatusCode, responseError.Detail.Code, responseError.Detail.Message)
}

func forwardResponsesCompactWithRetryAt(c *gin.Context, metrics *RelayMetrics, iter *balancer.Iterator, channel *dbmodel.Channel, usedKey dbmodel.ChannelKey, mappedModel string, requestBody []byte, trace balancer.CapabilityTrace, execution *relayExecution, hb *earlyHeartbeat) (resultStatus int, resultRetry time.Time, resultErr error) {
	ctx, cancel := execution.budget.attemptContext(c.Request.Context())
	defer cancel()
	span := iter.StartAttempt(channel.ID, usedKey.ID, channel.Name)
	span.SetCapability(trace)
	exchange := metrics.capture.beginAttempt(channel, mappedModel, usedKey.ChannelKey)
	defer func() {
		if exchange != nil && resultErr != nil {
			exchange.info.Error = capture.Text(resultErr.Error(), exchange.secrets...)
		}
		if exchange != nil {
			exchange.snapshot()
		}
	}()
	if exchange != nil {
		span.SetHTTPDetails(exchange.info.AttemptID, nil, "")
	}
	requestBody, err := replaceRequiredJSONModel(requestBody, mappedModel)
	if err != nil {
		classified := classifyLocalRelayError(FailureConfiguration, fmt.Errorf("failed to apply compact model mapping: %w", err))
		span.SetFailure(string(FailureConfiguration), false, time.Time{})
		span.End(dbmodel.AttemptFailed, 0, classified.Error())
		return 0, time.Time{}, classified
	}
	request, err := buildResponsesCompactRequest(ctx, channel, usedKey.ChannelKey, requestBody)
	if err != nil {
		classified := classifyLocalRelayError(FailureConfiguration, fmt.Errorf("failed to create compact request: %w", err))
		span.SetFailure(string(FailureConfiguration), false, time.Time{})
		span.End(dbmodel.AttemptFailed, 0, classified.Error())
		return 0, time.Time{}, classified
	}
	requestBody, captured, err := helper.ApplyParamOverrideWithPayload(request, channel.ParamOverride)
	if err != nil {
		classified := classifyLocalRelayError(FailureConfiguration, fmt.Errorf("invalid channel param override: %w", err))
		span.SetFailure(string(FailureConfiguration), false, time.Time{})
		span.End(dbmodel.AttemptFailed, 0, classified.Error())
		return 0, time.Time{}, classified
	}
	if !captured {
		requestBody, err = readOutboundRequestBody(request)
		if err != nil {
			classified := classifyLocalRelayError(FailureConfiguration, fmt.Errorf("failed to inspect compact request: %w", err))
			span.SetFailure(string(FailureConfiguration), false, time.Time{})
			span.End(dbmodel.AttemptFailed, 0, classified.Error())
			return 0, time.Time{}, classified
		}
	}
	actualModel, err := requiredJSONModel(requestBody)
	if err != nil {
		classified := classifyLocalRelayError(FailureConfiguration, fmt.Errorf("channel param override produced an invalid compact request: %w", err))
		span.SetFailure(string(FailureConfiguration), false, time.Time{})
		span.End(dbmodel.AttemptFailed, 0, classified.Error())
		return 0, time.Time{}, classified
	}
	metrics.SetTransportRequestPayload(requestBody, actualModel)
	metrics.ActualModel = actualModel
	copyProxyHeaders(c.Request.Header, channel, request.Header)
	if exchange != nil {
		exchange.info.Model = actualModel
	}
	metrics.capture.upstreamRequest(exchange, request)

	response, err := sendCompactRequest(channel, request, func() error {
		return execution.reserveSubmission(ctx, relayCandidate{channel.ID, usedKey.ID, mappedModel})
	})
	if err != nil {
		if context.Cause(ctx) != nil {
			err = context.Cause(ctx)
		}
		wrapped := fmt.Errorf("failed to send compact request: %w", err)
		failure := classifyRelayFailureContext(c.Request.Context(), 0, wrapped, time.Time{})
		span.SetFailure(string(failure.Class), failure.Retryable, failure.RetryAt, failure.Scope)
		span.End(dbmodel.AttemptFailed, 0, wrapped.Error())
		return 0, time.Time{}, wrapped
	}
	defer response.Body.Close()
	metrics.capture.upstreamResponse(exchange, response)
	if exchange != nil {
		span.SetHTTPDetails(exchange.info.AttemptID, &response.StatusCode, exchange.info.UpstreamRequestID)
	}

	body, readErr := httpio.ReadResponseBody(response.Body)
	if readErr != nil {
		if context.Cause(ctx) != nil {
			readErr = context.Cause(ctx)
		}
		wrapped := fmt.Errorf("failed to read compact response body: %w", readErr)
		failure := classifyRelayFailureContext(c.Request.Context(), responseProcessingErrorStatus(response.StatusCode), wrapped, time.Time{})
		span.SetFailure(string(failure.Class), failure.Retryable, failure.RetryAt, failure.Scope)
		span.End(dbmodel.AttemptFailed, response.StatusCode, wrapped.Error())
		return responseProcessingErrorStatus(response.StatusCode), time.Time{}, wrapped
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		retryAt := parseRetryAt(response.Header.Get("Retry-After"))
		statusCode := normalizeUpstreamStatusCode(response.StatusCode, string(body))
		responseErr := transformerModel.NormalizeHTTPError(statusCode, response.Header, body, "api_error")
		failure := classifyRelayFailureContext(c.Request.Context(), statusCode, responseErr, retryAt)
		span.SetFailure(string(failure.Class), failure.Retryable, retryAt, failure.Scope)
		span.End(dbmodel.AttemptFailed, statusCode, responseErr.Error())
		return statusCode, retryAt, responseErr
	}

	hb.Stop()
	copyProxyResponseHeaders(c.Writer.Header(), response.Header)
	contentType := response.Header.Get("Content-Type")
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/json"
	}
	execution.committed.Store(true)
	metrics.capture.bindDownstream(exchange)
	c.Data(response.StatusCode, contentType, body)

	var compactResp responsesCompactResponse
	if err := json.Unmarshal(body, &compactResp); err == nil {
		metrics.SetInternalResponse(compactResponseToInternalResponse(&compactResp), actualModel)
	}

	span.End(dbmodel.AttemptSuccess, response.StatusCode, "")
	return response.StatusCode, time.Time{}, nil
}

func buildResponsesCompactRequest(ctx context.Context, channel *dbmodel.Channel, key string, requestBody []byte) (*http.Request, error) {
	parsedURL, err := url.Parse(strings.TrimSuffix(channel.GetBaseUrl(), "/"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse base url: %w", err)
	}
	parsedURL.Path = parsedURL.Path + "/responses/compact"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(requestBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	return req, nil
}

func copyProxyHeaders(src http.Header, channel *dbmodel.Channel, dst http.Header) {
	for key, values := range src {
		lowerKey := strings.ToLower(key)
		if hopByHopHeaders[lowerKey] || lowerKey == "content-type" {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
	for _, header := range channel.CustomHeader {
		if strings.EqualFold(header.HeaderKey, "Content-Type") {
			continue
		}
		dst.Set(header.HeaderKey, header.HeaderValue)
	}
	// 防止 Go 默认 User-Agent 泄露到上游
	if dst.Get("User-Agent") == "" {
		dst.Set("User-Agent", "")
	}
}

func copyProxyResponseHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		if hopByHopHeaders[strings.ToLower(key)] {
			continue
		}
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func sendCompactRequest(channel *dbmodel.Channel, req *http.Request, reserve func() error) (*http.Response, error) {
	httpClient, err := helper.ChannelHTTPClientWithContext(req.Context(), channel)
	if err != nil {
		return nil, classifyLocalRelayError(FailureConfiguration, err)
	}
	if err := reserve(); err != nil {
		return nil, err
	}
	return httpClient.Do(req)
}

func compactResponseToInternalResponse(resp *responsesCompactResponse) *transformerModel.InternalLLMResponse {
	if resp == nil {
		return nil
	}
	return &transformerModel.InternalLLMResponse{
		ID:      resp.ID,
		Object:  resp.Object,
		Created: resp.CreatedAt,
		Usage:   convertCompactUsage(resp.Usage),
	}
}

func convertCompactUsage(usage *openaiOutbound.ResponsesUsage) *transformerModel.Usage {
	if usage == nil {
		return nil
	}
	result := &transformerModel.Usage{
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      usage.TotalTokens,
	}
	if usage.InputTokenDetails.CachedTokens > 0 {
		result.PromptTokensDetails = &transformerModel.PromptTokensDetails{
			CachedTokens: usage.InputTokenDetails.CachedTokens,
		}
	}
	if usage.OutputTokenDetails.ReasoningTokens > 0 {
		result.CompletionTokensDetails = &transformerModel.CompletionTokensDetails{
			ReasoningTokens: usage.OutputTokenDetails.ReasoningTokens,
		}
	}
	return result
}
