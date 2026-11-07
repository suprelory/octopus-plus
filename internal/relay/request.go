package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/transformer"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/polywire/compat"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
)

// prepareHTTPRelay validates the inbound request and fixes request-scoped routing
// policy before any retries. A nil result means the protocol error was written.
func prepareHTTPRelay(inboundType inbound.InboundType, c *gin.Context) *httpRelay {
	requestModel, ready := "", false
	defer recordEarlyHTTPFailure(c, &requestModel, relayEndpointType(inboundType), &ready, time.Now())
	rawBody, internalRequest, inAdapter, err := parseRequest(inboundType, c)
	if err != nil {
		return nil
	}
	requestModel = internalRequest.Model
	if supportedModels := c.GetString("supported_models"); supportedModels != "" {
		if !slices.Contains(strings.Split(supportedModels, ","), internalRequest.Model) {
			writeInboundProtocolError(c, nil, inAdapter, relayProtocolError(http.StatusBadRequest, CodeRelayModelNotSupported, "model not supported"))
			return nil
		}
	}

	apiKeyID := c.GetInt("api_key_id")
	emptyResponseDetection := emptyResponseDetectionEnabled()
	group, err := op.GroupGetEnabledMap(requestModel, c.Request.Context())
	if err != nil {
		writeInboundProtocolError(c, nil, inAdapter, relayProtocolError(http.StatusNotFound, CodeRelayModelNotFound, "model not found"))
		return nil
	}
	candidateSnapshot := newCandidateSnapshot(c.Request.Context(), group)
	internalRequest, replayState := prepareHTTPResponsesReplay(inboundType, apiKeyID, group.ID, internalRequest)

	var preferredSticky *balancer.SessionEntry
	if replayState != nil {
		preferredSticky = responsesReplayStateToSticky(replayState)
		if preferredSticky != nil {
			log.Debugf("HTTP replay sticky routing preference (channel=%d, key=%d)", preferredSticky.ChannelID, preferredSticky.ChannelKeyID)
		}
	}
	capabilityPolicy := getCapabilityDegradationPolicy()
	capabilityPlanner := newRelayCapabilityPlanner(internalRequest, rawBody, false)
	iter := balancer.NewIteratorWithPreferenceAndQuality(group, apiKeyID, requestModel, preferredSticky, func(item dbmodel.GroupItem) int {
		channel, _ := candidateSnapshot.Channel(item.ChannelID)
		return capabilityPlanner.rankChannel(channel, item)
	}, resolveRequestAffinity(c.Request.Header, rawBody))
	if iter.Len() == 0 {
		writeInboundProtocolError(c, nil, inAdapter, relayProtocolError(http.StatusServiceUnavailable, CodeRelayNoAvailableChannel, "no available channel"))
		return nil
	}

	// Start before forwarding or backoff so slow connection setup is covered.
	// JSON requests can independently enable whitespace keep-alives.
	isStream := internalRequest.Stream != nil && *internalRequest.Stream
	heartbeat := startEarlyHeartbeat(c, isStream)
	ready = true
	metrics := NewRelayMetrics(apiKeyID, requestModel, relayEndpointType(inboundType), middleware.ClientIP(c), rawBody, internalRequest)
	metrics.capture = captureFromContext(c.Request.Context())
	if metrics.capture != nil {
		metrics.StartTime = metrics.capture.started
	}
	if replayState != nil {
		metrics.SetWSMode(dbmodel.RelayLogWSModeReplay)
		metrics.SetWSRecovery(dbmodel.RelayLogWSRecoveryReplay)
	}
	request := &relayRequest{
		c:                 c,
		inAdapter:         inAdapter,
		inboundType:       inboundType,
		internalRequest:   internalRequest,
		metrics:           metrics,
		apiKeyID:          apiKeyID,
		requestModel:      requestModel,
		groupID:           group.ID,
		groupSessionTTL:   group.SessionKeepTime,
		iter:              iter,
		capabilityPolicy:  capabilityPolicy,
		capabilityPlanner: capabilityPlanner,
		candidateSnapshot: candidateSnapshot,
		rawBody:           rawBody,
		heartbeat:         heartbeat,
		execution:         newRelayExecution(group, emptyResponseDetection),
	}
	return &httpRelay{
		request:     request,
		group:       group,
		replayState: replayState,
	}
}

