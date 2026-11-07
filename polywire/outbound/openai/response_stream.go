package openai

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/samber/lo"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func (o *ResponseOutbound) toolCallIndexFor(outputIndex int) int {
	if o.toolCallIndexes == nil {
		o.toolCallIndexes = make(map[int]int)
	}
	if idx, ok := o.toolCallIndexes[outputIndex]; ok {
		return idx
	}
	idx := len(o.toolCallIndexes)
	o.toolCallIndexes[outputIndex] = idx
	return idx
}

func (o *ResponseOutbound) toolCallFromItem(outputIndex int, item ResponsesItem) model.ToolCall {
	return model.ToolCall{
		Index: o.toolCallIndexFor(outputIndex),
		ID:    item.CallID,
		Type:  "function",
		Function: model.FunctionCall{
			Name:      item.Name,
			Namespace: item.Namespace,
		},
	}
}

func (o *ResponseOutbound) ensureToolCallStarted(base model.StreamEvent, outputIndex int) []model.StreamEvent {
	if o.toolCallStarted == nil {
		o.toolCallStarted = make(map[int]bool)
	}
	if o.toolCallStarted[outputIndex] {
		return nil
	}
	item, ok := o.outputItems[outputIndex]
	if !ok || item.Type != "function_call" || item.Name == "" {
		return nil
	}

	o.toolCallStarted[outputIndex] = true
	toolCall := o.toolCallFromItem(outputIndex, item)
	return []model.StreamEvent{{Kind: model.StreamEventKindToolCallStart, ID: base.ID, Model: base.Model, Index: base.Index, ToolCall: &toolCall}}
}

func (o *ResponseOutbound) handleFunctionCallArgumentsDone(base model.StreamEvent, event ResponsesStreamEvent) ([]model.StreamEvent, error) {
	item := o.ensureOutputItem(event.OutputIndex, "function_call")
	if item.Type != "function_call" {
		return nil, nil
	}

	identityChanged := false
	if event.CallID != "" && event.CallID != item.CallID {
		item.CallID = event.CallID
		identityChanged = true
	}
	if event.Name != "" && event.Name != item.Name {
		item.Name = event.Name
		identityChanged = true
	}
	if event.Namespace != "" && event.Namespace != item.Namespace {
		item.Namespace = event.Namespace
		identityChanged = true
	}

	finalArgs := event.Arguments
	if finalArgs == "" {
		finalArgs = item.Arguments
	}
	forwardedArgs := o.toolCallForwardedArguments[event.OutputIndex]
	missingArgs := ""
	if finalArgs != "" {
		switch {
		case forwardedArgs == "":
			missingArgs = finalArgs
		case strings.HasPrefix(finalArgs, forwardedArgs):
			missingArgs = strings.TrimPrefix(finalArgs, forwardedArgs)
		case equalJSONValues(forwardedArgs, finalArgs):
			// Some providers reformat the final JSON without changing its value.
			// The downstream has already received the complete arguments.
			missingArgs = ""
		default:
			callID := item.CallID
			if callID == "" {
				callID = fmt.Sprintf("output_index=%d", event.OutputIndex)
			}
			return nil, fmt.Errorf("function call arguments mismatch for call_id %q", callID)
		}
		item.Arguments = finalArgs
	}
	o.outputItems[event.OutputIndex] = item

	wasStarted := o.toolCallStarted[event.OutputIndex]
	events := o.ensureToolCallStarted(base, event.OutputIndex)
	if !o.toolCallStarted[event.OutputIndex] {
		// A function name is required before a tool call can be represented in
		// Chat-style streams. Keep the completed item for raw Responses replay.
		return events, nil
	}

	if missingArgs != "" || (identityChanged && wasStarted) {
		toolCall := o.toolCallFromItem(event.OutputIndex, item)
		var delta *model.StreamDelta
		if missingArgs != "" {
			delta = &model.StreamDelta{Arguments: missingArgs}
		}
		events = append(events, model.StreamEvent{Kind: model.StreamEventKindToolCallDelta, ID: base.ID, Model: base.Model, Index: base.Index, ToolCall: &toolCall, Delta: delta})
	}
	if finalArgs != "" {
		o.toolCallForwardedArguments[event.OutputIndex] = finalArgs
	}
	return events, nil
}

