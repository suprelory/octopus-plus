package openai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/samber/lo"
	"github.com/suprelory/octopus-plus/polywire/internal/dataurl"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func (i *ResponseInbound) TransformResponse(ctx context.Context, response *model.InternalLLMResponse) ([]byte, error) {
	if response == nil {
		return nil, fmt.Errorf("response is nil")
	}

	// Store the response for later retrieval
	i.storedResponse = response

	// Convert to Responses API format
	resp := convertToResponsesAPIResponse(response)
	if i.truncation != nil {
		resp.Truncation = i.truncation
	}

	body, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal responses api response: %w", err)
	}

	return body, nil
}

func convertToResponsesAPIResponse(resp *model.InternalLLMResponse) *ResponsesResponse {
	result := &ResponsesResponse{
		Object:    "response",
		ID:        resp.ID,
		Model:     resp.Model,
		CreatedAt: resp.Created,
		Output:    make([]ResponsesItem, 0),
		Status:    lo.ToPtr("completed"),
	}

	// Convert usage
	result.Usage = convertUsageToResponses(resp.Usage)

	// Convert choices to output items
	for _, choice := range resp.Choices {
		var message *model.Message
		if choice.Message != nil {
			message = choice.Message
		} else if choice.Delta != nil {
			message = choice.Delta
		}

		if message == nil {
			continue
		}

		result.Output = append(result.Output, openAIMessageReasoningItems(message)...)

		// Handle tool calls
		if len(message.ToolCalls) > 0 {
			for _, toolCall := range message.ToolCalls {
				result.Output = append(result.Output, ResponsesItem{
					ID:        toolCall.ID,
					Type:      "function_call",
					CallID:    toolCall.ID,
					Name:      toolCall.Function.Name,
					Namespace: toolCall.Function.Namespace,
					Arguments: toolCall.Function.Arguments,
					Status:    lo.ToPtr("completed"),
				})
			}
		}

		// Handle message content
		contentItems := make([]ResponsesItem, 0, 2)
		if message.Content.Content != nil && *message.Content.Content != "" {
			text := *message.Content.Content
			contentItems = append(contentItems, ResponsesItem{
				Type:        "output_text",
				Text:        &text,
				Annotations: &[]ResponsesAnnotation{},
			})
		} else if len(message.Content.MultipleContent) > 0 {

			for _, part := range message.Content.MultipleContent {
				switch part.Type {
				case "text":
					if part.Text != nil {
						text := *part.Text
						contentItems = append(contentItems, ResponsesItem{
							Type:        "output_text",
							Text:        &text,
							Annotations: &[]ResponsesAnnotation{},
						})
					}
				case "image_url":
					if part.ImageURL != nil {
						result.Output = append(result.Output, ResponsesItem{
							ID:     generateItemID(),
							Type:   "image_generation_call",
							Role:   "assistant",
							Result: lo.ToPtr(dataurl.ExtractBase64FromDataURL(part.ImageURL.URL)),
							Status: lo.ToPtr("completed"),
						})
					}
				}
			}
		}

		if message.Refusal != "" {
			refusal := message.Refusal
			contentItems = append(contentItems, ResponsesItem{
				Type:    "refusal",
				Refusal: &refusal,
			})
		}

		if len(contentItems) > 0 {
			result.Output = append(result.Output, ResponsesItem{
				ID:      generateItemID(),
				Type:    "message",
				Role:    "assistant",
				Content: &ResponsesInput{Items: contentItems},
				Status:  lo.ToPtr("completed"),
			})
		}

		// Set status based on finish reason
		if choice.FinishReason != nil {
			_, status := responsesTerminalEvent(*choice.FinishReason)
			result.Status = lo.ToPtr(status)
		}
	}

	// If no output items, create empty message
	if len(result.Output) == 0 {
		emptyText := ""
		result.Output = []ResponsesItem{
			{
				ID:   generateItemID(),
				Type: "message",
				Role: "assistant",
				Content: &ResponsesInput{
					Items: []ResponsesItem{
						{
							Type: "output_text",
							Text: &emptyText,
						},
					},
				},
				Status: lo.ToPtr("completed"),
			},
		}
	}

	return result
}

func convertUsageToResponses(usage *model.Usage) *ResponsesUsage {
	if usage == nil {
		return nil
	}

	inputTokens := usage.PromptTokens
	cachedTokens := int64(0)
	if usage.PromptTokensDetails != nil {
		cachedTokens = usage.PromptTokensDetails.CachedTokens
	}
	if usage.HasAnthropicCacheSemantic() {
		// Anthropic stored PromptTokens as non-cached; add cache read/create back so
		// OpenAI clients see the conventional total input count.
		inputTokens += usage.CacheReadInputTokens + usage.CacheCreationInputTokens
		if cachedTokens == 0 && usage.CacheReadInputTokens > 0 {
			cachedTokens = usage.CacheReadInputTokens
		}
	}

	result := &ResponsesUsage{
		InputTokens:  inputTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
	}

	if cachedTokens > 0 {
		result.InputTokenDetails.CachedTokens = cachedTokens
	}

	if usage.CompletionTokensDetails != nil {
		result.OutputTokenDetails.ReasoningTokens = usage.CompletionTokensDetails.ReasoningTokens
	}

	return result
}

func generateItemID() string {
	return fmt.Sprintf("item_%s", lo.RandomString(16, lo.AlphanumericCharset))
}
