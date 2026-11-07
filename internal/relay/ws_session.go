package relay

import (
	"encoding/json"
	"strings"

	transformerModel "github.com/suprelory/octopus-plus/polywire/model"
	openaiOutbound "github.com/suprelory/octopus-plus/polywire/outbound/openai"
)

type wsConversationState struct {
	DownstreamSessionID string
	RequestModel        string
	ChannelID           int
	ChannelKeyID        int
	LastResponseID      string
	ReplayWindowItems   json.RawMessage
	Transcript          []transformerModel.Message
	ReplayAliases       []string
	ReplayPending       bool
}

func (s *wsConversationState) MatchesRequestModel(requestModel string) bool {
	if s == nil {
		return false
	}
	return strings.TrimSpace(s.RequestModel) == strings.TrimSpace(requestModel)
}

func (s *wsConversationState) CanAutoRestart(req *transformerModel.InternalLLMRequest) bool {
	if s == nil || req == nil {
		return false
	}
	if len(s.ReplayWindowItems) > 0 {
		if strings.TrimSpace(s.LastResponseID) == "" {
			return false
		}
		prevID := req.OpenAIPreviousResponseID()
		if prevID == "" {
			return true
		}
		return s.MatchesPreviousResponseID(prevID)
	}
	if s.ReplayPending && requestContainsToolOutputs(req) {
		return false
	}
	if strings.TrimSpace(s.LastResponseID) == "" || len(s.Transcript) == 0 {
		return false
	}
	if !requiresUpstreamWSContinuation(req) {
		return false
	}
	prevID := req.OpenAIPreviousResponseID()
	if prevID == "" {
		return true
	}
	return s.MatchesPreviousResponseID(prevID)
}

func (s *wsConversationState) MatchesPreviousResponseID(responseID string) bool {
	if s == nil {
		return false
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return false
	}
	if responseID == strings.TrimSpace(s.LastResponseID) {
		return true
	}
	for _, alias := range s.ReplayAliases {
		if responseID == strings.TrimSpace(alias) {
			return true
		}
	}
	return false
}

func (s *wsConversationState) ShouldRewritePreviousResponseID(responseID string) bool {
	if s == nil {
		return false
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" || responseID == strings.TrimSpace(s.LastResponseID) {
		return false
	}
	for _, alias := range s.ReplayAliases {
		if responseID == strings.TrimSpace(alias) {
			return true
		}
	}
	return false
}

func (s *wsConversationState) ShouldUseNativeContinuation(req *transformerModel.InternalLLMRequest) bool {
	if s == nil || req == nil {
		return false
	}
	if strings.TrimSpace(req.OpenAIPreviousResponseID()) == "" {
		return false
	}
	if strings.TrimSpace(s.LastResponseID) == "" {
		return false
	}
	return !s.ReplayPending
}

func (s *wsConversationState) ShouldUseLocalReplay(req *transformerModel.InternalLLMRequest) bool {
	if s == nil || req == nil {
		return false
	}
	if len(s.ReplayWindowItems) == 0 || strings.TrimSpace(s.LastResponseID) == "" {
		return false
	}
	prevID := req.OpenAIPreviousResponseID()
	if prevID == "" {
		return true
	}
	return s.MatchesPreviousResponseID(prevID)
}

func (s *wsConversationState) MarkReplayRecovered(req *transformerModel.InternalLLMRequest) {
	if s == nil {
		return
	}
	s.ReplayPending = requestContainsToolOutputs(req)
}

func (s *wsConversationState) MarkNativeContinuationReady() {
	if s == nil {
		return
	}
	s.ReplayPending = false
}

func (s *wsConversationState) RememberReplayAlias(responseID string) {
	if s == nil {
		return
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" || responseID == strings.TrimSpace(s.LastResponseID) {
		return
	}
	filtered := make([]string, 0, len(s.ReplayAliases)+1)
	filtered = append(filtered, responseID)
	for _, alias := range s.ReplayAliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || alias == responseID {
			continue
		}
		filtered = append(filtered, alias)
		if len(filtered) >= 8 {
			break
		}
	}
	s.ReplayAliases = filtered
}

