package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func sourceDone(event model.SourceEvent) bool {
	return bytes.Equal(bytes.TrimSpace(event.Data), []byte("[DONE]")) || event.Type == "[DONE]" || strings.EqualFold(strings.TrimSpace(event.Type), "done")
}

func parseResponseStreamEvent(source model.SourceEvent) (model.SourceEvent, ResponsesStreamEvent, error) {
	parsed, ok := source.Decoded.(*ResponsesStreamEvent)
	if !ok {
		parsed = &ResponsesStreamEvent{}
		var err error
		if len(bytes.TrimSpace(source.Data)) > 0 {
			err = json.Unmarshal(source.Data, parsed)
		}
		if err != nil {
			switch strings.TrimSpace(source.Type) {
			case "response.created", "response.in_progress", "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "response.error", "error":
			default:
				return source, ResponsesStreamEvent{}, fmt.Errorf("failed to unmarshal stream event: %w", err)
			}
		} else {
			// Cache payload fields before applying the mutable SSE envelope type.
			source.Decoded = parsed
		}
	}
	event := *parsed
	if source.Type != "" {
		event.Type = strings.TrimSpace(source.Type)
	}
	return source, event, nil
}

func responseStreamEventID(event ResponsesStreamEvent) string {
	for _, id := range []string{event.ID, event.EventID, event.LastEventID} {
		if id = strings.TrimSpace(id); id != "" {
			return id
		}
	}
	return ""
}

func (o *ResponseOutbound) InspectSourceEvent(_ context.Context, source model.SourceEvent) (model.SourceEvent, model.StreamEventPreview, error) {
	if sourceDone(source) {
		return source, model.StreamEventPreview{EventType: "[DONE]", Terminal: true}, nil
	}
	source, event, err := parseResponseStreamEvent(source)
	if err != nil {
		return source, model.StreamEventPreview{}, err
	}
	preview := model.StreamEventPreview{EventType: event.Type, EventID: responseStreamEventID(event), Terminal: IsResponseTerminalEvent(event.Type) || event.Type == "error"}
	switch event.Type {
	case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.refusal.delta", "response.audio_transcript.delta", "response.output_audio_transcript.delta":
		preview.Semantic = strings.TrimSpace(event.Delta) != ""
	case "response.function_call_arguments.delta", "response.audio.delta", "response.output_audio.delta":
		preview.Semantic = event.Delta != ""
	case "response.function_call_arguments.done":
		preview.Semantic = event.Arguments != ""
	case "response.output_item.added", "response.output_item.done":
		preview.Semantic = event.Item != nil && event.Item.Type != "message" && event.Item.Type != "reasoning"
	case "response.output_text.annotation.added":
		preview.Semantic = len(event.Annotation) > 0
	default:
		preview.Semantic = strings.HasPrefix(event.Type, "response.mcp_") || strings.HasPrefix(event.Type, "response.computer_") || strings.Contains(event.Type, "_call.")
	}
	return source, preview, nil
}

type parsedChatEvent struct {
	response model.InternalLLMResponse
	err      *model.ErrorDetail
}

func parseChatStreamEvent(source model.SourceEvent) (*parsedChatEvent, error) {
	if parsed, ok := source.Decoded.(*parsedChatEvent); ok {
		return parsed, nil
	}
	parsed := &parsedChatEvent{}
	envelope := struct {
		*model.InternalLLMResponse
		Error *model.ErrorDetail `json:"error"`
	}{InternalLLMResponse: &parsed.response}
	err := json.Unmarshal(source.Data, &envelope)
	parsed.err = envelope.Error
	if parsed.err != nil {
		return parsed, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal stream chunk: %w", err)
	}
	for index := range parsed.response.Choices {
		choice := &parsed.response.Choices[index]
		if choice.FinishReason != nil && *choice.FinishReason == "" {
			choice.FinishReason = nil
		}
	}
	if err := chatCitations(&parsed.response); err != nil {
		return nil, err
	}
	return parsed, nil
}

func (o *ChatOutbound) InspectSourceEvent(_ context.Context, source model.SourceEvent) (model.SourceEvent, model.StreamEventPreview, error) {
	if sourceDone(source) {
		return source, model.StreamEventPreview{EventType: "[DONE]", Terminal: true}, nil
	}
	if len(source.Data) == 0 {
		return source, model.StreamEventPreview{EventType: source.Type}, nil
	}
	parsed, err := parseChatStreamEvent(source)
	if err != nil {
		return source, model.StreamEventPreview{}, err
	}
	source.Decoded = parsed
	preview := model.StreamEventPreview{EventType: source.Type, EventID: parsed.response.ID, Terminal: parsed.err != nil}
	if preview.EventType == "" {
		preview.EventType = parsed.response.Object
	}
	if parsed.err == nil {
		preview.Semantic = model.HasSemanticStreamEvents(model.StreamEventsFromInternalResponse(&parsed.response))
	}
	return source, preview, nil
}
