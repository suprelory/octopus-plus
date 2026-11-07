package gemini

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func (o *MessagesOutbound) convertGeminiToLLMResponse(geminiResp *model.GeminiGenerateContentResponse, isStream bool, streamIndexer func(candidateIndex int) int, toolIndexers ...func() int) *model.InternalLLMResponse {
	resp := &model.InternalLLMResponse{
		Choices: []model.Choice{},
	}

	if isStream {
		resp.Object = "chat.completion.chunk"
	} else {
		resp.Object = "chat.completion"
	}

	if geminiResp.ResponseId != "" {
		resp.ID = geminiResp.ResponseId
	}
	if geminiResp.ModelVersion != "" {
		resp.Model = geminiResp.ModelVersion
	}
	if geminiResp.CreateTime != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, geminiResp.CreateTime); err == nil {
			resp.Created = parsed.Unix()
		} else if parsed, err := time.Parse(time.RFC3339, geminiResp.CreateTime); err == nil {
			resp.Created = parsed.Unix()
		}
	}

	// Convert candidates to choices
	for _, candidate := range geminiResp.Candidates {
		if candidate == nil {
			continue
		}
		choice := model.Choice{
			Index: candidate.Index,
		}

		// nextReasoningIndex returns the Index to stamp on the next
		// ReasoningBlock for this candidate. For streaming, it draws from the
		// outbound's per-candidate counter so signatures bind to the right
		// thinking block across chunks (G-C4). For non-streaming, it falls
		// back to the local slice length as before.
		nextReasoningIndex := func() int {
			if streamIndexer != nil {
				return streamIndexer(candidate.Index)
			}
			// local fallback — len of whatever we've appended so far
			// (captured via closure below).
			return -1
		}

		// Convert finish reason
		if candidate.FinishReason != nil {
			reason := convertGeminiFinishReason(*candidate.FinishReason)
			choice.FinishReason = &reason
		}

		// Convert content
		if candidate.Content != nil {
			msg := &model.Message{
				Role: "assistant",
			}

			// Extract text, images and function calls from parts
			var textParts []string
			var contentParts []model.MessageContentPart
			var toolCalls []model.ToolCall
			var reasoningContent *string
			// hasStructuredPart flags parts that cannot be serialised as a
			// plain string (inline data, server_tool_use, server_tool_result).
			// When true the message must use MultipleContent instead of the
			// scalar Content field, or the structured parts are silently
			// dropped. G-H9.
			var hasStructuredPart bool
			var reasoningBlocks []model.ReasoningBlock
			assignIndex := func() int {
				if idx := nextReasoningIndex(); idx >= 0 {
					return idx
				}
				return len(reasoningBlocks)
			}

			for idx, part := range candidate.Content.Parts {
				if part == nil {
					continue
				}
				if part.Thought {
					// Handle thinking/reasoning content
					if part.Text != "" && reasoningContent == nil {
						reasoningContent = &part.Text
					}
					// Thought Parts in Gemini 3 may carry a thoughtSignature that must be
					// replayed verbatim on the next turn.
					if part.Text != "" || part.ThoughtSignature != "" {
						reasoningBlocks = append(reasoningBlocks, geminiReasoningBlock(
							model.ReasoningBlockKindThinking,
							assignIndex(),
							part.Text,
							part.ThoughtSignature,
							"",
							"",
						))
					}
				} else if part.Text != "" {
					textParts = append(textParts, part.Text)
					// Also add to content parts for multimodal response
					text := part.Text
					contentParts = append(contentParts, model.MessageContentPart{
						Type: "text",
						Text: &text,
					})
					if part.ThoughtSignature != "" {
						reasoningBlocks = append(reasoningBlocks, geminiReasoningBlock(
							model.ReasoningBlockKindSignature,
							assignIndex(),
							"",
							part.ThoughtSignature,
							"",
							"",
						))
					}
				}
				// Handle inline data (images, audio, etc.)
				if part.InlineData != nil {
					if strings.HasPrefix(strings.ToLower(part.InlineData.MimeType), "audio/") {
						resp.NonChatStreamEvents = append(resp.NonChatStreamEvents, model.StreamEvent{Kind: model.StreamEventKindAudioDelta, ID: resp.ID, Model: resp.Model, Index: candidate.Index, Media: &model.StreamMedia{MediaType: part.InlineData.MimeType, Data: part.InlineData.Data}})
						continue
					}
					hasStructuredPart = true
					// Convert to data URL format: data:{mimeType};base64,{data}
					dataURL := fmt.Sprintf("data:%s;base64,%s", part.InlineData.MimeType, part.InlineData.Data)
					contentParts = append(contentParts, model.MessageContentPart{
						Type: "image_url",
						ImageURL: &model.ImageURL{
							URL: dataURL,
						},
					})
				}
				if part.FunctionCall != nil {
					toolIndex := idx
					if isStream && len(toolIndexers) > 0 {
						toolIndex = toolIndexers[0]()
					}
					argsJSON, _ := json.Marshal(part.FunctionCall.Args)
					toolCallID := geminiFunctionCallID(part.FunctionCall, toolIndex, geminiResp.ResponseId, part.ThoughtSignature)
					toolCall := model.ToolCall{
						Index: toolIndex,
						ID:    toolCallID,
						Type:  "function",
						Function: model.FunctionCall{
							Name:      part.FunctionCall.Name,
							Arguments: string(argsJSON),
						},
						ThoughtSignature:   part.ThoughtSignature,
						ProviderExtensions: geminiThoughtSignatureProviderExtension(part.ThoughtSignature),
					}
					toolCalls = append(toolCalls, toolCall)
					if part.ThoughtSignature != "" {
						// Anchor signatures to their originating functionCall so
						// the outbound replay can reconstruct the mapping by
						// name (G-H7) instead of relying on ordinal position —
						// multi-tool turns otherwise swap signatures and Gemini
						// rejects the request with 400.
						reasoningBlocks = append(reasoningBlocks, geminiReasoningBlock(
							model.ReasoningBlockKindSignature,
							assignIndex(),
							"",
							part.ThoughtSignature,
							toolCallID,
							part.FunctionCall.Name,
						))
					}
				}

				// ExecutableCode / CodeExecutionResult (G-H9): Gemini emits
				// these parts when the sandboxed code_execution tool runs.
				// We fold them into the cross-provider ServerToolUse /
				// ServerToolResult envelopes with BlockType="code_execution"
				// so the existing passthrough infrastructure (P1.1) carries
				// them through. ID ties use→result for clients that care.
				if part.ExecutableCode != nil {
					input, _ := json.Marshal(map[string]any{
						"language": part.ExecutableCode.Language,
						"code":     part.ExecutableCode.Code,
					})
					codeID := fmt.Sprintf("gemini_code_exec_%d", idx)
					hasStructuredPart = true
					contentParts = append(contentParts, model.MessageContentPart{
						Type: "server_tool_use",
						ServerToolUse: &model.ServerToolUseBlock{
							ID:    codeID,
							Name:  "code_execution",
							Input: input,
						},
					})
				}
				if part.CodeExecutionResult != nil {
					resultPayload, _ := json.Marshal(map[string]any{
						"outcome": part.CodeExecutionResult.Outcome,
						"output":  part.CodeExecutionResult.Output,
					})
					isError := part.CodeExecutionResult.Outcome != "" &&
						part.CodeExecutionResult.Outcome != "OUTCOME_OK" &&
						part.CodeExecutionResult.Outcome != "OUTCOME_UNSPECIFIED"
					hasStructuredPart = true
					contentParts = append(contentParts, model.MessageContentPart{
						Type: "server_tool_result",
						ServerToolResult: &model.ServerToolResultBlock{
							Content:   resultPayload,
							IsError:   &isError,
							BlockType: "code_execution_tool_result",
						},
					})
				}
			}

			// Set content - use MultipleContent if we have any structured
			// parts (inline data or server-tool blocks), otherwise fall back
			// to the scalar string form. G-H9 widened this from
			// hasInlineData-only so code_execution parts aren't dropped.
			//
			// Text parts are also already appended to contentParts in order
			// (see the text branch above), so the multipart path preserves
			// the full sequence.
			if hasStructuredPart {
				msg.Content = model.MessageContent{
					MultipleContent: contentParts,
				}
			} else if len(textParts) > 0 {
				text := strings.Join(textParts, "")
				msg.Content = model.MessageContent{
					Content: &text,
				}
			}

			// Set reasoning content
			if reasoningContent != nil {
				msg.ReasoningContent = reasoningContent
			}

			// Preserve Gemini thoughtSignatures in the order they arrived so outbound can
			// replay them verbatim on the next turn (mandatory for Gemini 3 function calls).
			if len(reasoningBlocks) > 0 {
				msg.ReasoningBlocks = reasoningBlocks
				o.logGeminiSignatureAudit("extract", reasoningBlocks)
			}

			// Set tool calls
			if len(toolCalls) > 0 {
				msg.ToolCalls = toolCalls
				if choice.FinishReason == nil && !isStream {
					reason := "tool_calls"
					choice.FinishReason = &reason
				}
			}

			if isStream {
				choice.Delta = msg
			} else {
				choice.Message = msg
			}
		}

		// Grounding / citations / URL context / safety ratings (G-H10, G-M9).
		// Populated on the Choice directly so consumers can surface them
		// without parsing the provider-native payload.
		if g := convertGeminiGroundingToInternal(candidate.GroundingMetadata); g != nil {
			choice.Grounding = g
		}
		if cites := convertGeminiCitationsToInternal(candidate.CitationMetadata); len(cites) > 0 {
			choice.Citations = cites
		}
		if u := convertGeminiURLContextToInternal(candidate.UrlContextMetadata); u != nil {
			choice.URLContext = u
		}
		if ratings := convertGeminiSafetyRatings(candidate.SafetyRatings); len(ratings) > 0 {
			choice.SafetyRatings = ratings
		}

		resp.Choices = append(resp.Choices, choice)
	}

	// Convert usage metadata
	resp.Usage = convertGeminiUsageMetadata(geminiResp.UsageMetadata)

	// When the prompt is blocked Gemini returns no candidates, only
	// promptFeedback.blockReason. Surface a synthetic choice so downstream
	// inbounds can translate to a proper content_filter / refusal finish
	// reason instead of returning an empty 200.
	if len(geminiResp.Candidates) == 0 && geminiResp.PromptFeedback != nil && geminiResp.PromptFeedback.BlockReason != "" {
		reason := model.FinishReasonFromGemini(geminiResp.PromptFeedback.BlockReason).String()
		if reason == "" {
			reason = string(model.FinishReasonContentFilter)
		}
		synthetic := model.Choice{
			Index:        0,
			Message:      &model.Message{Role: "assistant"},
			FinishReason: &reason,
		}
		// Promote promptFeedback.safetyRatings onto the synthetic choice so
		// the reason for the block is discoverable downstream. G-M9.
		if ratings := convertGeminiSafetyRatings(geminiResp.PromptFeedback.SafetyRatings); len(ratings) > 0 {
			synthetic.SafetyRatings = ratings
		}
		resp.Choices = append(resp.Choices, synthetic)
	}

	return resp
}