// BuildReplayRequest transforms a continuation request into a self-contained replay request
// by merging historical context from the stored state and removing previous_response_id.
//
// Returns nil if the transformation fails (e.g., unable to build replay raw input items).
// Callers should fall back to the original request when nil is returned.
func (s *wsConversationState) BuildReplayRequest(req *transformerModel.InternalLLMRequest) *transformerModel.InternalLLMRequest {
	if s == nil || req == nil {
		return nil
	}
	replayed := cloneInternalRequest(req)
	responsesOptions := replayed.GetOpenAIResponsesOptions()
	responsesOptions.PreviousResponseID = nil
	responsesOptions.Conversation = nil
	responsesOptions.RawInputItems = nil
	replayed.SetOpenAIResponsesOptions(responsesOptions)
	replayed.SetOpenAIRawInputItems(nil)
	replayed.SetConversationMessages(retainInstructionMessages(req.ConversationMessages()))

	// Merge historical replay window and current request into raw input items
	mergedRawInputItems, ok := buildReplayRawInputItems(s.ReplayWindowItems, s.Transcript, req.OpenAIRawInputItems(), req.ConversationMessages())
	if !ok || len(mergedRawInputItems) == 0 {
		// Merge failed - return nil to signal caller to use original request
		return nil
	}

	replayed.SetOpenAIRawInputItems(mergedRawInputItems)
	replayed.TransformOptions.ArrayInputs = boolPtr(true)
	if replayed.TransformerMetadata == nil {
		replayed.TransformerMetadata = map[string]string{}
	}
	replayed.MarkOpenAIExactReplayRequest()
	return replayed
}

func (s *wsConversationState) ApplySuccessfulTurn(req *transformerModel.InternalLLMRequest, resp *transformerModel.InternalLLMResponse) {
	if s == nil || req == nil || resp == nil {
		return
	}
	s.RequestModel = strings.TrimSpace(req.Model)
	if replayWindowItems, ok := buildNextReplayWindow(s.ReplayWindowItems, req, resp); ok {
		s.ReplayWindowItems = replayWindowItems
	}
	s.Transcript = append(s.Transcript, cloneMessages(req.ConversationMessages())...)
	s.Transcript = append(s.Transcript, assistantMessagesFromResponse(resp)...)
	if respID := strings.TrimSpace(resp.ID); respID != "" {
		s.LastResponseID = respID
	}
}

func cloneWSConversationState(state *wsConversationState) *wsConversationState {
	if state == nil {
		return nil
	}
	return &wsConversationState{
		DownstreamSessionID: strings.TrimSpace(state.DownstreamSessionID),
		RequestModel:        strings.TrimSpace(state.RequestModel),
		ChannelID:           state.ChannelID,
		ChannelKeyID:        state.ChannelKeyID,
		LastResponseID:      strings.TrimSpace(state.LastResponseID),
		ReplayWindowItems:   append(json.RawMessage(nil), state.ReplayWindowItems...),
		Transcript:          cloneMessages(state.Transcript),
		ReplayAliases:       append([]string(nil), state.ReplayAliases...),
		ReplayPending:       state.ReplayPending,
	}
}

func assistantMessagesFromResponse(resp *transformerModel.InternalLLMResponse) []transformerModel.Message {
	if resp == nil || len(resp.Choices) == 0 {
		return nil
	}
	result := make([]transformerModel.Message, 0, len(resp.Choices))
	for _, choice := range resp.Choices {
		if choice.Message == nil {
			continue
		}
		result = append(result, cloneMessage(*choice.Message))
	}
	return result
}

func cloneInternalRequest(req *transformerModel.InternalLLMRequest) *transformerModel.InternalLLMRequest {
	return req.Clone()
}

