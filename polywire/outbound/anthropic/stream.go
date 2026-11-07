package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/samber/lo"
	"github.com/suprelory/octopus-plus/polywire/compat"
	"github.com/suprelory/octopus-plus/polywire/model"
	anthropicModel "github.com/suprelory/octopus-plus/polywire/protocol/anthropic"
)

// TransformSourceEvent converts an Anthropic provider envelope while keeping
// the SSE event type authoritative over any payload field with the same name.
func (o *MessageOutbound) TransformSourceEvent(ctx context.Context, event model.SourceEvent) (events []model.StreamEvent, err error) {
	var providerType string
	defer func() {
		events = model.WithStreamSource(events, event, model.APIFormatAnthropicMessage, nil, providerType)
	}()
	eventType := strings.TrimSpace(event.Type)
	eventData := event.Data
	if len(eventData) == 0 && eventType == "" {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(eventData), []byte("[DONE]")) || eventType == "[DONE]" || strings.EqualFold(eventType, "done") {
		if eventType == "" {
			eventType = "[DONE]"
		}
		if o.messageStopped {
			return nil, nil
		}
		return []model.StreamEvent{{Kind: model.StreamEventKindDone, Terminal: true, TerminalEvent: eventType}}, nil
	}
	if !o.initialized {
		o.toolCalls = make(map[int]*model.ToolCall)
		o.blockToolCalls = make(map[int]int)
		o.serverToolUses = make(map[int]*model.ServerToolUseBlock)
		o.toolIndex = -1
		o.initialized = true
	}

	_, streamEvent, parseErr := parseSourceEvent(event)
	if parseErr != nil {
		return nil, parseErr
	}
	providerType = streamEvent.Type

	events = make([]model.StreamEvent, 0, 2)
	appendUsage := func(usage *model.Usage) {
		if usage != nil {
			events = append(events, model.StreamEvent{Kind: model.StreamEventKindUsageDelta, ID: o.streamID, Model: o.streamModel, Usage: usage})
		}
	}

	switch streamEvent.Type {
	case "message_start":
		if streamEvent.Message != nil {
			o.streamID = streamEvent.Message.ID
			o.streamModel = streamEvent.Message.Model
			// 上游只要返回了 usage 对象就采纳（即便全为 0），以便 message_delta
			// 能继承 PromptTokens。部分第三方兼容商在 message_start 返回全零 usage，
			// 之前的 >0 过滤会整体丢弃，导致后续 input 计为 0。
			if streamEvent.Message.Usage != nil {
				o.streamUsage = convertAnthropicUsage(streamEvent.Message.Usage)
			}
		}
		events = append(events, model.StreamEvent{Kind: model.StreamEventKindMessageStart, ID: o.streamID, Model: o.streamModel, Role: "assistant"})
		appendUsage(o.streamUsage)

	case "content_block_start":
		if streamEvent.ContentBlock == nil {
			return nil, nil
		}
		blockIndex := int(lo.FromPtr(streamEvent.Index))
		switch streamEvent.ContentBlock.Type {
		case "tool_use":
			o.toolIndex++
			toolCall := model.ToolCall{
				Index: o.toolIndex,
				ID:    streamEvent.ContentBlock.ID,
				Type:  "function",
				Function: model.FunctionCall{
					Name: lo.FromPtr(streamEvent.ContentBlock.Name),
				},
			}
			o.toolCalls[o.toolIndex] = &toolCall
			o.blockToolCalls[blockIndex] = o.toolIndex
			events = append(events, model.StreamEvent{Kind: model.StreamEventKindToolCallStart, ID: o.streamID, Model: o.streamModel, Index: 0, BlockIndex: &blockIndex, ToolCall: &toolCall})
		case "text", "thinking":
			events = append(events, model.StreamEvent{Kind: model.StreamEventKindContentBlockStart, ID: o.streamID, Model: o.streamModel, BlockIndex: &blockIndex, ContentBlock: &model.StreamContentBlock{Type: streamEvent.ContentBlock.Type}})
			if streamEvent.ContentBlock.Text != nil && *streamEvent.ContentBlock.Text != "" {
				events = append(events, model.StreamEvent{Kind: model.StreamEventKindTextDelta, ID: o.streamID, Model: o.streamModel, BlockIndex: &blockIndex, Delta: &model.StreamDelta{Text: *streamEvent.ContentBlock.Text}})
			}
			for _, citation := range compat.AnthropicCitationsToModel(streamEvent.ContentBlock.Citations) {
				events = append(events, model.StreamEvent{Kind: model.StreamEventKindCitationDelta, ID: o.streamID, Model: o.streamModel, BlockIndex: &blockIndex, Delta: &model.StreamDelta{Citation: &citation}})
			}
			if streamEvent.ContentBlock.Thinking != nil && *streamEvent.ContentBlock.Thinking != "" {
				events = append(events, model.StreamEvent{Kind: model.StreamEventKindThinkingDelta, ID: o.streamID, Model: o.streamModel, BlockIndex: &blockIndex, Delta: &model.StreamDelta{Thinking: *streamEvent.ContentBlock.Thinking}})
			}
		case "redacted_thinking":
			events = append(events, model.StreamEvent{Kind: model.StreamEventKindContentBlockStart, ID: o.streamID, Model: o.streamModel, Index: 0, BlockIndex: &blockIndex, ContentBlock: &model.StreamContentBlock{Type: "redacted_thinking", Data: streamEvent.ContentBlock.Data}})
		default:
			if anthropicModel.IsServerToolUse(streamEvent.ContentBlock.Type) {
				use := compat.AnthropicServerToolUseToModel(*streamEvent.ContentBlock)
				o.serverToolUses[blockIndex] = use
				events = append(events, model.StreamEvent{Kind: model.StreamEventKindContentBlockStart, ID: o.streamID, Model: o.streamModel, BlockIndex: &blockIndex, ContentBlock: &model.StreamContentBlock{Type: use.BlockType, ID: use.ID, Name: use.Name, Input: use.Input, ServerToolUse: use}})
				events = append(events, anthropicNativeTool(use.BlockType, "start", use.ID, use.Name, use.ServerName, blockIndex, use.Input))
				break
			}
			if anthropicModel.IsServerToolResult(streamEvent.ContentBlock.Type) {
				result := compat.AnthropicServerToolResultToModel(*streamEvent.ContentBlock)
				events = append(events, model.StreamEvent{Kind: model.StreamEventKindContentBlockStart, ID: o.streamID, Model: o.streamModel, Index: 0, BlockIndex: &blockIndex, ContentBlock: &model.StreamContentBlock{Type: streamEvent.ContentBlock.Type, ToolUseID: result.ToolUseID, IsError: result.IsError, ServerToolResult: result}})
				events = append(events, anthropicNativeTool(result.BlockType, "stop", result.ToolUseID, "", "", blockIndex, result.Content))
				break
			}
			return []model.StreamEvent{model.OpaqueSourceEvent(event)}, nil
		}

	case "content_block_delta":
		if streamEvent.Delta == nil || streamEvent.Delta.Type == nil {
			return nil, nil
		}
		switch *streamEvent.Delta.Type {
		case "text_delta":
			if streamEvent.Delta.Text != nil {
				events = append(events, model.StreamEvent{Kind: model.StreamEventKindTextDelta, ID: o.streamID, Model: o.streamModel, Index: 0, BlockIndex: lo.ToPtr(int(lo.FromPtr(streamEvent.Index))), Delta: &model.StreamDelta{Text: *streamEvent.Delta.Text}})
			}
		case "input_json_delta":
			blockIndex := int(lo.FromPtr(streamEvent.Index))
			if streamEvent.Delta.PartialJSON == nil {
				break
			}
			if use := o.serverToolUses[blockIndex]; use != nil {
				metadata := *use
				metadata.Input = nil
				events = append(events, model.StreamEvent{Kind: model.StreamEventKindContentBlockDelta, ID: o.streamID, Model: o.streamModel, BlockIndex: &blockIndex, ContentBlock: &model.StreamContentBlock{Type: use.BlockType, ServerToolUse: &metadata}, Delta: &model.StreamDelta{Arguments: *streamEvent.Delta.PartialJSON}})
				native := anthropicNativeTool(use.BlockType, "delta", use.ID, use.Name, use.ServerName, blockIndex, nil)
				native.Native.Arguments = *streamEvent.Delta.PartialJSON
				events = append(events, native)
			} else if toolIndex, ok := o.blockToolCalls[blockIndex]; ok {
				toolCall := model.ToolCall{Index: toolIndex, Type: "function", Function: model.FunctionCall{Arguments: *streamEvent.Delta.PartialJSON}}
				if existing := o.toolCalls[toolIndex]; existing != nil {
					toolCall.ID = existing.ID
				}
				events = append(events, model.StreamEvent{Kind: model.StreamEventKindToolCallDelta, ID: o.streamID, Model: o.streamModel, Index: 0, BlockIndex: lo.ToPtr(int(lo.FromPtr(streamEvent.Index))), ToolCall: &toolCall, Delta: &model.StreamDelta{Arguments: *streamEvent.Delta.PartialJSON}})
			}
		case "thinking_delta":
			if streamEvent.Delta.Thinking != nil {
				events = append(events, model.StreamEvent{Kind: model.StreamEventKindThinkingDelta, ID: o.streamID, Model: o.streamModel, Index: 0, BlockIndex: lo.ToPtr(int(lo.FromPtr(streamEvent.Index))), Delta: &model.StreamDelta{Thinking: *streamEvent.Delta.Thinking}})
			}
		case "signature_delta":
			if streamEvent.Delta.Signature != nil {
				events = append(events, model.StreamEvent{Kind: model.StreamEventKindSignatureDelta, ID: o.streamID, Model: o.streamModel, Index: 0, BlockIndex: lo.ToPtr(int(lo.FromPtr(streamEvent.Index))), Delta: &model.StreamDelta{
					Signature: *streamEvent.Delta.Signature,
					SignatureSource: &model.OpaqueSignature{
						Provider: model.SignatureProviderAnthropic,
						Kind:     model.OpaqueSignatureKindAnthropicThinking,
						Value:    *streamEvent.Delta.Signature,
					},
				}})
			}
		case "citations_delta":
			if streamEvent.Delta.Citation != nil {
				citation := compat.AnthropicCitationsToModel([]anthropicModel.Citation{*streamEvent.Delta.Citation})[0]
				events = append(events, model.StreamEvent{Kind: model.StreamEventKindCitationDelta, ID: o.streamID, Model: o.streamModel, Index: 0, BlockIndex: lo.ToPtr(int(lo.FromPtr(streamEvent.Index))), Delta: &model.StreamDelta{Citation: &citation}})
			}
		default:
			return []model.StreamEvent{model.OpaqueSourceEvent(event)}, nil
		}

	case "message_delta":
		if streamEvent.Usage != nil {
			usage := convertAnthropicUsage(streamEvent.Usage)
			if o.streamUsage != nil {
				// message_delta 自身通常只带 output；input 来自 message_start。
				// 仅当 delta 未携带 input 时才继承，避免上游把真实 input 放在
				// message_delta 时被 message_start 的零值覆盖。
				if usage.PromptTokens == 0 {
					usage.PromptTokens = o.streamUsage.PromptTokens
				}
				if usage.CacheCreationInputTokens == 0 {
					usage.CacheCreationInputTokens = o.streamUsage.CacheCreationInputTokens
				}
				if usage.CacheReadInputTokens == 0 {
					usage.CacheReadInputTokens = o.streamUsage.CacheReadInputTokens
				}
				if usage.CacheCreation5mInputTokens == 0 {
					usage.CacheCreation5mInputTokens = o.streamUsage.CacheCreation5mInputTokens
				}
				if usage.CacheCreation1hInputTokens == 0 {
					usage.CacheCreation1hInputTokens = o.streamUsage.CacheCreation1hInputTokens
				}
				if usage.PromptTokensDetails == nil {
					usage.PromptTokensDetails = o.streamUsage.PromptTokensDetails
				}
			}
			usage.TotalTokens = usage.EffectiveInputTokens() + usage.CompletionTokens
			o.streamUsage = usage
			appendUsage(usage)
		}
		if streamEvent.Delta != nil && streamEvent.Delta.StopReason != nil {
			finishReason := convertStopReason(streamEvent.Delta.StopReason)
			if finishReason != nil {
				events = append(events, model.StreamEvent{Kind: model.StreamEventKindMessageStop, ID: o.streamID, Model: o.streamModel, StopReason: model.ParseFinishReason(*finishReason), StopSequence: streamEvent.Delta.StopSequence})
			}
		}

	case "message_stop":
		if o.messageStopped {
			return nil, nil
		}
		o.messageStopped = true
		appendUsage(o.streamUsage)
		// An explicit terminal marker lets the finalizer infer a missing stop
		// reason without accepting an incomplete stream that only reached EOF.
		events = append(events, model.StreamEvent{Kind: model.StreamEventKindDone, Terminal: true, TerminalEvent: "message_stop"})

	case "content_block_stop":
		blockIndex := int(lo.FromPtr(streamEvent.Index))
		stop := model.StreamEvent{Kind: model.StreamEventKindContentBlockStop, ID: o.streamID, Model: o.streamModel, Index: 0, BlockIndex: &blockIndex}
		if toolIndex, ok := o.blockToolCalls[blockIndex]; ok {
			stop.Kind = model.StreamEventKindToolCallStop
			stop.ToolCall = &model.ToolCall{Index: toolIndex}
		}
		events = append(events, stop)
		if use := o.serverToolUses[blockIndex]; use != nil {
			events = append(events, anthropicNativeTool(use.BlockType, "stop", use.ID, use.Name, use.ServerName, blockIndex, nil))
		}
		delete(o.serverToolUses, int(lo.FromPtr(streamEvent.Index)))
		delete(o.blockToolCalls, int(lo.FromPtr(streamEvent.Index)))

	case "ping":
		return []model.StreamEvent{{Kind: model.StreamEventKindMessageMetadata, Importance: model.StreamImportanceMetadata}}, nil

	case "error":
		if streamEvent.Error == nil {
			return nil, &model.ResponseError{StatusCode: 502, Detail: model.ErrorDetail{Type: "upstream_error", Message: "Anthropic stream error without error detail"}}
		}
		events = append(events, model.StreamEvent{Kind: model.StreamEventKindError, ID: o.streamID, Model: o.streamModel, Error: &model.ResponseError{StatusCode: mapAnthropicErrorTypeToStatus(streamEvent.Error.Type), Detail: model.ErrorDetail{Type: streamEvent.Error.Type, Message: streamEvent.Error.Message}}})

	default:
		return []model.StreamEvent{model.OpaqueSourceEvent(event)}, nil
	}

	if len(events) == 0 {
		return nil, nil
	}
	return events, nil
}

func anthropicNativeTool(kind, phase, id, name, server string, index int, payload json.RawMessage) model.StreamEvent {
	eventKind := model.StreamEventKindServerTool
	if strings.HasPrefix(kind, "mcp_") {
		eventKind = model.StreamEventKindMCPCall
	}
	return model.StreamEvent{Kind: eventKind, BlockIndex: &index, Native: &model.StreamNativeEvent{Type: kind, Phase: phase, ID: id, Name: name, ServerLabel: server, Payload: payload}}
}

// TransformStreamEvent retains the byte-only compatibility contract.
func (o *MessageOutbound) TransformStreamEvent(ctx context.Context, eventData []byte) ([]model.StreamEvent, error) {
	return o.TransformSourceEvent(ctx, model.SourceEvent{Data: eventData})
}
