package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/samber/lo"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func (i *ResponseInbound) TransformRequest(ctx context.Context, body []byte) (*model.InternalLLMRequest, error) {
	var req ResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to decode responses api request: %w", err)
	}
	var rawFields struct {
		Input json.RawMessage `json:"input"`
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &rawFields); err != nil {
		return nil, fmt.Errorf("failed to preserve responses api native fields: %w", err)
	}
	if strings.HasPrefix(strings.TrimSpace(string(rawFields.Input)), "[") {
		req.RawInputItems = append(json.RawMessage(nil), rawFields.Input...)
	}
	if strings.HasPrefix(strings.TrimSpace(string(rawFields.Tools)), "[") {
		req.RawTools = append(json.RawMessage(nil), rawFields.Tools...)
	}

	if req.Model == "" {
		return nil, fmt.Errorf("model is required")
	}

	i.truncation = req.Truncation

	internalRequest, err := convertToInternalRequest(&req)
	if err != nil {
		return nil, err
	}
	internalRequest.RequestType = model.RequestTypeResponses
	if err := internalRequest.CaptureFieldPresence(body); err != nil {
		return nil, err
	}
	if err := internalRequest.NormalizeOperation(); err != nil {
		return nil, err
	}
	if err := internalRequest.CaptureRequestRecovery(body, req); err != nil {
		return nil, err
	}
	return internalRequest, nil
}

// Conversion functions

func convertToInternalRequest(req *ResponsesRequest) (*model.InternalLLMRequest, error) {
	chatReq := &model.InternalLLMRequest{
		Model:               req.Model,
		Temperature:         req.Temperature,
		TopP:                req.TopP,
		Stream:              req.Stream,
		Store:               req.Store,
		ServiceTier:         req.ServiceTier,
		Truncation:          req.Truncation,
		User:                req.User,
		Metadata:            req.Metadata,
		MaxCompletionTokens: req.MaxOutputTokens,
		TopLogprobs:         req.TopLogprobs,
		ParallelToolCalls:   req.ParallelToolCalls,
		RawAPIFormat:        model.APIFormatOpenAIResponse,
		TransformerMetadata: map[string]string{},
		Include:             append([]string(nil), req.Include...),
	}

	if req.Input.Text == nil && len(req.Input.Items) > 0 {
		chatReq.TransformOptions.ArrayInputs = lo.ToPtr(true)
		rawItems := append(json.RawMessage(nil), req.RawInputItems...)
		if len(rawItems) == 0 {
			rawItems, _ = json.Marshal(req.Input.Items)
		}
		if len(rawItems) > 0 {
			chatReq.SetOpenAIRawInputItems(rawItems)
		}
	}
	markOpenAIResponsesPassthroughIfNeeded(req, chatReq)

	var reasoningSummary *string
	var reasoningGenerateSummary *string

	// Convert reasoning
	if req.Reasoning != nil {
		if effort := validateReasoningEffort(req.Reasoning.Effort); effort != "" {
			chatReq.ReasoningEffort = effort
		}
		if req.Reasoning.MaxTokens != nil {
			chatReq.ReasoningBudget = req.Reasoning.MaxTokens
		}
		if req.Reasoning.Summary != nil {
			if summary := validateReasoningSummary(*req.Reasoning.Summary); summary != "" {
				reasoningSummary = &summary
			}
		}
		reasoningGenerateSummary = req.Reasoning.GenerateSummary
	}

	chatReq.SetOpenAIResponsesOptions(model.OpenAIResponsesOptions{
		PreviousResponseID:       req.PreviousResponseID,
		Background:               req.Background,
		Prompt:                   req.Prompt,
		PromptCacheKey:           req.PromptCacheKey,
		PromptCacheRetention:     req.PromptCacheRetention,
		SafetyIdentifier:         req.SafetyIdentifier,
		MaxToolCalls:             req.MaxToolCalls,
		Conversation:             req.Conversation,
		ContextManagement:        req.ContextManagement,
		StreamOptions:            req.StreamOptions,
		ReasoningSummary:         reasoningSummary,
		ReasoningGenerateSummary: reasoningGenerateSummary,
		RawInputItems:            chatReq.OpenAIRawInputItems(),
		RawTools:                 req.RawTools,
	})

	// Convert tool choice
	if req.ToolChoice != nil {
		chatReq.ToolChoice = convertToolChoiceToInternal(req.ToolChoice)
	}

	// Convert instructions to system message
	messages := make([]model.Message, 0)
	if req.Instructions != "" {
		messages = append(messages, model.Message{
			Role: "system",
			Content: model.MessageContent{
				Content: lo.ToPtr(req.Instructions),
			},
		})
	}

	// Convert input to messages
	inputMessages, err := convertInputToMessages(&req.Input)
	if err != nil {
		return nil, err
	}
	messages = append(messages, inputMessages...)
	chatReq.Messages = messages

	// Convert tools
	if len(req.Tools) > 0 {
		tools, err := convertToolsToInternal(req.Tools)
		if err != nil {
			return nil, err
		}
		chatReq.Tools = tools
	}

	// Convert text format
	if req.Text != nil && req.Text.Format != nil && req.Text.Format.Type != "" {
		rf := &model.ResponseFormat{
			Strict:      req.Text.Format.Strict,
			Description: req.Text.Format.Description,
			Type:        req.Text.Format.Type,
			Name:        req.Text.Format.Name,
		}
		if len(req.Text.Format.Schema) > 0 {
			rf.RawSchema = req.Text.Format.Schema
			rf.JSONSchema = req.Text.Format.Schema
			if parsed, err := model.ParseSchema(req.Text.Format.Schema); err == nil {
				rf.Schema = parsed
			}
		}
		chatReq.ResponseFormat = rf
	}

	return chatReq, nil
}