func cloneMessages(messages []transformerModel.Message) []transformerModel.Message {
	return transformerModel.CloneMessages(messages)
}

func cloneMessage(message transformerModel.Message) transformerModel.Message {
	return message.Clone()
}

func boolPtr(value bool) *bool {
	return &value
}

func retainInstructionMessages(messages []transformerModel.Message) []transformerModel.Message {
	if len(messages) == 0 {
		return nil
	}
	kept := make([]transformerModel.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "system" || msg.Role == "developer" {
			kept = append(kept, cloneMessage(msg))
		}
	}
	return kept
}

func requestContainsToolOutputs(req *transformerModel.InternalLLMRequest) bool {
	if req == nil {
		return false
	}
	for _, msg := range req.ConversationMessages() {
		if msg.Role == "tool" && msg.ToolCallID != nil && strings.TrimSpace(*msg.ToolCallID) != "" {
			return true
		}
	}
	return false
}

func buildReplayRawInputItems(
	replayWindowItems json.RawMessage,
	transcript []transformerModel.Message,
	currentRawInputItems json.RawMessage,
	currentMessages []transformerModel.Message,
) (json.RawMessage, bool) {
	currentItems, ok := buildRequestInputItems(currentRawInputItems, currentMessages)
	if !ok {
		return nil, false
	}
	if len(replayWindowItems) > 0 {
		return mergeRawJSONArray(replayWindowItems, currentItems)
	}
	if len(transcript) == 0 {
		return append(json.RawMessage(nil), currentItems...), true
	}
	transcriptItems, err := openaiOutbound.MarshalResponsesInputItems(transcript)
	if err != nil {
		return nil, false
	}
	return mergeRawJSONArray(transcriptItems, currentItems)
}

func buildRequestInputItems(currentRawInputItems json.RawMessage, currentMessages []transformerModel.Message) (json.RawMessage, bool) {
	// RawInputItems remains the authoritative runtime source for Responses replay
	// because it preserves exact upstream items that Messages cannot always rebuild.
	if len(currentRawInputItems) > 0 {
		return append(json.RawMessage(nil), currentRawInputItems...), true
	}
	currentItems, err := openaiOutbound.MarshalResponsesInputItems(currentMessages)
	if err != nil || len(currentItems) == 0 {
		return nil, false
	}
	return currentItems, true
}

func buildNextReplayWindow(existing json.RawMessage, req *transformerModel.InternalLLMRequest, resp *transformerModel.InternalLLMResponse) (json.RawMessage, bool) {
	if req == nil || resp == nil {
		return nil, false
	}
	var base json.RawMessage
	if rawInputItems := req.OpenAIRawInputItems(); req.IsOpenAIExactReplayRequest() && len(rawInputItems) > 0 {
		base = rawInputItems
	} else if currentItems, ok := buildRequestInputItems(req.OpenAIRawInputItems(), req.ConversationMessages()); ok {
		if len(existing) > 0 {
			merged, ok := mergeRawJSONArray(existing, currentItems)
			if !ok {
				return nil, false
			}
			base = merged
		} else {
			base = currentItems
		}
	} else if len(existing) > 0 {
		base = append(json.RawMessage(nil), existing...)
	}
	if len(base) == 0 {
		return nil, false
	}
	if len(resp.RawResponsesOutputItems) == 0 {
		return base, true
	}
	return mergeRawJSONArray(base, resp.RawResponsesOutputItems)
}

func mergeRawJSONArray(parts ...json.RawMessage) (json.RawMessage, bool) {
	mergedItems := make([]json.RawMessage, 0)
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		decoded, err := decodeRawJSONArray(part)
		if err != nil {
			return nil, false
		}
		mergedItems = append(mergedItems, decoded...)
	}
	if len(mergedItems) == 0 {
		return nil, false
	}
	data, err := json.Marshal(mergedItems)
	if err != nil {
		return nil, false
	}
	return data, true
}

func decodeRawJSONArray(data json.RawMessage) ([]json.RawMessage, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	return items, nil
}
