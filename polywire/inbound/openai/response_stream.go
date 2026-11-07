package openai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/samber/lo"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func (i *ResponseInbound) TransformStream(ctx context.Context, stream *model.InternalLLMResponse) ([]byte, error) {
	return i.TransformStreamEvents(ctx, model.StreamEventsFromInternalResponse(stream))
}

func (i *ResponseInbound) TransformStreamEvents(ctx context.Context, events []model.StreamEvent) ([]byte, error) {
	if err := model.ValidateNativeStreamEncoding(events, model.APIFormatOpenAIResponse); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	if stream := model.InternalResponseFromStreamEvents(events); stream != nil && stream.Object != "[DONE]" {
		i.streamAggregator.Add(stream)
	}

	var out [][]byte

	// Initialize tool call tracking maps if needed
	if i.toolCalls == nil {
		i.toolCalls = make(map[int]*model.ToolCall)
		i.toolCallItemStarted = make(map[int]bool)
		i.toolCallOutputIndex = make(map[int]int)
	}

	for _, event := range events {
		if event.ID != "" {
			i.responseID = event.ID
		}
		if event.Model != "" {
			i.model = event.Model
		}

		switch event.Kind {
		case model.StreamEventKindMessageMetadata:
			if event.Metadata != nil && event.Metadata.Created != 0 {
				i.createdAt = event.Metadata.Created
			}
		case model.StreamEventKindMessageStart:
			if !i.hasResponseCreated {
				i.hasResponseCreated = true
				response := &ResponsesResponse{
					Object:     "response",
					ID:         i.responseID,
					Model:      i.model,
					CreatedAt:  i.createdAt,
					Status:     lo.ToPtr("in_progress"),
					Truncation: i.truncation,
					Output:     []ResponsesItem{},
				}
				out = append(out, i.enqueueEvent(&ResponsesStreamEvent{Type: "response.created", Response: response}))
				out = append(out, i.enqueueEvent(&ResponsesStreamEvent{Type: "response.in_progress", Response: response}))
			}

		case model.StreamEventKindTextDelta:
			if event.Delta == nil {
				continue
			}
			if event.Delta.Refusal != "" {
				out = append(out, i.handleRefusalContent(event.Delta.Refusal)...)
			} else if event.Delta.Text != "" {
				out = append(out, i.handleTextContent(&event.Delta.Text)...)
			}

		case model.StreamEventKindCitationDelta:
			if event.Delta == nil || event.Delta.Citation == nil {
				continue
			}
			raw, err := model.OpenAICitationForWire(*event.Delta.Citation, model.APIFormatOpenAIResponse)
			if err != nil {
				return nil, model.NewStreamConversionLoss(event, model.APIFormatOpenAIResponse, err.Error())
			}
			if !i.hasContentPartStarted {
				out = append(out, i.handleTextContent(lo.ToPtr(""))...)
			}
			if i.messageAnnotations == nil {
				i.messageAnnotations = make(map[int][]ResponsesAnnotation)
			}
			index := len(i.messageAnnotations[i.contentIndex])
			i.messageAnnotations[i.contentIndex] = append(i.messageAnnotations[i.contentIndex], ResponsesAnnotation{Raw: raw})
			out = append(out, i.enqueueEvent(&ResponsesStreamEvent{Type: "response.output_text.annotation.added", ItemID: &i.currentItemID, OutputIndex: &i.outputIndex, ContentIndex: &i.contentIndex, AnnotationIndex: &index, Annotation: raw}))

		case model.StreamEventKindThinkingDelta:
			if event.Delta != nil {
				signature := openAIStreamSignature(event.Delta)
				if event.Delta.Thinking == "" && signature == "" {
					continue
				}
				if event.Delta.Thinking != "" {
					out = append(out, i.handleReasoningContent(&event.Delta.Thinking)...)
				} else if signature != "" {
					out = append(out, i.ensureReasoningItemStarted()...)
				}
				if signature != "" {
					i.reasoningBlockSignatures = append(i.reasoningBlockSignatures, signature)
					out = append(out, i.closeReasoningItem()...)
				}
			}

		case model.StreamEventKindSignatureDelta:
			if signature := openAIStreamSignature(event.Delta); signature != "" {
				out = append(out, i.ensureReasoningItemStarted()...)
				i.reasoningBlockSignatures = append(i.reasoningBlockSignatures, signature)
				out = append(out, i.closeReasoningItem()...)
			}

		case model.StreamEventKindContentBlockStart:
			if event.ContentBlock != nil && event.ContentBlock.Type == string(model.ReasoningBlockKindRedacted) {
				out = append(out, i.ensureReasoningItemStarted()...)
			}

		case model.StreamEventKindToolCallStart:
			if event.ToolCall != nil {
				out = append(out, i.handleToolCalls([]model.ToolCall{*event.ToolCall})...)
			}

		case model.StreamEventKindToolCallDelta:
			if event.ToolCall != nil {
				call := *event.ToolCall
				if event.Delta != nil && event.Delta.Arguments != "" {
					call.Function.Arguments = event.Delta.Arguments
				}
				out = append(out, i.handleToolCalls([]model.ToolCall{call})...)
			}

		case model.StreamEventKindMessageStop:
			if !i.hasFinished {
				i.hasFinished = true
				i.finalFinishReason = event.StopReason.String()
				out = append(out, i.closeCurrentContentPart()...)
				out = append(out, i.closeCurrentOutputItem()...)
			}

		case model.StreamEventKindUsageDelta:
			if event.Usage != nil && i.hasFinished && !i.responseCompleted {
				i.responseCompleted = true
				i.usage = event.Usage
				eventType, status := responsesTerminalEvent(i.finalFinishReason)
				output := i.finalOutputItems()
				if event.ProviderExtensions != nil && event.ProviderExtensions.OpenAI != nil && len(event.ProviderExtensions.OpenAI.RawResponseItems) > 0 {
					var items []ResponsesItem
					if err := json.Unmarshal(event.ProviderExtensions.OpenAI.RawResponseItems, &items); err == nil {
						output = items
					}
				}
				response := &ResponsesResponse{
					Object:     "response",
					ID:         i.responseID,
					Model:      i.model,
					CreatedAt:  i.createdAt,
					Status:     &status,
					Truncation: i.truncation,
					Output:     output,
					Usage:      convertUsageToResponses(i.usage),
				}
				out = append(out, i.enqueueEvent(&ResponsesStreamEvent{Type: eventType, Response: response}))
			}

		case model.StreamEventKindDone:
			if !i.responseCompleted && len(out) == 0 {
				return []byte("data: [DONE]\n\n"), nil
			}

		case model.StreamEventKindError:
			if event.Error == nil {
				continue
			}
			i.responseCompleted = true
			response := &ResponsesResponse{
				Object:    "response",
				ID:        i.responseID,
				Model:     i.model,
				CreatedAt: i.createdAt,
				Status:    lo.ToPtr("failed"),
				Error: &ResponsesError{
					Code:    500,
					Message: event.Error.Detail.Message,
				},
			}
			out = append(out, i.enqueueEvent(&ResponsesStreamEvent{Type: "response.failed", Response: response}))
		}
	}

	if len(out) == 0 {
		return nil, nil
	}

	result := make([]byte, 0)
	for _, event := range out {
		if event != nil {
			result = append(result, event...)
		}
	}
	return result, nil
}