func markOpenAIResponsesPassthroughIfNeeded(req *ResponsesRequest, chatReq *model.InternalLLMRequest) {
	if req == nil || chatReq == nil {
		return
	}
	seen := make(map[string]bool)
	mark := func(reason string) {
		if !seen[reason] {
			chatReq.MarkOpenAIResponsesPassthroughRequired(reason)
			seen[reason] = true
		}
	}
	for _, tool := range req.Tools {
		if kind := firstUnsupportedResponsesToolType([]ResponsesTool{tool}); kind != "" {
			mark("tool:" + kind)
		}
	}
	var visit func(*ResponsesInput, bool)
	visit = func(input *ResponsesInput, nested bool) {
		if input == nil {
			return
		}
		for _, item := range input.Items {
			kind := firstUnsupportedResponsesTopLevelItemType(&item)
			if nested {
				kind = firstUnsupportedResponsesContentItemType(&ResponsesInput{Items: []ResponsesItem{item}})
			}
			if kind != "" {
				mark("input:" + kind)
			}
			visit(item.Content, true)
			visit(item.Output, true)
		}
	}
	visit(&req.Input, false)
	var markExtensions func(json.RawMessage, bool)
	markExtensions = func(raw json.RawMessage, tools bool) {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return
		}
		for _, item := range items {
			var schema any = ResponsesItem{}
			prefix := "input_field:"
			if tools {
				schema, prefix = ResponsesTool{}, "tool_field:"
			}
			unknown, err := model.UnknownWireFields(item, schema)
			if err != nil {
				continue
			}
			fields := make([]string, 0, len(unknown))
			for field := range unknown {
				fields = append(fields, field)
			}
			sort.Strings(fields)
			for _, field := range fields {
				mark(prefix + field)
			}
			if !tools {
				var children map[string]json.RawMessage
				if json.Unmarshal(item, &children) == nil {
					markExtensions(children["content"], false)
					markExtensions(children["output"], false)
				}
			}
		}
	}
	markExtensions(req.RawInputItems, false)
	markExtensions(req.RawTools, true)
}

func firstUnsupportedResponsesToolType(tools []ResponsesTool) string {
	for _, tool := range tools {
		switch tool.Type {
		case "function", "image_generation":
			continue
		case "":
			return "<empty>"
		default:
			return tool.Type
		}
	}
	return ""
}

func firstUnsupportedResponsesTopLevelItemType(item *ResponsesItem) string {
	if item == nil {
		return ""
	}
	switch item.Type {
	case "", "message", "input_text", "input_image", "input_file", "input_audio", "function_call", "function_call_output", "reasoning":
	default:
		return item.Type
	}
	if unsupported := firstUnsupportedResponsesContentItemType(item.Content); unsupported != "" {
		return unsupported
	}
	if unsupported := firstUnsupportedResponsesContentItemType(item.Output); unsupported != "" {
		return unsupported
	}
	return ""
}

func firstUnsupportedResponsesContentItemType(input *ResponsesInput) string {
	if input == nil || input.Text != nil || len(input.Items) == 0 {
		return ""
	}
	for _, item := range input.Items {
		switch item.Type {
		case "input_text", "text", "output_text", "input_image", "input_file", "input_audio":
			continue
		case "":
			return "<empty>"
		default:
			return item.Type
		}
	}
	return ""
}

