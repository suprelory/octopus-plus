package relay

import (
	"github.com/gin-gonic/gin"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/polywire/inbound"
)

type httpRelay struct {
	request     *relayRequest
	group       dbmodel.Group
	replayState *wsConversationState
}

func Handler(inboundType inbound.InboundType, c *gin.Context) {
	startHTTPRelayCapture(c)
	relay := prepareHTTPRelay(inboundType, c)
	if relay == nil {
		return
	}
	resp.UseSafeErrorLogDetails(c, CodeRelayUpstreamFailed, "relay response details omitted from console")
	defer relay.request.heartbeat.Stop()
	relay.run()
}

func (r *httpRelay) run() {
	req := r.request
	outcome := (&relayExecutor{request: req}).run()
	result := outcome.result
	if result.Success {
		r.saveResponsesReplay(outcome.attempt, outcome.channel, outcome.key)
	}
	defer func() {
		req.heartbeat.Stop()
		req.metrics.SaveWithChannelStats(req.requestContext(), result.Success, result.Err, req.attempts(), false)
	}()
	if result.Success || result.Canceled || result.Written || req.responseCommitted() {
		return
	}
	if result.ResetConversation {
		errorAdapter := req.inAdapter
		if outcome.attempt != nil {
			errorAdapter = outcome.attempt.inAdapter
		}
		if publicErr, ok := classifyWSPublicError(result.Err, result.StatusCode); ok {
			writeInboundProtocolError(req.c, req.heartbeat, errorAdapter, relayProtocolError(publicErr.Status, CodeRelayUpstreamFailed, publicErr.Message))
		} else {
			writeInboundProtocolError(req.c, req.heartbeat, errorAdapter, protocolErrorFromError(result.StatusCode, result.Err))
		}
		return
	}
	r.writeFinalError(result, result.Err, outcome.attempt)
}
