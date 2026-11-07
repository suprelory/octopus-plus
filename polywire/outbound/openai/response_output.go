package openai

import (
	"strings"

	"github.com/samber/lo"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func convertToLLMResponseFromResponses(resp *ResponsesResponse) *model.InternalLLMResponse {
	if resp == nil {
		return &model.InternalLLMResponse{
			Object: "chat.completion",
		}
	}

	result := &model.InternalLLMResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Model:   resp.Model,
		Created: resp.CreatedAt,
	}
	if len(resp.Output) > 0 {
		if rawOutput, err := marshalResponsesOutputItems(resp.Output); err == nil {
			result.RawResponsesOutputItems = rawOutput
		}
	}

	var (
		contentParts     []model.MessageContentPart
		textContent      strings.Builder
		refusalContent   strings.Builder
		reasoningContent strings.Builder
		reasoningBlocks  []model.ReasoningBlock
		toolCalls        []model.ToolCall
	)

	for _, outputItem := range resp.Output {
		switch outputItem.Type {
		case "message":
			if outputItem.Content != nil {
				for _, item := range outputItem.Content.Items {
					switch item.Type {
					case "output_text":
						if item.Text != nil {
							textContent.WriteString(*item.Text)
						}
					case "refusal":
						if item.Refusal != nil {
							refusalContent.WriteString(*item.Refusal)
						} else if item.Text != nil {
							refusalContent.WriteString(*item.Text)
						}
					}
				}
			}
		case "output_text":
			if outputItem.Text != nil {
				textContent.WriteString(*outputItem.Text)
			}
		case "function_call":
			toolCalls = append(toolCalls, model.ToolCall{
				ID:   outputItem.CallID,
				Type: "function",
				Function: model.FunctionCall{
					Name:      outputItem.Name,
					Namespace: outputItem.Namespace,
					Arguments: outputItem.Arguments,
				},
			})
		case "reasoning":
			text := reasoningTextFromResponsesItem(outputItem)
			if text != "" {
				reasoningContent.WriteString(text)
			}
			blockKind := model.ReasoningBlockKindSignature
			if text != "" {
				blockKind = model.ReasoningBlockKindThinking
			}
			block := model.ReasoningBlock{Kind: blockKind, Index: len(reasoningBlocks), Text: text}
			if outputItem.EncryptedContent != nil && *outputItem.EncryptedContent != "" {
				signature := model.OpaqueSignature{
					Provider: model.SignatureProviderOpenAI,
					Kind:     model.OpaqueSignatureKindOpenAIReasoning,
					Value:    *outputItem.EncryptedContent,
				}
				block.SetOpaqueSignature(signature)
			}
			if text != "" || block.SignatureSource != nil {
				reasoningBlocks = append(reasoningBlocks, block)
			}
		case "image_generation_call":
			if outputItem.Result != nil && *outputItem.Result != "" {
				outputFormat := "png"
				if outputItem.OutputFormat != nil {
					outputFormat = *outputItem.OutputFormat
				}
				contentParts = append(contentParts, model.MessageContentPart{
					Type: "image_url",
					ImageURL: &model.ImageURL{
						URL: "data:image/" + outputFormat + ";base64," + *outputItem.Result,
					},
				})
			}
		}
	}

	choice := model.Choice{
		Index: 0,
		Message: &model.Message{
			Role:            "assistant",
			ToolCalls:       toolCalls,
			ReasoningBlocks: reasoningBlocks,
		},
	}

	// Set reasoning content if present
	if reasoningContent.Len() > 0 {
		choice.Message.ReasoningContent = lo.ToPtr(reasoningContent.String())
	}
	if len(reasoningBlocks) > 0 {
		if signature, ok := reasoningBlocks[len(reasoningBlocks)-1].OpaqueSignature(); ok {
			choice.Message.SetOpaqueReasoningSignature(signature)
		}
	}
	if refusalContent.Len() > 0 {
		choice.Message.Refusal = refusalContent.String()
	}

	// Set message content
	if textContent.Len() > 0 {
		if len(contentParts) > 0 {
			textPart := model.MessageContentPart{
				Type: "text",
				Text: lo.ToPtr(textContent.String()),
			}
			contentParts = append([]model.MessageContentPart{textPart}, contentParts...)
			choice.Message.Content = model.MessageContent{
				MultipleContent: contentParts,
			}
		} else {
			choice.Message.Content = model.MessageContent{
				Content: lo.ToPtr(textContent.String()),
			}
		}
	} else if len(contentParts) > 0 {
		choice.Message.Content = model.MessageContent{
			MultipleContent: contentParts,
		}
	}

	// Set finish reason based on status
	if len(toolCalls) > 0 {
		choice.FinishReason = lo.ToPtr("tool_calls")
	} else {
		finishReason, respErr := normalizeResponsesFinishReason(resp.Status, resp.Error)
		choice.FinishReason = finishReason
		if respErr != nil {
			result.Error = respErr
		}
	}

	result.Choices = []model.Choice{choice}
	result.Usage = convertResponsesUsage(resp.Usage)

	return result
}

