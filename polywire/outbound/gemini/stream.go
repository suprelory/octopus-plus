package gemini

import (
	"bytes"
	"context"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func (o *MessagesOutbound) nextReasoningIndex(candidateIndex int) int {
	if o.streamReasoningIndex == nil {
		o.streamReasoningIndex = make(map[int]int)
	}
	index := o.streamReasoningIndex[candidateIndex]
	o.streamReasoningIndex[candidateIndex]++
	return index
}

func (o *MessagesOutbound) nextToolCallIndex() int {
	index := o.streamToolCallIndex
	o.streamToolCallIndex++
	return index
}

func (o *MessagesOutbound) TransformSourceEvent(ctx context.Context, event model.SourceEvent) (events []model.StreamEvent, err error) {
	defer func() { events = model.WithStreamSource(events, event, model.APIFormatGeminiContents, nil) }()
	eventType := strings.TrimSpace(event.Type)
	if bytes.Equal(bytes.TrimSpace(event.Data), []byte("[DONE]")) || eventType == "[DONE]" || strings.EqualFold(eventType, "done") {
		if eventType == "" {
			eventType = "[DONE]"
		}
		return []model.StreamEvent{{Kind: model.StreamEventKindDone, Terminal: true, TerminalEvent: eventType}}, nil
	}
	if len(bytes.TrimSpace(event.Data)) == 0 {
		return nil, nil
	}
	response, parseErr := parseGeminiSource(event)
	if parseErr != nil {
		return nil, parseErr
	}
	chunk := o.convertGeminiToLLMResponse(response, true, o.nextReasoningIndex, o.nextToolCallIndex)
	for _, candidate := range response.Candidates {
		if candidate == nil || candidate.Content == nil {
			continue
		}
		for _, part := range candidate.Content.Parts {
			if part == nil {
				continue
			}
			var extra *model.StreamEvent
			if part.FileData != nil {
				media := &model.StreamMedia{MediaType: part.FileData.MimeType, URI: part.FileData.FileURI}
				switch {
				case strings.HasPrefix(strings.ToLower(media.MediaType), "audio/"):
					extra = &model.StreamEvent{Kind: model.StreamEventKindAudioDelta, Media: media}
				case strings.HasPrefix(strings.ToLower(media.MediaType), "image/"):
					extra = &model.StreamEvent{Kind: model.StreamEventKindImageDelta, Media: media}
				default:
					opaque := model.OpaqueSourceEvent(event)
					extra = &opaque
				}
			} else if part.Text == "" && part.ThoughtSignature == "" && part.InlineData == nil && part.FunctionCall == nil && part.ExecutableCode == nil && part.CodeExecutionResult == nil {
				opaque := model.OpaqueSourceEvent(event)
				extra = &opaque
			}
			if extra != nil {
				extra.ID, extra.Model, extra.Index = chunk.ID, chunk.Model, candidate.Index
				events = append(events, *extra)
			}
		}
	}
	for _, event := range model.StreamEventsFromInternalResponse(chunk) {
		if event.Kind == model.StreamEventKindToolCallDelta && event.ToolCall != nil {
			start := event
			start.Kind, start.Delta = model.StreamEventKindToolCallStart, nil
			tool := *event.ToolCall
			tool.Function.Arguments = ""
			start.ToolCall = &tool
			events = append(events, start, event)
			start.Kind = model.StreamEventKindToolCallStop
			events = append(events, start)
		} else {
			events = append(events, event)
		}
		if event.Metadata != nil && event.Metadata.Choice != nil && event.Metadata.Choice.Grounding != nil {
			events = append(events, model.StreamEvent{Kind: model.StreamEventKindGrounding, ID: chunk.ID, Model: chunk.Model, Index: event.Index, Grounding: event.Metadata.Choice.Grounding})
		}
		if event.ContentBlock != nil && (event.ContentBlock.ServerToolUse != nil || event.ContentBlock.ServerToolResult != nil) {
			native := &model.StreamNativeEvent{Type: event.ContentBlock.Type, Phase: "start"}
			if use := event.ContentBlock.ServerToolUse; use != nil {
				native.ID, native.Name, native.Payload = use.ID, use.Name, use.Input
			}
			if result := event.ContentBlock.ServerToolResult; result != nil {
				native.Phase, native.Payload = "stop", result.Content
			}
			events = append(events, model.StreamEvent{Kind: model.StreamEventKindServerTool, ID: chunk.ID, Model: chunk.Model, Index: event.Index, Native: native})
		}
	}
	if len(response.Candidates) == 0 && response.PromptFeedback != nil && response.PromptFeedback.BlockReason != "" {
		for index := range events {
			if events[index].Kind == model.StreamEventKindMessageStop {
				events[index].Terminal, events[index].TerminalEvent = true, "prompt_feedback"
			}
		}
	}
	if len(events) == 0 {
		events = append(events, model.OpaqueSourceEvent(event))
	}
	return events, nil
}

func (o *MessagesOutbound) TransformStreamEvent(ctx context.Context, eventData []byte) ([]model.StreamEvent, error) {
	return o.TransformSourceEvent(ctx, model.SourceEvent{Data: eventData})
}

func (o *MessagesOutbound) TransformStream(ctx context.Context, eventData []byte) (*model.InternalLLMResponse, error) {
	events, err := o.TransformSourceEvent(ctx, model.SourceEvent{Data: eventData})
	if err != nil {
		return nil, err
	}
	return model.InternalResponseFromStreamEvents(events), nil
}