func convertToolChoiceToInternal(src *ResponsesToolChoice) *model.ToolChoice {
	if src == nil {
		return nil
	}

	result := &model.ToolChoice{}
	if src.Mode != nil {
		result.ToolChoice = src.Mode
	} else if src.Type != nil {
		result.NamedToolChoice = &model.NamedToolChoice{
			Type: *src.Type,
		}
		if src.Name != nil {
			name := *src.Name
			result.NamedToolChoice.Function = &model.ToolFunction{Name: name}
			result.NamedToolChoice.Name = &name
		}
	}
	return result
}

func convertInputToMessages(input *ResponsesInput) ([]model.Message, error) {
	if input == nil {
		return nil, nil
	}

	// Simple text input
	if input.Text != nil {
		return []model.Message{
			{
				Role: "user",
				Content: model.MessageContent{
					Content: input.Text,
				},
			},
		}, nil
	}

	// Array of items. Consecutive function calls belong to one assistant
	// turn; emitting one assistant message per item creates an invalid Chat
	// sequence once the corresponding tool results follow.
	messages := make([]model.Message, 0, len(input.Items))
	for idx := 0; idx < len(input.Items); {
		item := &input.Items[idx]
		if item.Type == "function_call" {
			msg := model.Message{Role: "assistant"}
			for idx < len(input.Items) && input.Items[idx].Type == "function_call" {
				callMsg, err := convertItemToMessage(&input.Items[idx])
				if err != nil {
					return nil, err
				}
				if callMsg != nil {
					msg.ToolCalls = append(msg.ToolCalls, callMsg.ToolCalls...)
				}
				idx++
			}
			messages = append(messages, msg)
			continue
		}

		msg, err := convertItemToMessage(item)
		if err != nil {
			return nil, err
		}
		if msg != nil {
			messages = append(messages, *msg)
		}
		idx++
	}

	return messages, nil
}

func convertItemToMessage(item *ResponsesItem) (*model.Message, error) {
	if item == nil {
		return nil, nil
	}

	switch item.Type {
	case "message", "input_text", "":
		msg := &model.Message{
			Role: item.Role,
		}

		if item.Content != nil && len(item.Content.Items) > 0 && item.isOutputMessageContent() {
			msg.Content = convertContentItemsToMessageContent(item.GetContentItems())
		} else if item.Content != nil {
			msg.Content = convertInputToMessageContent(*item.Content)
		} else if item.Text != nil {
			msg.Content = model.MessageContent{Content: item.Text}
		}

		return msg, nil

	case "input_image", "input_file", "input_audio":
		role := item.Role
		if role == "" {
			role = "user"
		}
		content := convertInputToMessageContent(ResponsesInput{
			Items: []ResponsesItem{*item},
		})
		if content.Content == nil && len(content.MultipleContent) == 0 {
			return nil, nil
		}
		return &model.Message{
			Role:    role,
			Content: content,
		}, nil

	case "function_call":
		return &model.Message{
			Role: "assistant",
			ToolCalls: []model.ToolCall{
				{
					ID:   item.CallID,
					Type: "function",
					Function: model.FunctionCall{
						Name:      item.Name,
						Namespace: item.Namespace,
						Arguments: item.Arguments,
					},
				},
			},
		}, nil

	case "function_call_output":
		return &model.Message{
			Role:       "tool",
			ToolCallID: lo.ToPtr(item.CallID),
			Content:    convertInputToMessageContent(*item.Output),
		}, nil

	case "reasoning":
		msg := &model.Message{
			Role: "assistant",
		}

		reasoningText := reasoningTextFromInputItem(item)
		if reasoningText != "" {
			msg.ReasoningContent = lo.ToPtr(reasoningText)
		}

		blockKind := model.ReasoningBlockKindSignature
		if reasoningText != "" {
			blockKind = model.ReasoningBlockKindThinking
		}
		block := model.ReasoningBlock{
			Kind:  blockKind,
			Index: -1,
			Text:  reasoningText,
		}
		if item.EncryptedContent != nil && *item.EncryptedContent != "" {
			signature := model.OpaqueSignature{
				Provider: model.SignatureProviderOpenAI,
				Kind:     model.OpaqueSignatureKindOpenAIReasoning,
				Value:    *item.EncryptedContent,
			}
			msg.SetOpaqueReasoningSignature(signature)
			block.SetOpaqueSignature(signature)
		}
		if reasoningText != "" || block.SignatureSource != nil {
			msg.AppendReasoningBlock(block)
		}

		return msg, nil

	default:
		return nil, nil
	}
}

