package anthropic

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/compat"
	"github.com/suprelory/octopus-plus/polywire/internal/dataurl"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func (i *MessagesInbound) TransformResponse(ctx context.Context, response *model.InternalLLMResponse) ([]byte, error) {
	// Store the response for later retrieval
	i.storedResponse = response

	resp := &Message{
		ID:    response.ID,
		Type:  "message",
		Role:  "assistant",
		Model: response.Model,
	}

	// Convert choices to content blocks
	if len(response.Choices) > 0 {
		choice := response.Choices[0]

		var message *model.Message

		if choice.Message != nil {
			message = choice.Message
		} else if choice.Delta != nil {
			message = choice.Delta
		}

		if message != nil {
			var contentBlocks []MessageContentBlock
			textBlockCitationCount := 0

			// Prefer per-block reasoning provenance when available so multiple thinking /
			// redacted_thinking blocks from the upstream can be replayed in order. Fall back to
			// the legacy flat fields when ReasoningBlocks is empty (non-Anthropic upstream).
			if len(message.ReasoningBlocks) > 0 {
				for _, rb := range message.ReasoningBlocks {
					switch rb.Kind {
					case model.ReasoningBlockKindThinking:
						signature := reasoningBlockSignatureForProvider(rb, model.SignatureProviderAnthropic, model.OpaqueSignatureKindAnthropicThinking)
						if rb.Text == "" && signature == "" {
							continue
						}
						thinking := rb.Text
						block := MessageContentBlock{Type: "thinking", Thinking: &thinking}
						if signature != "" {
							block.Signature = &signature
						}
						contentBlocks = append(contentBlocks, block)
					case model.ReasoningBlockKindRedacted:
						if rb.Data != "" {
							contentBlocks = append(contentBlocks, MessageContentBlock{
								Type: "redacted_thinking",
								Data: rb.Data,
							})
						}
					case model.ReasoningBlockKindSignature:
						if signature := reasoningBlockSignatureForProvider(rb, model.SignatureProviderAnthropic, model.OpaqueSignatureKindAnthropicThinking); signature != "" {
							for index := len(contentBlocks) - 1; index >= 0; index-- {
								if contentBlocks[index].Type == "thinking" && contentBlocks[index].Signature == nil {
									contentBlocks[index].Signature = &signature
									break
								}
							}
						} else if signature := geminiToolCallShimSignature(rb); signature != "" {
							thinking := ""
							contentBlocks = append(contentBlocks, MessageContentBlock{
								Type:      "thinking",
								Thinking:  &thinking,
								Signature: &signature,
							})
						}
					}
				}
			} else {
				// Handle reasoning content (thinking) first if present
				if message.ReasoningContent != nil && *message.ReasoningContent != "" {
					thinkingBlock := MessageContentBlock{
						Type:     "thinking",
						Thinking: message.ReasoningContent,
					}
					if signature := messageReasoningSignatureForProvider(message, model.SignatureProviderAnthropic, model.OpaqueSignatureKindAnthropicThinking); signature != "" {
						thinkingBlock.Signature = &signature
					}
					// No fallback magic string — if signature is absent (non-Anthropic upstream),
					// Signature remains nil and is omitted via omitempty.

					contentBlocks = append(contentBlocks, thinkingBlock)
				}

				// Handle redacted thinking blocks
				for _, data := range message.RedactedThinkingBlocks {
					contentBlocks = append(contentBlocks, MessageContentBlock{
						Type: "redacted_thinking",
						Data: data,
					})
				}
			}

			// Handle regular content
			if len(message.Content.MultipleContent) > 0 {
				for _, part := range message.Content.MultipleContent {
					switch part.Type {
					case "text":
						if part.Text != nil {
							block := MessageContentBlock{
								Type:      "text",
								Text:      part.Text,
								Citations: compat.AnthropicCitationsFromModel(part.Citations),
							}
							textBlockCitationCount += len(block.Citations)
							contentBlocks = append(contentBlocks, block)
						}
					case "image_url":
						if part.ImageURL != nil && part.ImageURL.URL != "" {
							// Convert OpenAI image format to Anthropic format
							url := part.ImageURL.URL
							if parsed := dataurl.ParseDataURL(url); parsed != nil {
								contentBlocks = append(contentBlocks, MessageContentBlock{
									Type: "image",
									Source: &ImageSource{
										Type:      "base64",
										MediaType: parsed.MediaType,
										Data:      parsed.Data,
									},
								})
							} else {
								contentBlocks = append(contentBlocks, MessageContentBlock{
									Type: "image",
									Source: &ImageSource{
										Type: "url",
										URL:  part.ImageURL.URL,
									},
								})
							}
						}
					case "server_tool_use":
						if part.ServerToolUse != nil {
							contentBlocks = append(contentBlocks, compat.AnthropicServerToolUseFromModel(part.ServerToolUse))
						}
					case "server_tool_result":
						if part.ServerToolResult != nil {
							contentBlocks = append(contentBlocks, compat.AnthropicServerToolResultFromModel(part.ServerToolResult))
						}
					}
				}
			} else if message.Content.Content != nil && *message.Content.Content != "" {
				contentBlocks = append(contentBlocks, MessageContentBlock{Type: "text", Text: message.Content.Content})
			}
			if message.Refusal != "" {
				refusal := message.Refusal
				contentBlocks = append(contentBlocks, MessageContentBlock{Type: "text", Text: &refusal})
			}

			// Handle tool calls
			if len(message.ToolCalls) > 0 {
				emittedSignatureShims := countGeminiSignatureShims(contentBlocks)
				for _, toolCall := range message.ToolCalls {
					var input json.RawMessage
					if toolCall.Function.Arguments != "" {
						// Attempt to use the provided arguments; repair if invalid, fallback to {}
						if json.Valid([]byte(toolCall.Function.Arguments)) {
							input = json.RawMessage(toolCall.Function.Arguments)
						} else {
							input = json.RawMessage("{}")
						}
					} else {
						input = json.RawMessage("{}")
					}

					block := MessageContentBlock{
						Type:  "tool_use",
						ID:    toolCall.ID,
						Name:  &toolCall.Function.Name,
						Input: input,
					}
					if sig := toolCall.GetGeminiExtensions().ThoughtSignature; strings.TrimSpace(sig) != "" {
						i.saveGeminiThoughtSignature(i.geminiSignatureScope(ctx, response.Model), toolCall.ID, toolCall.Function.Name, sig)
						if emittedSignatureShims >= len(message.ToolCalls) {
							contentBlocks = append(contentBlocks, block)
							continue
						}
						thinking := ""
						signature := sig
						contentBlocks = append(contentBlocks, MessageContentBlock{
							Type:      "thinking",
							Thinking:  &thinking,
							Signature: &signature,
						})
						emittedSignatureShims++
					}
					contentBlocks = append(contentBlocks, block)
				}
			}

			// Choice-level citations are a compatibility fallback for providers
			// that do not attach attribution to individual content parts.
			if textBlockCitationCount == 0 && len(choice.Citations) > 0 {
				for index := len(contentBlocks) - 1; index >= 0; index-- {
					if contentBlocks[index].Type == "text" {
						contentBlocks[index].Citations = compat.AnthropicCitationsFromModel(choice.Citations)
						break
					}
				}
			}

			resp.Content = contentBlocks
		}

		// Convert finish reason
		if choice.FinishReason != nil {
			reason := model.ParseFinishReason(*choice.FinishReason)
			if wire := reason.ToAnthropic(); wire != "" {
				resp.StopReason = &wire
			} else {
				resp.StopReason = choice.FinishReason
			}
		} else {
			stopReason := "end_turn"
			for _, block := range resp.Content {
				if block.Type == "tool_use" {
					stopReason = "tool_use"
					break
				}
			}
			resp.StopReason = &stopReason
		}
		if message != nil && message.Refusal != "" && resp.StopReason != nil && *resp.StopReason == "end_turn" {
			refusal := "refusal"
			resp.StopReason = &refusal
		}

		if choice.StopSequence != nil {
			resp.StopSequence = choice.StopSequence
		}
	}

	// Convert usage
	if response.Usage != nil {
		resp.Usage = i.convertUsage(response.Usage)
	}

	return json.Marshal(resp)
}

func (i *MessagesInbound) convertUsage(usage *model.Usage) *Usage {
	anthropicUsage := &Usage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
	}
	if usage.HasAnthropicCacheSemantic() {
		anthropicUsage.CacheCreationInputTokens = usage.CacheCreationInputTokens
		anthropicUsage.CacheReadInputTokens = usage.CacheReadInputTokens
		if usage.CacheCreation5mInputTokens > 0 || usage.CacheCreation1hInputTokens > 0 {
			anthropicUsage.CacheCreation = &CacheCreationUsage{
				Ephemeral5mInputTokens: usage.CacheCreation5mInputTokens,
				Ephemeral1hInputTokens: usage.CacheCreation1hInputTokens,
			}
		}
	} else if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens > 0 {
		anthropicUsage.CacheReadInputTokens = usage.PromptTokensDetails.CachedTokens
		anthropicUsage.InputTokens -= anthropicUsage.CacheReadInputTokens
		if anthropicUsage.InputTokens < 0 {
			anthropicUsage.InputTokens = 0
		}
	}
	return anthropicUsage
}
