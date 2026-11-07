package openai

import (
	"encoding/json"
	"strings"

	"github.com/samber/lo"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func ConvertToResponsesRequest(req *model.InternalLLMRequest) *ResponsesRequest {
	// `user` is deprecated on OpenAI text APIs and is rejected by some
	// OpenAI-compatible upstreams, so keep the modern identifiers
	// (`prompt_cache_key` / `safety_identifier`) and omit the legacy field.
	promptCacheKey, promptCacheRetention := anthropicCacheMetadataForResponses(req)
	responsesOptions := req.GetOpenAIResponsesOptions()
	result := &ResponsesRequest{
		Model:                req.Model,
		Temperature:          req.Temperature,
		TopP:                 req.TopP,
		Stream:               req.Stream,
		Store:                req.Store,
		ServiceTier:          req.ServiceTier,
		Truncation:           req.Truncation,
		Metadata:             req.Metadata,
		MaxOutputTokens:      req.MaxCompletionTokens,
		ParallelToolCalls:    req.ParallelToolCalls,
		PromptCacheKey:       promptCacheKey,
		PromptCacheRetention: promptCacheRetention,
	}
	if result.MaxOutputTokens == nil {
		result.MaxOutputTokens = req.MaxTokens
	}

	// Convert instructions from system messages
	result.Instructions = convertInstructionsFromMessages(req.ConversationMessages())

	// Convert input from messages or preserve original array items when available.
	result.Input = buildResponsesInput(req)

	// Preserve native Responses tools during canonical replay/continuation.
	if rawTools, ok := responsesToolsFromRaw(responsesOptions.RawTools); ok {
		result.Tools = rawTools
	} else if len(req.Tools) > 0 {
		result.Tools = convertToolsToResponses(req.Tools)
	}

	// Convert tool choice
	if choice := req.ToolChoiceForTarget(model.APIFormatOpenAIResponse); choice != nil {
		result.ToolChoice = convertToolChoiceToResponses(choice)
	}

	// Convert text options
	if req.ResponseFormat != nil {
		format := &ResponsesTextFormat{
			Strict:      req.ResponseFormat.Strict,
			Description: req.ResponseFormat.Description,
			Type:        req.ResponseFormat.Type,
			Name:        req.ResponseFormat.Name,
		}
		// Prefer the parsed Schema so nested fields survive round-trips;
		// fall back to RawSchema (passthrough) then the legacy JSONSchema
		// blob for callers that never populated the new fields.
		if req.ResponseFormat.Schema != nil {
			if b, err := req.ResponseFormat.Schema.ToOpenAIResponseFormat(); err == nil {
				format.Schema = b
			}
		}
		if len(format.Schema) == 0 && len(req.ResponseFormat.RawSchema) > 0 {
			format.Schema = req.ResponseFormat.RawSchema
		}
		if len(format.Schema) == 0 && len(req.ResponseFormat.JSONSchema) > 0 {
			format.Schema = req.ResponseFormat.JSONSchema
		}
		result.Text = &ResponsesTextOptions{Format: format}
	}

	// Verbosity (O-M8) is a sibling of format on Responses text. Attach it
	// even when ResponseFormat is nil — the gpt-5 verbosity knob can be
	// used with plain-text output too.
	if req.Verbosity != nil && strings.TrimSpace(*req.Verbosity) != "" {
		if result.Text == nil {
			result.Text = &ResponsesTextOptions{}
		}
		result.Text.Verbosity = req.Verbosity
	}

	// Convert reasoning
	if req.ReasoningEffort != "" || req.ReasoningBudget != nil || responsesOptions.ReasoningSummary != nil || responsesOptions.ReasoningGenerateSummary != nil {
		result.Reasoning = &ResponsesReasoning{
			Effort:          req.ReasoningEffort,
			MaxTokens:       req.ReasoningBudget,
			Summary:         responsesOptions.ReasoningSummary,
			GenerateSummary: responsesOptions.ReasoningGenerateSummary,
		}
	}

	// Pass-through fields
	result.PreviousResponseID = responsesOptions.PreviousResponseID
	result.Background = responsesOptions.Background
	result.Prompt = responsesOptions.Prompt
	if result.PromptCacheKey == nil {
		result.PromptCacheKey = responsesOptions.PromptCacheKey
	}
	if result.PromptCacheRetention == nil {
		result.PromptCacheRetention = responsesOptions.PromptCacheRetention
	}
	result.SafetyIdentifier = responsesOptions.SafetyIdentifier
	result.MaxToolCalls = responsesOptions.MaxToolCalls
	result.Conversation = responsesOptions.Conversation
	result.ContextManagement = responsesOptions.ContextManagement
	result.StreamOptions = responsesOptions.StreamOptions
	result.Include = req.Include
	result.TopLogprobs = req.TopLogprobs

	return result
}

func buildResponsesInput(req *model.InternalLLMRequest) ResponsesInput {
	if req == nil {
		return ResponsesInput{}
	}
	// RawInputItems is the authoritative runtime source for Responses requests,
	// especially after websocket replay mutates it in-place for exact replay.
	if rawInputItems := req.OpenAIRawInputItems(); len(rawInputItems) > 0 {
		return ResponsesInput{Raw: sanitizeResponsesRawItems(rawInputItems)}
	}
	openaiExt := req.GetOpenAIExtensions()
	if len(openaiExt.RawResponseItems) > 0 {
		return ResponsesInput{Raw: sanitizeResponsesRawItems(append(json.RawMessage(nil), openaiExt.RawResponseItems...))}
	}
	return sanitizeResponsesInput(convertInputFromMessages(req.ConversationMessages(), req.TransformOptions))
}

func MarshalResponsesInputItems(msgs []model.Message) (json.RawMessage, error) {
	forceArray := true
	input := convertInputFromMessages(msgs, model.TransformOptions{ArrayInputs: &forceArray})
	if len(input.Items) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(input.Items)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func convertInstructionsFromMessages(msgs []model.Message) string {
	var instructions []string
	for _, msg := range msgs {
		if msg.Role != "system" && msg.Role != "developer" {
			continue
		}
		if msg.Content.Content != nil {
			instructions = append(instructions, *msg.Content.Content)
		}
		if len(msg.Content.MultipleContent) > 0 {
			var sb strings.Builder
			for _, p := range msg.Content.MultipleContent {
				if p.Type == "text" && p.Text != nil {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(*p.Text)
				}
			}
			if sb.Len() > 0 {
				instructions = append(instructions, sb.String())
			}
		}
	}
	return strings.Join(instructions, "\n")
}

func convertInputFromMessages(msgs []model.Message, transformOptions model.TransformOptions) ResponsesInput {
	if len(msgs) == 0 {
		return ResponsesInput{}
	}

	wasArrayFormat := transformOptions.ArrayInputs != nil && *transformOptions.ArrayInputs

	// Check for simple single user message
	nonSystemMsgs := make([]model.Message, 0)
	for _, msg := range msgs {
		if msg.Role != "system" && msg.Role != "developer" {
			nonSystemMsgs = append(nonSystemMsgs, msg)
		}
	}

	if !wasArrayFormat && len(nonSystemMsgs) == 1 && nonSystemMsgs[0].Content.Content != nil && nonSystemMsgs[0].Role == "user" {
		return ResponsesInput{Text: nonSystemMsgs[0].Content.Content}
	}

	// Build call_id -> item_id mapping for function_call_output reference
	callIDToItemID := make(map[string]string)
	var items []ResponsesItem
	for _, msg := range msgs {
		switch msg.Role {
		case "system", "developer":
			continue
		case "user":
			items = append(items, convertUserMessageToResponses(msg))
		case "assistant":
			assistantItems := convertAssistantMessageToResponses(msg)
			for _, item := range assistantItems {
				if item.Type == "function_call" && item.ID != "" && item.CallID != "" {
					callIDToItemID[item.CallID] = item.ID
				}
			}
			items = append(items, assistantItems...)
		case "tool":
			items = append(items, convertToolMessageToResponses(msg, callIDToItemID))
		}
	}

	return ResponsesInput{Items: sanitizeResponsesItems(items)}
}

func convertUserMessageToResponses(msg model.Message) ResponsesItem {
	var contentItems []ResponsesItem

	if msg.Content.Content != nil {
		contentItems = append(contentItems, ResponsesItem{
			Type: "input_text",
			Text: msg.Content.Content,
		})
	} else {
		for _, p := range msg.Content.MultipleContent {
			switch p.Type {
			case "text":
				if p.Text != nil {
					contentItems = append(contentItems, ResponsesItem{
						Type: "input_text",
						Text: p.Text,
					})
				}
			case "image_url":
				if p.ImageURL != nil {
					contentItems = append(contentItems, ResponsesItem{
						Type:     "input_image",
						ImageURL: &p.ImageURL.URL,
						Detail:   p.ImageURL.Detail,
					})
				}
			case "file":
				// O-H6: reproduce whichever file representation the
				// caller used originally.
				if p.File == nil {
					continue
				}
				item := ResponsesItem{Type: "input_file"}
				if p.File.FileID != "" {
					item.FileID = lo.ToPtr(p.File.FileID)
				}
				if p.File.FileURL != "" {
					item.FileURL = lo.ToPtr(p.File.FileURL)
				}
				if p.File.Filename != "" {
					item.Filename = lo.ToPtr(p.File.Filename)
				}
				if p.File.FileData != "" {
					item.FileData = lo.ToPtr(p.File.FileData)
				}
				if item.FileID == nil && item.FileURL == nil && item.FileData == nil {
					continue
				}
				contentItems = append(contentItems, item)
			case "input_audio":
				// O-H6: audio on Responses rides in a nested object
				// rather than a flat field.
				if p.Audio == nil {
					continue
				}
				contentItems = append(contentItems, ResponsesItem{
					Type: "input_audio",
					InputAudio: &ResponsesInputAudio{
						Data:   p.Audio.Data,
						Format: p.Audio.Format,
					},
				})
			}
		}
	}

	return ResponsesItem{
		Role:    msg.Role,
		Content: &ResponsesInput{Items: contentItems},
	}
}

func convertAssistantMessageToResponses(msg model.Message) []ResponsesItem {
	var items []ResponsesItem

	items = append(items, openAIReasoningItems(msg)...)

	// Handle tool calls
	for _, tc := range msg.ToolCalls {
		items = append(items, ResponsesItem{
			ID:        generateResponsesItemID(),
			Type:      "function_call",
			CallID:    tc.ID,
			Name:      tc.Function.Name,
			Namespace: tc.Function.Namespace,
			Arguments: tc.Function.Arguments,
		})
	}

	// Handle content
	var contentItems []ResponsesItem
	if msg.Content.Content != nil {
		contentItems = append(contentItems, ResponsesItem{
			Type: "output_text",
			Text: msg.Content.Content,
		})
	} else {
		for _, p := range msg.Content.MultipleContent {
			if p.Type == "text" && p.Text != nil {
				contentItems = append(contentItems, ResponsesItem{
					Type: "output_text",
					Text: p.Text,
				})
			}
		}
	}

	if len(contentItems) > 0 {
		items = append(items, ResponsesItem{
			Type:    "message",
			Role:    msg.Role,
			Status:  lo.ToPtr("completed"),
			Content: &ResponsesInput{Items: contentItems},
		})
	}

	return sanitizeResponsesItems(items)
}

func convertToolMessageToResponses(msg model.Message, callIDToItemID map[string]string) ResponsesItem {
	var output ResponsesInput

	if msg.Content.Content != nil {
		output.Text = msg.Content.Content
	} else if len(msg.Content.MultipleContent) > 0 {
		for _, p := range msg.Content.MultipleContent {
			switch p.Type {
			case "text":
				if p.Text == nil {
					continue
				}
				output.Items = append(output.Items, ResponsesItem{
					Type: "input_text",
					Text: p.Text,
				})
			case "image_url":
				if p.ImageURL == nil {
					continue
				}
				detail := "auto"
				if p.ImageURL.Detail != nil {
					detail = *p.ImageURL.Detail
				}
				output.Items = append(output.Items, ResponsesItem{
					Type:     "input_image",
					ImageURL: &p.ImageURL.URL,
					Detail:   &detail,
				})
			}
		}
	}

	if output.Text == nil && len(output.Items) == 0 {
		output.Text = lo.ToPtr("")
	}

	item := ResponsesItem{
		Type:   "function_call_output",
		CallID: lo.FromPtr(msg.ToolCallID),
		Output: &output,
	}

	// Set item_reference to the corresponding function_call's ID
	if msg.ToolCallID != nil {
		if itemID, ok := callIDToItemID[*msg.ToolCallID]; ok {
			item.ItemReference = lo.ToPtr(itemID)
		}
	}

	return item
}

func convertToolsToResponses(tools []model.Tool) []ResponsesTool {
	result := make([]ResponsesTool, 0, len(tools))
	for _, tool := range tools {
		switch tool.Type {
		case "function":
			rt := ResponsesTool{
				Type:        "function",
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Strict:      tool.Function.Strict,
			}
			if len(tool.Function.Parameters) > 0 {
				var params map[string]any
				if err := json.Unmarshal(tool.Function.Parameters, &params); err == nil {
					rt.Parameters = params
				}
			}
			result = append(result, rt)
		case "image_generation":
			rt := ResponsesTool{
				Type: "image_generation",
			}
			if tool.ImageGeneration != nil {
				rt.Background = tool.ImageGeneration.Background
				rt.OutputFormat = tool.ImageGeneration.OutputFormat
				rt.Quality = tool.ImageGeneration.Quality
				rt.Size = tool.ImageGeneration.Size
				rt.OutputCompression = tool.ImageGeneration.OutputCompression
			}
			result = append(result, rt)
		}
	}
	return result
}

func convertToolChoiceToResponses(tc *model.ToolChoice) *ResponsesToolChoice {
	if tc == nil {
		return nil
	}

	result := &ResponsesToolChoice{}
	if tc.ToolChoice != nil {
		result.Mode = tc.ToolChoice
	} else if tc.NamedToolChoice != nil {
		result.Type = &tc.NamedToolChoice.Type
		if name := tc.NamedToolChoice.ResolvedFunctionName(); name != "" {
			n := name
			result.Name = &n
		}
	}
	return result
}
