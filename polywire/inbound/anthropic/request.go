package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/samber/lo"
	"github.com/suprelory/octopus-plus/polywire/compat"
	"github.com/suprelory/octopus-plus/polywire/model"
	wire "github.com/suprelory/octopus-plus/polywire/protocol/anthropic"
)

func (i *MessagesInbound) TransformRequest(ctx context.Context, body []byte) (*model.InternalLLMRequest, error) {
	var anthropicReq MessageRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return nil, err
	}
	originalMaxTokens := anthropicReq.MaxTokens
	if anthropicReq.MaxTokens < 1 {
		anthropicReq.MaxTokens = 1
	}
	i.requestModel = strings.TrimSpace(anthropicReq.Model)
	chatReq := &model.InternalLLMRequest{
		RequestType:         model.RequestTypeChat,
		Model:               anthropicReq.Model,
		MaxTokens:           &anthropicReq.MaxTokens,
		Temperature:         anthropicReq.Temperature,
		TopP:                anthropicReq.TopP,
		TopK:                anthropicReq.TopK,
		Stream:              anthropicReq.Stream,
		Metadata:            map[string]string{},
		RawAPIFormat:        model.APIFormatAnthropicMessage,
		TransformerMetadata: map[string]string{},
	}
	if err := chatReq.CaptureFieldPresence(body); err != nil {
		return nil, err
	}
	if originalMaxTokens < 1 {
		chatReq.SetTransformerMetadataValue(model.TransformerMetadataAnthropicMaxTokensRepairFrom, strconv.FormatInt(originalMaxTokens, 10))
	}
	if tier := strings.TrimSpace(anthropicReq.ServiceTier); tier != "" {
		chatReq.ServiceTier = &tier
	}
	if anthropicReq.Metadata != nil {
		if userID := strings.TrimSpace(anthropicReq.Metadata.UserID); userID != "" {
			chatReq.SetTransformerMetadataValue(model.TransformerMetadataAnthropicUserID, userID)
		}
	}
	// mcp_servers / container (A-H6): preserve the raw payload for
	// round-trip on the Anthropic→Anthropic same-protocol path. Triggers
	// the mcp-client-2025-11-20 beta header downstream (A-H7).
	if len(anthropicReq.MCPServers) > 0 || len(anthropicReq.Container) > 0 {
		chatReq.SetAnthropicExtensions(model.AnthropicExtension{
			MCPServers: anthropicReq.MCPServers,
			Container:  anthropicReq.Container,
		})
	}

	// Convert messages
	messages := make([]model.Message, 0, len(anthropicReq.Messages))

	// Add system message if present
	if anthropicReq.System != nil {
		if anthropicReq.System.Prompt != nil {
			systemContent := anthropicReq.System.Prompt
			messages = append(messages, model.Message{
				Role: "system",
				Content: model.MessageContent{
					Content: systemContent,
				},
			})
			i.inputToken += int64(i.config.CountTokens(*systemContent, chatReq.Model))
		} else if len(anthropicReq.System.MultiplePrompts) > 0 {
			// Mark that system was originally in array format
			chatReq.SetTransformerMetadataValue(model.TransformerMetadataAnthropicSystemArrayFormat, "true")

			for _, prompt := range anthropicReq.System.MultiplePrompts {
				msg := model.Message{
					Role: "system",
					Content: model.MessageContent{
						Content: &prompt.Text,
					},
					CacheControl: convertToLLMCacheControl(prompt.CacheControl),
				}
				i.inputToken += int64(i.config.CountTokens(prompt.Text, chatReq.Model))
				messages = append(messages, msg)
			}
		}
	}

	// Convert Anthropic messages to ChatCompletionMessage
	for msgIndex, msg := range anthropicReq.Messages {
		chatMsg := model.Message{
			Role: msg.Role,
		}

		var (
			hasContent    bool
			hasToolResult bool
		)

		// Convert content

		if msg.Content.Content != nil {
			chatMsg.Content = model.MessageContent{
				Content: msg.Content.Content,
			}
			hasContent = true
			i.inputToken += int64(i.config.CountTokens(*msg.Content.Content, chatReq.Model))
		} else if len(msg.Content.MultipleContent) > 0 {
			contentParts := make([]model.MessageContentPart, 0, len(msg.Content.MultipleContent))

			var (
				reasoningContent      string
				hasReasoningInContent bool
			)

			var reasoningSignature string
			pendingGeminiThoughtSignatures := make([]string, 0)

			for _, block := range msg.Content.MultipleContent {
				switch block.Type {
				case "thinking":
					if sig := geminiThoughtSignatureShim(block); sig != "" {
						pendingGeminiThoughtSignatures = append(pendingGeminiThoughtSignatures, sig)
						chatMsg.AppendReasoningBlock(model.ReasoningBlock{
							Kind:      model.ReasoningBlockKindSignature,
							Index:     -1,
							Signature: sig,
							Provider:  "gemini",
						})
						continue
					}

					// Keep thinking content in MultipleContent to preserve order
					thinkingText := ""
					if block.Thinking != nil && *block.Thinking != "" {
						thinkingText = *block.Thinking
						reasoningContent = thinkingText
						hasReasoningInContent = true
					}

					sig := ""
					if block.Signature != nil && *block.Signature != "" {
						sig = *block.Signature
						reasoningSignature = sig
					}

					// Preserve per-block provenance so multi-thinking-block assistant turns can
					// be replayed to Anthropic without flattening to a single signature.
					chatMsg.AppendReasoningBlock(model.ReasoningBlock{
						Kind:      model.ReasoningBlockKindThinking,
						Index:     -1,
						Text:      thinkingText,
						Signature: sig,
						Provider:  "anthropic",
					})
				case "redacted_thinking":
					if block.Data != "" {
						chatMsg.RedactedThinkingBlocks = append(chatMsg.RedactedThinkingBlocks, block.Data)
						chatMsg.AppendReasoningBlock(model.ReasoningBlock{
							Kind:     model.ReasoningBlockKindRedacted,
							Index:    -1,
							Data:     block.Data,
							Provider: "anthropic",
						})
						hasContent = true
					}
				case "text":
					contentParts = append(contentParts, model.MessageContentPart{
						Type:         "text",
						Text:         block.Text,
						Citations:    compat.AnthropicCitationsToModel(block.Citations),
						CacheControl: convertToLLMCacheControl(block.CacheControl),
					})
					if block.Text != nil {
						i.inputToken += int64(i.config.CountTokens(*block.Text, chatReq.Model))
					}
					hasContent = true
				case "image":
					if block.Source != nil {
						part := model.MessageContentPart{
							Type:         "image_url",
							CacheControl: convertToLLMCacheControl(block.CacheControl),
						}
						if block.Source.Type == "base64" {
							// Convert Anthropic image format to OpenAI format
							imageURL := fmt.Sprintf("data:%s;base64,%s", block.Source.MediaType, block.Source.Data)
							part.ImageURL = &model.ImageURL{
								URL: imageURL,
							}
						} else {
							part.ImageURL = &model.ImageURL{
								URL: block.Source.URL,
							}
						}

						contentParts = append(contentParts, part)
						hasContent = true
					}
				case "tool_result":
					hasToolResult = true
					toolMsg := model.Message{
						Role:            "tool",
						MessageIndex:    lo.ToPtr(msgIndex),
						ToolCallID:      block.ToolUseID,
						CacheControl:    convertToLLMCacheControl(block.CacheControl),
						ToolCallIsError: block.IsError,
					}

					if block.Content != nil {
						if block.Content.Content != nil {
							toolMsg.Content = model.MessageContent{
								Content: block.Content.Content,
							}
						} else if len(block.Content.MultipleContent) > 0 {
							// Handle multiple content blocks in tool_result
							// Keep as MultipleContent to preserve the original format
							toolContentParts := make([]model.MessageContentPart, 0, len(block.Content.MultipleContent))
							for _, contentBlock := range block.Content.MultipleContent {
								if contentBlock.Type == "text" {
									toolContentParts = append(toolContentParts, model.MessageContentPart{
										Type: "text",
										Text: contentBlock.Text,
									})
									i.inputToken += int64(i.config.CountTokens(*contentBlock.Text, chatReq.Model))
								}
							}

							toolMsg.Content = model.MessageContent{
								MultipleContent: toolContentParts,
							}
						}
					}

					messages = append(messages, toolMsg)
				case "tool_use":
					toolCall := model.ToolCall{
						ID:   block.ID,
						Type: "function",
						Function: model.FunctionCall{
							Name:      lo.FromPtr(block.Name),
							Arguments: string(block.Input),
						},
						CacheControl: convertToLLMCacheControl(block.CacheControl),
					}
					if len(pendingGeminiThoughtSignatures) > 0 {
						toolCall.ThoughtSignature = pendingGeminiThoughtSignatures[0]
						pendingGeminiThoughtSignatures = pendingGeminiThoughtSignatures[1:]
					} else if sig := i.restoreGeminiThoughtSignature(i.geminiSignatureScope(ctx, chatReq.Model), toolCall.ID, toolCall.Function.Name); sig != "" {
						toolCall.ThoughtSignature = sig
					}
					chatMsg.ToolCalls = append(chatMsg.ToolCalls, toolCall)
					hasContent = true
				case "document":
					part := convertDocumentBlockToLLM(block)
					if part != nil {
						contentParts = append(contentParts, *part)
						hasContent = true
					}
				default:
					if wire.IsServerToolUse(block.Type) {
						contentParts = append(contentParts, model.MessageContentPart{
							Type: "server_tool_use", ServerToolUse: compat.AnthropicServerToolUseToModel(block),
							CacheControl: convertToLLMCacheControl(block.CacheControl),
						})
						hasContent = true
					} else if wire.IsServerToolResult(block.Type) {
						contentParts = append(contentParts, model.MessageContentPart{
							Type: "server_tool_result", ServerToolResult: compat.AnthropicServerToolResultToModel(block),
							CacheControl: convertToLLMCacheControl(block.CacheControl),
						})
						hasContent = true
					}
				}
			}

			// Check if it's a simple text-only message (single text block)
			if len(contentParts) == 1 && contentParts[0].Type == "text" && len(contentParts[0].Citations) == 0 {
				// Convert single text block to simple content format for compatibility
				chatMsg.Content = model.MessageContent{
					Content: contentParts[0].Text,
				}
				// Preserve cache control at message level when simplifying
				if contentParts[0].CacheControl != nil {
					chatMsg.CacheControl = contentParts[0].CacheControl
				}

				hasContent = true
			} else if len(contentParts) > 0 {
				chatMsg.Content = model.MessageContent{
					MultipleContent: contentParts,
				}
				hasContent = true
			}

			if hasReasoningInContent || reasoningSignature != "" || len(chatMsg.ReasoningBlocks) > 0 {
				hasContent = true
			}

			// Assign reasoning content and signature if present
			if reasoningContent != "" && hasReasoningInContent {
				chatMsg.ReasoningContent = &reasoningContent
			}

			if reasoningSignature != "" {
				chatMsg.SetOpaqueReasoningSignature(model.OpaqueSignature{
					Provider: model.SignatureProviderAnthropic,
					Kind:     model.OpaqueSignatureKindAnthropicThinking,
					Value:    reasoningSignature,
				})
			}
		}

		if !hasContent {
			continue
		}

		// If this message had tool_result blocks, set MessageIndex so we can match it later
		if hasToolResult {
			chatMsg.MessageIndex = lo.ToPtr(msgIndex)
		}

		messages = append(messages, chatMsg)
	}

	chatReq.Messages = messages

	// Convert tools
	if len(anthropicReq.Tools) > 0 {
		tools := make([]model.Tool, 0, len(anthropicReq.Tools))
		for _, tool := range anthropicReq.Tools {
			if tool.IsServerTool() {
				// Server-side tool (web_search_*, code_execution_*, computer_*).
				// Preserve the raw spec body so the outbound path can replay
				// the wire payload verbatim; Type drives beta header selection.
				llmTool := model.Tool{
					Type: tool.Type,
					Function: model.Function{
						Name: tool.Name,
					},
					CacheControl:        convertToLLMCacheControl(tool.CacheControl),
					AnthropicServerSpec: tool.RawBody,
				}
				tools = append(tools, llmTool)
				i.inputToken += int64(i.config.CountTokens(tool.Name, chatReq.Model))
				continue
			}
			llmTool := model.Tool{
				Type: "function",
				Function: model.Function{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.InputSchema,
				},
				CacheControl: convertToLLMCacheControl(tool.CacheControl),
			}
			tools = append(tools, llmTool)
			i.inputToken += int64(i.config.CountTokens(tool.Name, chatReq.Model))
			i.inputToken += int64(i.config.CountTokens(tool.Description, chatReq.Model))
			i.inputToken += int64(i.config.CountTokens(string(tool.InputSchema), chatReq.Model))
		}
		i.inputToken += int64(len(tools) * 3)

		chatReq.Tools = tools
	}

	// Convert tool_choice
	if anthropicReq.ToolChoice != nil {
		chatReq.ToolChoice = convertToolChoiceFromAnthropic(anthropicReq.ToolChoice)
	}

	// Convert stop sequences
	if len(anthropicReq.StopSequences) > 0 {
		if len(anthropicReq.StopSequences) == 1 {
			chatReq.Stop = &model.Stop{
				Stop: &anthropicReq.StopSequences[0],
			}
		} else {
			chatReq.Stop = &model.Stop{
				MultipleStop: anthropicReq.StopSequences,
			}
		}
	}

	// Convert thinking configuration to reasoning effort and preserve budget
	if anthropicReq.Thinking != nil {
		if anthropicReq.Thinking.Display != "" {
			chatReq.ThinkingDisplay = anthropicReq.Thinking.Display
		}
		switch anthropicReq.Thinking.Type {
		case ThinkingTypeEnabled:
			if anthropicReq.Thinking.BudgetTokens != nil {
				chatReq.ReasoningEffort = thinkingBudgetToReasoningEffort(*anthropicReq.Thinking.BudgetTokens)
				chatReq.ReasoningBudget = anthropicReq.Thinking.BudgetTokens
			} else {
				i.config.Log().Warnf("thinking type is 'enabled' but budget_tokens is nil, thinking will be ignored")
			}
		case ThinkingTypeAdaptive:
			effort := EffortHigh
			if anthropicReq.OutputConfig != nil && anthropicReq.OutputConfig.Effort != "" {
				effort = anthropicReq.OutputConfig.Effort
			}
			chatReq.ReasoningEffort = effort
			chatReq.AdaptiveThinking = true
		case ThinkingTypeDisabled:
			// Explicitly disabled, nothing to do
		default:
			i.config.Log().Warnf("unknown thinking type: %s", anthropicReq.Thinking.Type)
		}
	}
	if err := chatReq.NormalizeOperation(); err != nil {
		return nil, err
	}
	if err := chatReq.CaptureRequestRecovery(body, anthropicReq); err != nil {
		return nil, err
	}
	chatReq.EstimatedInputTokens = i.inputToken
	return chatReq, nil
}