func (a ResponsesAnnotation) MarshalJSON() ([]byte, error) {
	if len(a.Raw) > 0 {
		return json.Marshal(a.Raw)
	}
	type wireAnnotation ResponsesAnnotation
	return json.Marshal(wireAnnotation(a))
}

func (a *ResponsesAnnotation) UnmarshalJSON(data []byte) error {
	type wireAnnotation ResponsesAnnotation
	var annotation wireAnnotation
	if err := json.Unmarshal(data, &annotation); err != nil {
		return err
	}
	*a = ResponsesAnnotation(annotation)
	a.Raw = append(json.RawMessage(nil), data...)
	return nil
}

func (i *ResponseInbound) enqueueEvent(ev *ResponsesStreamEvent) []byte {
	ev.SequenceNumber = i.sequenceNumber
	i.sequenceNumber++

	data, err := json.Marshal(ev)
	if err != nil {
		return nil
	}

	return formatSSEData(data)
}

// formatSSEData formats data as SSE data line
func formatSSEData(data []byte) []byte {
	return []byte(fmt.Sprintf("data: %s\n\n", string(data)))
}

// responsesTerminalEvent picks the correct terminal stream event + status
// pair based on the canonical FinishReason (see model/finishreason.go).
// Length-truncated or paused turns map to response.incomplete; safety /
// refusal / error-class stops map to response.failed; everything else is
// the normal response.completed.
func responsesTerminalEvent(finishReason string) (eventType string, status string) {
	r := model.ParseFinishReason(finishReason)
	switch {
	case r.IsZero():
		return "response.completed", "completed"
	case r == model.FinishReasonLength || r == model.FinishReasonPauseTurn:
		return "response.incomplete", "incomplete"
	case r == model.FinishReasonError || r == model.FinishReasonMalformedCall:
		return "response.failed", "failed"
	case r.IsSafetyBlock():
		return "response.failed", "failed"
	default:
		return "response.completed", "completed"
	}
}
