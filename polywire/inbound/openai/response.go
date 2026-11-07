package openai

import (
	"context"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

// ResponseInbound implements the Inbound interface for OpenAI Responses API.
type ResponseInbound struct {
	// State tracking
	hasResponseCreated      bool
	hasMessageItemStarted   bool
	hasReasoningItemStarted bool
	hasContentPartStarted   bool
	hasRefusalPartStarted   bool
	hasFinished             bool
	responseCompleted       bool
	finalFinishReason       string

	// Response metadata
	responseID string
	model      string
	createdAt  int64
	truncation *string

	// Content tracking
	outputIndex    int
	contentIndex   int
	sequenceNumber int
	currentItemID  string

	// messageContentOrder preserves the order content parts were emitted so
	// closeMessageItem can rebuild the final ResponsesInput.Items array with
	// the correct output_text / refusal sequencing.
	messageContentOrder []string
	messageAnnotations  map[int][]ResponsesAnnotation

	// Content accumulation
	accumulatedText      strings.Builder
	accumulatedReasoning strings.Builder
	accumulatedRefusal   strings.Builder

	// reasoningBlockSignatures holds the signature for the currently open
	// reasoning item. A signature closes that item so opaque values are never
	// combined or rewritten.
	reasoningBlockSignatures []string

	// Tool call tracking
	toolCalls           map[int]*model.ToolCall
	toolCallItemStarted map[int]bool
	toolCallOutputIndex map[int]int

	// Usage tracking
	usage *model.Usage

	// completedOutputItems buffers every ResponsesItem emitted during streaming
	// (message / reasoning / function_call) so the terminal response.completed
	// event can echo the full output array. Upstream Responses clients treat
	// an empty output on response.completed as an error (O-H3).
	completedOutputItems []ResponsesItem

	streamAggregator model.StreamAggregator
	// storedResponse stores the non-stream response
	storedResponse *model.InternalLLMResponse
}

// SeedRequestState restores the request-derived truncation strategy that
// TransformRequest captured from the client body, so retry attempts echo the
// same value in response.created / response.completed (O-H5) without
// re-parsing the body.
func (i *ResponseInbound) SeedRequestState(request *model.InternalLLMRequest) {
	if i == nil || request == nil {
		return
	}
	i.truncation = request.Truncation
}

// GetInternalResponse returns the complete internal response for logging, statistics, etc.
// For streaming: aggregates all stored stream chunks into a complete response
// For non-streaming: returns the stored response
func (i *ResponseInbound) GetInternalResponse(ctx context.Context) (*model.InternalLLMResponse, error) {
	if i.storedResponse != nil {
		return i.storedResponse, nil
	}
	return i.streamAggregator.BuildAndReset(), nil
}