// TransformSourceEvent converts a complete OpenAI Responses event. The
// envelope type wins over a payload type so split SSE framing cannot make the
// adapter process the wrong lifecycle event.
func (o *ResponseOutbound) TransformSourceEvent(ctx context.Context, event model.SourceEvent) (events []model.StreamEvent, err error) {
	var providerSequence *int64
	var providerType string
	defer func() {
		events = model.WithStreamSource(events, event, model.APIFormatOpenAIResponse, providerSequence, providerType)
	}()
	eventData := event.Data
	eventType := strings.TrimSpace(event.Type)
	if len(eventData) == 0 && eventType == "" {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(eventData), []byte("[DONE]")) || eventType == "[DONE]" || strings.EqualFold(eventType, "done") {
		if eventType == "" {
			eventType = "[DONE]"
		}
		return []model.StreamEvent{{Kind: model.StreamEventKindDone, Terminal: true, TerminalEvent: eventType}}, nil
	}

	if !o.initialized {
		o.initialized = true
		o.outputItems = make(map[int]ResponsesItem)
		o.toolCallIndexes = make(map[int]int)
		o.toolCallStarted = make(map[int]bool)
		o.toolCallForwardedArguments = make(map[int]string)
	}

	_, streamEvent, parseErr := parseResponseStreamEvent(event)
	if parseErr != nil {
		return nil, parseErr
	}
	providerType = streamEvent.Type
	providerSequence = streamEvent.SequenceNumber

	if streamEvent.Response != nil {
		if streamEvent.Response.ID != "" {
			o.streamID = streamEvent.Response.ID
		}
		if streamEvent.Response.Model != "" {
			o.streamModel = streamEvent.Response.Model
		}
	}

	base := model.StreamEvent{ID: o.streamID, Model: o.streamModel, Index: 0}
	events = append(events, o.responsesNativeEvents(streamEvent, base)...)

	switch streamEvent.Type {
	case "response.created", "response.in_progress":
		events = append(events, model.StreamEvent{Kind: model.StreamEventKindMessageStart, ID: base.ID, Model: base.Model, Index: base.Index, Role: "assistant"})

	case "response.output_text.delta":
		o.mergeOutputTextDelta(streamEvent)
		if streamEvent.Delta != "" {
			events = append(events, model.StreamEvent{Kind: model.StreamEventKindTextDelta, ID: base.ID, Model: base.Model, Index: base.Index, Delta: &model.StreamDelta{Text: streamEvent.Delta}})
		}

	case "response.function_call_arguments.delta":
		o.mergeFunctionCallDelta(streamEvent)
		item := o.outputItems[streamEvent.OutputIndex]
		if item.Name != "" {
			events = append(events, o.ensureToolCallStarted(base, streamEvent.OutputIndex)...)
			if streamEvent.Delta != "" {
				toolCall := o.toolCallFromItem(streamEvent.OutputIndex, item)
				events = append(events, model.StreamEvent{Kind: model.StreamEventKindToolCallDelta, ID: base.ID, Model: base.Model, Index: base.Index, ToolCall: &toolCall, Delta: &model.StreamDelta{Arguments: streamEvent.Delta}})
				o.toolCallForwardedArguments[streamEvent.OutputIndex] += streamEvent.Delta
			}
		}

	case "response.function_call_arguments.done":
		doneEvents, err := o.handleFunctionCallArgumentsDone(base, streamEvent)
		if err != nil {
			return nil, err
		}
		events = append(events, doneEvents...)

	case "response.output_item.added":
		o.mergeOutputItemAdded(streamEvent)
		if item, ok := o.outputItems[streamEvent.OutputIndex]; ok && item.Type == "function_call" && item.Name != "" {
			events = append(events, o.ensureToolCallStarted(base, streamEvent.OutputIndex)...)
		}

	case "response.output_item.done":
		o.mergeOutputItemAdded(streamEvent)
		if streamEvent.Item != nil && streamEvent.Item.Type == "reasoning" && streamEvent.Item.EncryptedContent != nil && *streamEvent.Item.EncryptedContent != "" {
			signature := model.OpaqueSignature{
				Provider: model.SignatureProviderOpenAI,
				Kind:     model.OpaqueSignatureKindOpenAIReasoning,
				Value:    *streamEvent.Item.EncryptedContent,
			}
			events = append(events, model.StreamEvent{
				Kind:  model.StreamEventKindSignatureDelta,
				ID:    base.ID,
				Model: base.Model,
				Index: base.Index,
				Delta: &model.StreamDelta{Signature: signature.Value, SignatureSource: &signature},
			})
		}

	case "response.reasoning_summary_text.delta":
		o.mergeReasoningDelta(streamEvent)
		if streamEvent.Delta != "" {
			events = append(events, model.StreamEvent{Kind: model.StreamEventKindThinkingDelta, ID: base.ID, Model: base.Model, Index: base.Index, Delta: &model.StreamDelta{Thinking: streamEvent.Delta}})
		}

	case "response.reasoning_text.delta":
		o.mergeReasoningTextDelta(streamEvent)
		if streamEvent.Delta != "" {
			events = append(events, model.StreamEvent{Kind: model.StreamEventKindThinkingDelta, ID: base.ID, Model: base.Model, Index: base.Index, Delta: &model.StreamDelta{Thinking: streamEvent.Delta}})
		}

	case "response.reasoning_text.done":
		o.mergeReasoningTextDone(streamEvent)
		events = append(events, responseProgressEvent(streamEvent, base))

	case "response.refusal.delta":
		if streamEvent.Delta != "" {
			events = append(events, model.StreamEvent{Kind: model.StreamEventKindTextDelta, ID: base.ID, Model: base.Model, Index: base.Index, Delta: &model.StreamDelta{Refusal: streamEvent.Delta}})
		}

	case "response.refusal.done":
		events = append(events, responseProgressEvent(streamEvent, base))

	case "response.output_text.annotation.added":
		citation, parseErr := model.OpenAICitationFromRaw(streamEvent.Annotation, model.APIFormatOpenAIResponse)
		if parseErr != nil {
			return nil, parseErr
		}
		citation.AnnotationIndex = streamEvent.AnnotationIndex
		events = append(events, model.StreamEvent{Kind: model.StreamEventKindCitationDelta, ID: base.ID, Model: base.Model, BlockIndex: streamEvent.ContentIndex, Delta: &model.StreamDelta{Citation: &citation}})

	case "response.audio.delta", "response.output_audio.delta":
		events = append(events, model.StreamEvent{Kind: model.StreamEventKindAudioDelta, ID: base.ID, Model: base.Model, BlockIndex: streamEvent.ContentIndex, Media: &model.StreamMedia{MediaType: "audio", Format: streamEvent.Format, Data: streamEvent.Delta}})
	case "response.audio_transcript.delta", "response.output_audio_transcript.delta":
		events = append(events, model.StreamEvent{Kind: model.StreamEventKindAudioDelta, ID: base.ID, Model: base.Model, BlockIndex: streamEvent.ContentIndex, Media: &model.StreamMedia{MediaType: "audio", Transcript: streamEvent.Delta}})
	case "response.content_part.added", "response.content_part.done", "response.output_text.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.reasoning_summary_text.done", "response.reasoning.delta", "response.reasoning.done", "response.audio.done", "response.output_audio.done", "response.audio_transcript.done", "response.output_audio_transcript.done":
		events = append(events, responseProgressEvent(streamEvent, base))

	case "response.completed", "response.done":
		if streamEvent.Response != nil {
			if len(streamEvent.Response.Output) > 0 {
				rawOutput := streamEvent.Response.RawOutput
				if len(rawOutput) == 0 {
					rawOutput, _ = marshalResponsesOutputItems(streamEvent.Response.Output)
				}
				if len(rawOutput) > 0 {
					base.ProviderExtensions = &model.ProviderExtensions{OpenAI: &model.OpenAIExtension{RawResponseItems: rawOutput}}
				}
			} else if rawOutput, ok := o.marshalTrackedOutputItems(); ok {
				base.ProviderExtensions = &model.ProviderExtensions{OpenAI: &model.OpenAIExtension{RawResponseItems: rawOutput}}
			}
			status := streamEvent.Response.Status
			if status == nil {
				status = lo.ToPtr("completed")
			}
			finishReason, respErr := normalizeResponsesFinishReason(status, streamEvent.Response.Error)
			if respErr != nil {
				events = append(events, model.StreamEvent{Kind: model.StreamEventKindError, ID: base.ID, Model: base.Model, Error: respErr})
				return events, nil
			}
			if finishReason != nil && *finishReason == "stop" && o.responseCarriesFunctionCall(streamEvent.Response) {
				finishReason = lo.ToPtr("tool_calls")
			}
			stopEvent := model.StreamEvent{Kind: model.StreamEventKindMessageStop, ID: base.ID, Model: base.Model, Index: base.Index, StopReason: model.ParseFinishReason(lo.FromPtr(finishReason)), ProviderExtensions: base.ProviderExtensions, Terminal: true, TerminalEvent: streamEvent.Type}
			events = append(events, stopEvent)
			if streamEvent.Response.Usage != nil {
				usage := convertResponsesUsage(streamEvent.Response.Usage)
				usageEvent := model.StreamEvent{Kind: model.StreamEventKindUsageDelta, ID: base.ID, Model: base.Model, Usage: usage, ProviderExtensions: base.ProviderExtensions}
				events = append(events, usageEvent)
			}
		} else {
			events = append(events, model.StreamEvent{Kind: model.StreamEventKindDone, ID: base.ID, Model: base.Model, Terminal: true, TerminalEvent: streamEvent.Type})
		}

	case "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "response.error", "error":
		var reason *string
		var respErr *model.ResponseError
		switch streamEvent.Type {
		case "response.incomplete":
			reason = lo.ToPtr("length")
		default:
			reason = lo.ToPtr("stop")
		}
		errDetail := streamEvent.Error
		if streamEvent.Response != nil && streamEvent.Response.Error != nil {
			errDetail = streamEvent.Response.Error
		}
		if errDetail != nil {
			respErr = &model.ResponseError{
				StatusCode: 502,
				Detail: model.ErrorDetail{
					Code:    NormalizeStreamErrorCode(errDetail.Code),
					Type:    errDetail.Type,
					Message: errDetail.Message,
				},
			}
		} else if NormalizeStreamErrorCode(streamEvent.Code) != "" || streamEvent.Message != "" {
			respErr = &model.ResponseError{
				StatusCode: 502,
				Detail: model.ErrorDetail{
					Code:    NormalizeStreamErrorCode(streamEvent.Code),
					Message: streamEvent.Message,
				},
			}
		}
		if respErr != nil {
			events = append(events, model.StreamEvent{Kind: model.StreamEventKindError, ID: base.ID, Model: base.Model, Error: respErr})
		} else if streamEvent.Type != "response.incomplete" {
			events = append(events, model.StreamEvent{Kind: model.StreamEventKindError, ID: base.ID, Model: base.Model, Error: &model.ResponseError{StatusCode: 502, Detail: model.ErrorDetail{Type: "upstream_error", Message: "OpenAI Responses stream ended with " + streamEvent.Type}}})
		}
		events = append(events, model.StreamEvent{Kind: model.StreamEventKindMessageStop, ID: base.ID, Model: base.Model, Index: base.Index, StopReason: model.ParseFinishReason(lo.FromPtr(reason)), Terminal: true, TerminalEvent: streamEvent.Type})
		if streamEvent.Response != nil && streamEvent.Response.Usage != nil {
			events = append(events, model.StreamEvent{Kind: model.StreamEventKindUsageDelta, ID: base.ID, Model: base.Model, Usage: convertResponsesUsage(streamEvent.Response.Usage)})
		}

	default:
		if len(events) == 0 {
			events = append(events, model.OpaqueSourceEvent(event))
		}
	}

	return events, nil
}

// TransformStreamEvent retains the byte-only compatibility contract.
func (o *ResponseOutbound) TransformStreamEvent(ctx context.Context, eventData []byte) ([]model.StreamEvent, error) {
	return o.TransformSourceEvent(ctx, model.SourceEvent{Data: eventData})
}

func (o *ResponseOutbound) TransformStream(ctx context.Context, eventData []byte) (*model.InternalLLMResponse, error) {
	events, err := o.TransformSourceEvent(ctx, model.SourceEvent{Data: eventData})
	if err != nil {
		return nil, err
	}
	return model.InternalResponseFromStreamEvents(events), nil
}
