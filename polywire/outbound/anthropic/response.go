package anthropic

import (
	"net/http"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/compat"
	"github.com/suprelory/octopus-plus/polywire/model"
	anthropicModel "github.com/suprelory/octopus-plus/polywire/protocol/anthropic"
)

// Response conversion functions

func (o *MessageOutbound) convertToLLMResponse(resp *anthropicModel.Message) *model.InternalLLMResponse {
	if resp == nil {
		return &model.InternalLLMResponse{
			Object: "chat.completion",
		}
	}

	result := &model.InternalLLMResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Model:   resp.Model,
		Created: 0,
	}

	var (
		content           model.MessageContent
		thinkingText      *string
		thinkingSignature *string
		toolCalls         []model.ToolCall
		textParts         []string
		redactedBlocks    []string
		reasoningBlocks   []model.ReasoningBlock
		citations         []model.Citation
	)

	for blockIndex, block := range resp.Content {
		switch block.Type {
		case "text":
			if block.Text != nil {
				textParts = append(textParts, *block.Text)
				part := model.MessageContentPart{
					Type:       "text",
					Text:       block.Text,
					Citations:  compat.AnthropicCitationsToModel(block.Citations),
					BlockIndex: &blockIndex,
				}
				content.MultipleContent = append(content.MultipleContent, part)
				citations = append(citations, part.Citations...)
			}
		case "tool_use":
			if block.ID != "" && block.Name != nil {
				input := "{}"
				if len(block.Input) > 0 {
					input = string(block.Input)
				}
				toolCalls = append(toolCalls, model.ToolCall{
					ID:   block.ID,
					Type: "function",
					Function: model.FunctionCall{
						Name:      *block.Name,
						Arguments: input,
					},
				})
			}
		case "thinking":
			if block.Thinking != nil {
				thinkingText = block.Thinking
			}
			thinkingSignature = block.Signature
			rb := model.ReasoningBlock{
				Kind:     model.ReasoningBlockKindThinking,
				Index:    len(reasoningBlocks),
				Provider: string(model.SignatureProviderAnthropic),
			}
			if block.Thinking != nil {
				rb.Text = *block.Thinking
			}
			if block.Signature != nil {
				rb.SetOpaqueSignature(model.OpaqueSignature{
					Provider: model.SignatureProviderAnthropic,
					Kind:     model.OpaqueSignatureKindAnthropicThinking,
					Value:    *block.Signature,
				})
			}
			reasoningBlocks = append(reasoningBlocks, rb)
		case "redacted_thinking":
			if block.Data != "" {
				redactedBlocks = append(redactedBlocks, block.Data)
				reasoningBlocks = append(reasoningBlocks, model.ReasoningBlock{
					Kind:     model.ReasoningBlockKindRedacted,
					Index:    len(reasoningBlocks),
					Data:     block.Data,
					Provider: "anthropic",
				})
			}
		default:
			if anthropicModel.IsServerToolUse(block.Type) {
				content.MultipleContent = append(content.MultipleContent, model.MessageContentPart{
					Type: "server_tool_use", BlockIndex: &blockIndex,
					ServerToolUse: compat.AnthropicServerToolUseToModel(block),
				})
			} else if anthropicModel.IsServerToolResult(block.Type) {
				content.MultipleContent = append(content.MultipleContent, model.MessageContentPart{
					Type: "server_tool_result", BlockIndex: &blockIndex,
					ServerToolResult: compat.AnthropicServerToolResultToModel(block),
				})
			}
		}
	}

	// If we only have text content, use simple string format
	if len(textParts) > 0 && len(content.MultipleContent) == len(textParts) && len(citations) == 0 {
		allText := strings.Join(textParts, "")
		content.Content = &allText
		content.MultipleContent = nil
	}

	message := &model.Message{
		Role:                   resp.Role,
		Content:                content,
		ToolCalls:              toolCalls,
		ReasoningContent:       thinkingText,
		RedactedThinkingBlocks: redactedBlocks,
		ReasoningBlocks:        reasoningBlocks,
	}
	if thinkingSignature != nil && *thinkingSignature != "" {
		message.SetOpaqueReasoningSignature(model.OpaqueSignature{
			Provider: model.SignatureProviderAnthropic,
			Kind:     model.OpaqueSignatureKindAnthropicThinking,
			Value:    *thinkingSignature,
		})
	}

	choice := model.Choice{
		Index:        0,
		Message:      message,
		FinishReason: convertStopReason(resp.StopReason),
		StopSequence: resp.StopSequence,
		Citations:    citations,
	}

	result.Choices = []model.Choice{choice}
	result.Usage = convertAnthropicUsage(resp.Usage)

	o.logAnthropicSignatureAudit("extract", reasoningBlocks)

	return result
}

// convertStopReason parses Anthropic's stop_reason into the canonical
// FinishReason (model.FinishReasonFromAnthropic) and returns a *string for
// Choice.FinishReason. Rich reasons such as "pause_turn" / "refusal" are
// preserved so downstream inbounds can distinguish them from a plain stop.
func convertStopReason(stopReason *string) *string {
	if stopReason == nil {
		return nil
	}
	reason := model.FinishReasonFromAnthropic(*stopReason)
	if reason.IsZero() {
		return nil
	}
	s := reason.String()
	return &s
}

// mapAnthropicErrorTypeToStatus maps Anthropic API error `type` strings to HTTP
// status codes so streaming error events can be surfaced with the correct code.
// Reference: https://docs.anthropic.com/en/api/errors
func mapAnthropicErrorTypeToStatus(errType string) int {
	switch errType {
	case "invalid_request_error":
		return http.StatusBadRequest
	case "authentication_error":
		return http.StatusUnauthorized
	case "permission_error":
		return http.StatusForbidden
	case "not_found_error":
		return http.StatusNotFound
	case "request_too_large":
		return http.StatusRequestEntityTooLarge
	case "rate_limit_error":
		return http.StatusTooManyRequests
	case "overloaded_error":
		return 529
	case "api_error":
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func convertAnthropicUsage(usage *anthropicModel.Usage) *model.Usage {
	if usage == nil {
		return nil
	}

	result := &model.Usage{
		PromptTokens:             usage.InputTokens,
		CompletionTokens:         usage.OutputTokens,
		TotalTokens:              usage.InputTokens + usage.OutputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
	}

	if usage.CacheCreation != nil {
		result.CacheCreation5mInputTokens = usage.CacheCreation.Ephemeral5mInputTokens
		result.CacheCreation1hInputTokens = usage.CacheCreation.Ephemeral1hInputTokens
	}

	if usage.CacheReadInputTokens > 0 {
		result.PromptTokensDetails = &model.PromptTokensDetails{
			CachedTokens: usage.CacheReadInputTokens,
		}
	}
	return result
}