func reasoningTextFromInputItem(item *ResponsesItem) string {
	if item == nil {
		return ""
	}
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

func convertInputToMessageContent(input ResponsesInput) model.MessageContent {
	if input.Text != nil {
		return model.MessageContent{Content: input.Text}
	}

	parts := make([]model.MessageContentPart, 0, len(input.Items))
	for _, item := range input.Items {
		switch item.Type {
		case "input_text", "text", "output_text":
			if item.Text != nil {
				parts = append(parts, model.MessageContentPart{
					Type: "text",
					Text: item.Text,
				})
			}
		case "input_image":
			if item.ImageURL != nil {
				parts = append(parts, model.MessageContentPart{
					Type: "image_url",
					ImageURL: &model.ImageURL{
						URL:    *item.ImageURL,
						Detail: item.Detail,
					},
				})
			}
		case "input_file":
			// O-H6: OpenAI Responses accepts three shapes for input_file —
			// keep whichever representation the caller provided so
			// downstream transformers can route the reference verbatim.
			file := &model.File{}
			if item.FileID != nil {
				file.FileID = *item.FileID
			}
			if item.FileURL != nil {
				file.FileURL = *item.FileURL
			}
			if item.Filename != nil {
				file.Filename = *item.Filename
			}
			if item.FileData != nil {
				file.FileData = *item.FileData
			}
			if file.FileID == "" && file.FileURL == "" && file.FileData == "" {
				continue
			}
			parts = append(parts, model.MessageContentPart{
				Type: "file",
				File: file,
			})
		case "input_audio":
			// O-H6: `input_audio` rides in a nested object per the
			// Responses schema ({ data, format }).
			if item.InputAudio == nil {
				continue
			}
			parts = append(parts, model.MessageContentPart{
				Type: "input_audio",
				Audio: &model.Audio{
					Format: item.InputAudio.Format,
					Data:   item.InputAudio.Data,
				},
			})
		}
	}

	if len(parts) == 1 && parts[0].Type == "text" && parts[0].Text != nil {
		return model.MessageContent{Content: parts[0].Text}
	}

	return model.MessageContent{MultipleContent: parts}
}

func convertContentItemsToMessageContent(items []ResponsesContentItem) model.MessageContent {
	if len(items) == 1 && (items[0].Type == "output_text" || items[0].Type == "input_text" || items[0].Type == "text") {
		return model.MessageContent{Content: lo.ToPtr(items[0].Text)}
	}

	parts := make([]model.MessageContentPart, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case "output_text", "input_text", "text":
			parts = append(parts, model.MessageContentPart{
				Type: "text",
				Text: lo.ToPtr(item.Text),
			})
		}
	}

	return model.MessageContent{MultipleContent: parts}
}

func convertToolsToInternal(tools []ResponsesTool) ([]model.Tool, error) {
	result := make([]model.Tool, 0, len(tools))

	for _, tool := range tools {
		switch tool.Type {
		case "function":
			params, err := json.Marshal(tool.Parameters)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal function parameters: %w", err)
			}

			result = append(result, model.Tool{
				Type: "function",
				Function: model.Function{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  params,
					Strict:      tool.Strict,
				},
			})

		case "image_generation":
			result = append(result, model.Tool{
				Type: "image_generation",
				ImageGeneration: &model.ImageGeneration{
					Background:        tool.Background,
					OutputFormat:      tool.OutputFormat,
					Quality:           tool.Quality,
					Size:              tool.Size,
					OutputCompression: tool.OutputCompression,
				},
			})
		}
	}

	return result, nil
}

// validateReasoningEffort whitelists the values OpenAI's Responses API
// accepts for `reasoning.effort`. Unknown inputs are dropped (empty
// return) so the upstream schema validator never sees garbage; callers
// fall back to the provider default.
func validateReasoningEffort(effort string) string {
	switch effort {
	case "minimal", "low", "medium", "high":
		return effort
	case "":
		return ""
	default:
		return ""
	}
}

// validateReasoningSummary whitelists the values OpenAI's Responses API
// accepts for `reasoning.summary`. Unknown inputs are dropped.
func validateReasoningSummary(summary string) string {
	switch summary {
	case "auto", "concise", "detailed":
		return summary
	case "":
		return ""
	default:
		return ""
	}
}