// newAttemptInboundAdapter builds the fresh inbound adapter owned by one
// upstream attempt. Response-side state must stay isolated per attempt, but
// request-derived state is seeded from the canonical parsed request instead
// of re-running TransformRequest (JSON parse, protocol conversion, and
// token counting) on the unchanged body for every retry.
func newAttemptInboundAdapter(inboundType inbound.InboundType, seed *model.InternalLLMRequest) (model.Inbound, error) {
	adapter := transformer.Inbound(inboundType)
	if adapter == nil {
		return nil, fmt.Errorf("unsupported inbound type: %d", inboundType)
	}
	if seedable, ok := adapter.(model.RequestStateSeedable); ok {
		seedable.SeedRequestState(seed)
	}
	return adapter, nil
}

// parseRequest 解析并验证入站请求
// 返回值中的 rawBody 为客户端原始请求字节，供同格式直通路径重用。
func parseRequest(inboundType inbound.InboundType, c *gin.Context) ([]byte, *model.InternalLLMRequest, model.Inbound, error) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		// middleware.MaxBodySize 命中时要回 413，别把限流当成内部错误。
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			resp.ErrorWithCode(c, http.StatusRequestEntityTooLarge, CodeRelayRequestTooLarge, "request body too large")
			return nil, nil, nil, err
		}
		resp.ErrorWithCode(c, http.StatusInternalServerError, CodeRelayUpstreamFailed, err.Error())
		return nil, nil, nil, err
	}

	inAdapter := transformer.Inbound(inboundType)
	transformCtx := c.Request.Context()
	signatureScope := compat.GeminiSignatureScopeFromContext(transformCtx)
	if apiKeyID := c.GetInt("api_key_id"); apiKeyID > 0 {
		signatureScope.APIKeyID = strconv.Itoa(apiKeyID)
		transformCtx = compat.WithGeminiSignatureScope(transformCtx, signatureScope)
		c.Request = c.Request.WithContext(transformCtx)
	}
	internalRequest, err := inAdapter.TransformRequest(transformCtx, body)
	if err != nil {
		writeInboundProtocolError(c, nil, inAdapter, relayProtocolError(http.StatusBadRequest, "invalid_request", err.Error()))
		return nil, nil, nil, err
	}

	// Pass through the original query parameters
	internalRequest.Query = c.Request.URL.Query()

	if err := internalRequest.Validate(); err != nil {
		writeInboundProtocolError(c, nil, inAdapter, relayProtocolError(http.StatusBadRequest, "invalid_request", err.Error()))
		return nil, nil, nil, err
	}

	if trace := captureFromContext(c.Request.Context()); trace != nil {
		trace.recordClientRequest(body)
	}
	return body, internalRequest, inAdapter, nil
}

// newAttemptRelayRequest creates the mutable state owned by one upstream
// attempt. Request-level collaborators are shared only when they are safe to
// reuse across attempts.
func newAttemptRelayRequest(base *relayRequest, ctx context.Context, modelName string) (*relayRequest, error) {
	if base == nil {
		return nil, fmt.Errorf("base relay request is nil")
	}
	if base.internalRequest == nil {
		return nil, fmt.Errorf("base internal request is nil")
	}

	internalRequest := base.internalRequest.Clone()
	internalRequest.Model = modelName
	// Seed request-derived adapter state from the ORIGINAL request: it must
	// carry the client's model name, not the channel-mapped one written to
	// the attempt clone above.
	inAdapter, err := newAttemptInboundAdapter(base.inboundType, base.internalRequest)
	if err != nil {
		return nil, err
	}

	return &relayRequest{
		c:                 base.c,
		ctx:               ctx,
		inAdapter:         inAdapter,
		inboundType:       base.inboundType,
		internalRequest:   internalRequest,
		metrics:           base.metrics,
		apiKeyID:          base.apiKeyID,
		requestModel:      base.requestModel,
		groupID:           base.groupID,
		groupSessionTTL:   base.groupSessionTTL,
		iter:              base.iter,
		capabilityPolicy:  base.capabilityPolicy,
		capabilityPlanner: base.capabilityPlanner,
		candidateSnapshot: base.candidateSnapshot,
		rawBody:           base.rawBody,
		streamWriter:      base.streamWriter,
		heartbeat:         base.heartbeat,
		execution:         base.execution,
	}, nil
}