func reasoningTextFromResponsesItem(item ResponsesItem) string {
	if item.Content != nil {
		var content strings.Builder
		for _, part := range item.Content.Items {
			if part.Type == "reasoning_text" && part.Text != nil {
				content.WriteString(*part.Text)
			}
		}
		if content.Len() > 0 {
			return content.String()
		}
	}

	var summary strings.Builder
	for _, part := range item.Summary {
		summary.WriteString(part.Text)
	}
	return summary.String()
}

// normalizeResponsesFinishReason maps an OpenAI Responses API `status` value
// to a legal OpenAI Chat Completions finish_reason. The Chat schema enum is
// stop | length | tool_calls | content_filter | function_call; emitting
// anything else (the historical "error" value we used) is rejected outright
// by strict downstream SDKs such as OpenAI Python / Pydantic AI.
//
// When the upstream carries a ResponsesError on failure / incomplete turns
// we synthesise a ResponseError so the inbound layer can surface the
// original cause to the client via the final chunk payload. Tool-call
// driven completions are handled by the caller before this helper runs;
// content_filter / nuanced mappings (incomplete_details.reason) will land
// with O-M1.
func normalizeResponsesFinishReason(status *string, errDetail *ResponsesError) (*string, *model.ResponseError) {
	var respErr *model.ResponseError
	if errDetail != nil {
		respErr = &model.ResponseError{
			StatusCode: 502,
			Detail: model.ErrorDetail{
				Code:    NormalizeStreamErrorCode(errDetail.Code),
				Type:    errDetail.Type,
				Message: errDetail.Message,
			},
		}
	}

	if status == nil {
		return nil, respErr
	}
	switch *status {
	case "completed":
		return lo.ToPtr("stop"), respErr
	case "incomplete":
		return lo.ToPtr("length"), respErr
	case "failed", "cancelled", "canceled":
		if respErr == nil {
			respErr = &model.ResponseError{StatusCode: 502, Detail: model.ErrorDetail{Type: "upstream_error", Message: "OpenAI Responses stream ended with " + *status}}
		}
		return lo.ToPtr("stop"), respErr
	default:
		return nil, respErr
	}
}

// responseCarriesFunctionCall reports whether a terminal Responses event
// contains at least one function_call output item. Used by the streaming
// response.completed branch to override a "stop" finish_reason to
// "tool_calls" when the upstream chose to invoke a client-defined tool.
//
// The lookup prefers the fully-populated `response.output` Array attached
// to the completed event; when the upstream omits it (some OpenAI-compat
// upstreams do), we fall back to the items we have been tracking during
// the stream via mergeOutputItemAdded / mergeFunctionCallDelta.
func (o *ResponseOutbound) responseCarriesFunctionCall(resp *ResponsesResponse) bool {
	if resp != nil {
		for _, item := range resp.Output {
			if item.Type == "function_call" {
				return true
			}
		}
		if len(resp.Output) > 0 {
			return false
		}
	}
	if o == nil || len(o.outputItems) == 0 {
		return false
	}
	for _, item := range o.outputItems {
		if item.Type == "function_call" {
			return true
		}
	}
	return false
}

func convertResponsesUsage(usage *ResponsesUsage) *model.Usage {
	if usage == nil {
		return nil
	}

	result := &model.Usage{
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      usage.TotalTokens,
	}

	if usage.InputTokenDetails.CachedTokens > 0 {
		result.PromptTokensDetails = &model.PromptTokensDetails{
			CachedTokens: usage.InputTokenDetails.CachedTokens,
		}
	}

	if usage.OutputTokenDetails.ReasoningTokens > 0 {
		result.CompletionTokensDetails = &model.CompletionTokensDetails{
			ReasoningTokens: usage.OutputTokenDetails.ReasoningTokens,
		}
	}

	return result
}
