package relay

import (
	"context"
	"fmt"
	"net/http"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func (ra *relayAttempt) buildOutboundRequest(ctx context.Context) (*http.Request, outbound.LossReport, error) {
	if ra.preparedRequest != nil {
		return ra.preparedRequest.Build(ctx, ra.channel.GetBaseUrl(), ra.usedKey.ChannelKey)
	}
	return outbound.BuildRequest(ctx, ra.outAdapter, ra.channel.Type, ra.internalRequest, ra.channel.GetBaseUrl(), ra.usedKey.ChannelKey)
}

// forward 转发请求到上游服务
func (ra *relayAttempt) forward() (int, error) {
	ctx := ra.requestContext()
	ctx = ra.startFirstTokenBudget(ctx)
	if ra.transportRecovery == upstreamRecoveryHTTP {
		if requiresUpstreamWSContinuation(ra.internalRequest) || ra.internalRequest.HasOpenAIResponsesPassthrough() {
			return http.StatusConflict, fmt.Errorf("upstream continuation transport unavailable; please restart the conversation")
		}
		ra.metrics.SetWSRecovery(dbmodel.RelayLogWSRecoveryDowngrade)
		ra.transportRecovery = upstreamRecoveryHTTP
		return ra.forwardViaHTTP(ctx)
	}

	// 尝试上游 WebSocket（仅 OpenAI Response outbound 类型；必须是客户端 WS 入站且新开关显式启用）
	if ra.channel.Type == outbound.OutboundTypeOpenAIResponse &&
		ra.internalRequest.RawAPIFormat == model.APIFormatOpenAIResponse {

		shouldTryWS := false
		// Passthrough is now handled by forwardViaHTTP via PassthroughCapable interface
		if ra.internalRequest.IsOpenAIExactReplayRequest() {
			shouldTryWS = false
		} else if ra.c == nil {
			wsMode := effectiveResponsesWSMode(ra.channel)
			shouldTryWS = shouldEnableResponsesWS(ra.channel) && wsMode != responsesWSModeOff
		} else if requiresUpstreamWSContinuation(ra.internalRequest) {
			// Safety: HTTP ingress must not proactively use upstream WS for fresh requests,
			// but an explicit continuation cannot be safely failovered as ordinary HTTP.
			shouldTryWS = true
		}

		if shouldTryWS {
			statusCode, err := ra.forwardViaWS(ctx)
			if statusCode != -1 {
				return statusCode, err
			}
			if requiresUpstreamWSContinuation(ra.internalRequest) {
				balancer.DeleteRoutingAffinity(ra.apiKeyID, ra.groupID, ra.requestModel)
				return http.StatusConflict, fmt.Errorf("upstream continuation transport unavailable; please restart the conversation")
			}
			if ra.internalRequest.HasOpenAIResponsesPassthrough() {
				return http.StatusBadGateway, fmt.Errorf("upstream WS passthrough unavailable for native-only Responses semantics")
			}
			ra.metrics.SetWSRecovery(dbmodel.RelayLogWSRecoveryDowngrade)
			// statusCode == -1 means WS not available, fall through to HTTP
		}
	}

	return ra.forwardViaHTTP(ctx)
}