// convertToolChoiceFromAnthropic converts the wire-level Anthropic
// ToolChoice into the provider-agnostic internal representation. The string
// form is used for {auto,none,any} which are the simple modes; the named
// form preserves `tool + name` (Anthropic) and `disable_parallel_tool_use`
// so outbound emitters can reproduce them verbatim when the upstream is
// also Anthropic.
func convertToolChoiceFromAnthropic(src *ToolChoice) *model.ToolChoice {
	if src == nil {
		return nil
	}
	switch src.Type {
	case "auto", "none", "any":
		if src.DisableParallelToolUse == nil {
			mode := src.Type
			return &model.ToolChoice{ToolChoice: &mode}
		}
		return &model.ToolChoice{
			NamedToolChoice: &model.NamedToolChoice{
				Type:                   src.Type,
				DisableParallelToolUse: src.DisableParallelToolUse,
			},
		}
	case "tool":
		named := &model.NamedToolChoice{
			Type:                   "tool",
			DisableParallelToolUse: src.DisableParallelToolUse,
		}
		if src.Name != nil {
			name := *src.Name
			named.Name = &name
			named.Function = &model.ToolFunction{Name: name}
		}
		return &model.ToolChoice{NamedToolChoice: named}
	default:
		return nil
	}
}

// convertDocumentBlockToLLM maps an Anthropic document content block into
// an internal MessageContentPart of type "document". The wire `source`
// carries either a base64/url/text payload or a pre-chunked content array;
// Title / Context / Citations metadata is preserved verbatim.
func convertDocumentBlockToLLM(block MessageContentBlock) *model.MessageContentPart {
	if block.Source == nil {
		return nil
	}
	doc := &model.DocumentSource{
		Type:      block.Source.Type,
		MediaType: block.Source.MediaType,
		Data:      block.Source.Data,
		URL:       block.Source.URL,
		Content:   block.Source.Content,
		Title:     block.Title,
		Context:   block.Context,
	}
	// The wire shape carries text in source.data when type == "text"; split
	// it out into the dedicated Text field so converters can distinguish
	// raw text from a base64 blob.
	if doc.Type == "text" {
		doc.Text = doc.Data
		doc.Data = ""
	}
	if block.CitationConfig != nil {
		doc.Citations = &model.DocumentCitations{Enabled: block.CitationConfig.Enabled}
	}
	return &model.MessageContentPart{
		Type:         "document",
		Document:     doc,
		CacheControl: convertToLLMCacheControl(block.CacheControl),
	}
}
