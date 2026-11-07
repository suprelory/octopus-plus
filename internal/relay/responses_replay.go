package relay

import (
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
)

// prepareHTTPResponsesReplay makes a known continuation self-contained before
// candidate ranking. A failed merge keeps the original request and routing.
func prepareHTTPResponsesReplay(
	inboundType inbound.InboundType,
	apiKeyID, groupID int,
	request *model.InternalLLMRequest,
) (*model.InternalLLMRequest, *wsConversationState) {
	if inboundType != inbound.InboundTypeOpenAIResponse || request.RawAPIFormat != model.APIFormatOpenAIResponse {
		return request, nil
	}
	previousID := request.OpenAIPreviousResponseID()
	if previousID == "" {
		return request, nil
	}
	requestModel := request.Model
	state := resolveResponsesReplayState(apiKeyID, groupID, requestModel, request)
	if state == nil {
		log.Debugf("no HTTP replay state found (apikey=%d, group=%d, model=%s, previous_response_id=%s)",
			apiKeyID, groupID, requestModel, previousID)
		return request, nil
	}
	log.Debugf("loaded HTTP replay state (apikey=%d, group=%d, model=%s, previous_response_id=%s, channel=%d, key=%d)",
		apiKeyID, groupID, requestModel, previousID, state.ChannelID, state.ChannelKeyID)
	replayed := state.BuildReplayRequest(request)
	if replayed == nil {
		log.Warnf("HTTP replay history merge failed (apikey=%d, group=%d, model=%s, previous_response_id=%s), keeping original request",
			apiKeyID, groupID, requestModel, previousID)
		return request, nil
	}
	log.Debugf("HTTP replay request transformed (apikey=%d, removed previous_response_id, merged history)", apiKeyID)
	return replayed, state
}

// saveResponsesReplay runs only after the complete attempt succeeds. Reuse the
// response already collected by stream finalization so its aggregator is not
// consumed a second time, and retain prior turns for exact replay requests.
func (r *httpRelay) saveResponsesReplay(attempt *relayRequest, channel *dbmodel.Channel, key dbmodel.ChannelKey) {
	req := r.request
	if req.inboundType != inbound.InboundTypeOpenAIResponse || attempt == nil ||
		attempt.internalRequest.RawAPIFormat != model.APIFormatOpenAIResponse {
		return
	}
	internalResponse := req.metrics.InternalResponse
	if internalResponse == nil {
		var err error
		internalResponse, err = attempt.inAdapter.GetInternalResponse(req.c.Request.Context())
		if err != nil {
			log.Debugf("failed to get internal response for replay state save: %v", relayErrorDiagnostic(err))
		}
	}
	if internalResponse == nil {
		return
	}

	var state *wsConversationState
	if attempt.internalRequest.IsOpenAIExactReplayRequest() && r.replayState != nil {
		state = cloneWSConversationState(r.replayState)
		if state != nil {
			state.ChannelID = channel.ID
			state.ChannelKeyID = key.ID
		}
	}
	if state == nil {
		state = &wsConversationState{
			RequestModel: req.requestModel,
			ChannelID:    channel.ID,
			ChannelKeyID: key.ID,
		}
	}
	state.ApplySuccessfulTurn(attempt.internalRequest, internalResponse)
	if state.LastResponseID != "" {
		ttl := wsConversationStateTTL(r.group.SessionKeepTime)
		storeResponsesReplayState(req.apiKeyID, r.group.ID, req.requestModel, state, ttl)
		log.Debugf("saved HTTP replay state (apikey=%d, group=%d, model=%s, response_id=%s, channel=%d, key=%d, ttl=%v, is_replay=%t)",
			req.apiKeyID, r.group.ID, req.requestModel, state.LastResponseID, channel.ID, key.ID, ttl, attempt.internalRequest.IsOpenAIExactReplayRequest())
	}
}
