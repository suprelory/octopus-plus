package anthropic

import (
	"encoding/json"

	"github.com/suprelory/octopus-plus/polywire/compat"
	"github.com/suprelory/octopus-plus/polywire/internal/dataurl"
	"github.com/suprelory/octopus-plus/polywire/model"
	anthropicModel "github.com/suprelory/octopus-plus/polywire/protocol/anthropic"
)

func (o *MessageOutbound) convertMessages(req *model.InternalLLMRequest) []anthropicModel.MessageParam {
	messages := make([]anthropicModel.MessageParam, 0, len(req.ConversationMessages()))
	processedIndexes := make(map[int]bool)

	for _, msg := range req.ConversationMessages() {
		if msg.Role == "system" {
			continue
		}

		converted := o.convertSingleMessage(msg, req.ConversationMessages(), processedIndexes)
		for _, convertedMsg := range converted {
			// Anthropic API 要求消息角色必须交替出现（user/assistant/user/assistant）。
			// 当 OpenAI 格式的多个连续 tool 消息被各自转换为独立的 user 消息时，
			// 会产生连续的同角色消息，需要合并以避免 "Improperly formed request" 错误。
			if n := len(messages); n > 0 && messages[n-1].Role == convertedMsg.Role {
				last := &messages[n-1]
				last.Content = anthropicModel.MessageContent{
					MultipleContent: append(contentToBlocks(last.Content), contentToBlocks(convertedMsg.Content)...),
				}
			} else {
				messages = append(messages, convertedMsg)
			}
		}
	}

	return messages
}

// contentToBlocks 将 MessageContent 统一展开为 MessageContentBlock 切片。
func contentToBlocks(c anthropicModel.MessageContent) []anthropicModel.MessageContentBlock {
	if len(c.MultipleContent) > 0 {
		// 返回副本，避免后续 append 污染原 slice
		return append([]anthropicModel.MessageContentBlock(nil), c.MultipleContent...)
	}
	if c.Content != nil && *c.Content != "" {
		return []anthropicModel.MessageContentBlock{{Type: "text", Text: c.Content}}
	}
	return nil
}

func (o *MessageOutbound) convertSingleMessage(msg model.Message, allMessages []model.Message, processedIndexes map[int]bool) []anthropicModel.MessageParam {
	switch msg.Role {
	case "tool":
		return o.convertToolMessage(msg, allMessages, processedIndexes)
	case "user":
		if msg.MessageIndex != nil && processedIndexes[*msg.MessageIndex] {
			return nil
		}
		return o.convertUserMessage(msg)
	case "assistant":
		return o.convertAssistantMessage(msg)
	default:
		return nil
	}
}

func (o *MessageOutbound) convertToolMessage(msg model.Message, allMessages []model.Message, processedIndexes map[int]bool) []anthropicModel.MessageParam {
	if msg.MessageIndex == nil {
		return []anthropicModel.MessageParam{{
			Role: "user",
			Content: anthropicModel.MessageContent{
				MultipleContent: []anthropicModel.MessageContentBlock{convertToolResultBlock(msg)},
			},
		}}
	}

	if processedIndexes[*msg.MessageIndex] {
		return nil
	}

	var toolMsgs []model.Message
	for _, m := range allMessages {
		if m.Role == "tool" && m.MessageIndex != nil && *m.MessageIndex == *msg.MessageIndex {
			toolMsgs = append(toolMsgs, m)
		}
	}

	if len(toolMsgs) == 0 {
		return nil
	}

	contentBlocks := make([]anthropicModel.MessageContentBlock, 0, len(toolMsgs))
	for _, tm := range toolMsgs {
		contentBlocks = append(contentBlocks, convertToolResultBlock(tm))
	}

	// Merge the associated user message content (if any) into the same Anthropic user message.
	// In Anthropic Messages, tool_result blocks live inside a user message's content array.
	// Our internal format represents tool results as separate "tool" role messages, but the
	// original Anthropic request may also include additional user content alongside tool_result.
	if userMsg := findUserMessageByIndex(allMessages, *msg.MessageIndex); userMsg != nil {
		userContent := o.buildMessageContent(*userMsg)
		contentBlocks = append(contentBlocks, contentToBlocks(userContent)...)
	}

	processedIndexes[*msg.MessageIndex] = true

	return []anthropicModel.MessageParam{{
		Role:    "user",
		Content: anthropicModel.MessageContent{MultipleContent: contentBlocks},
	}}
}

func findUserMessageByIndex(allMessages []model.Message, messageIndex int) *model.Message {
	for i := range allMessages {
		m := &allMessages[i]
		if m.Role == "user" && m.MessageIndex != nil && *m.MessageIndex == messageIndex {
			return m
		}
	}
	return nil
}

func convertToolResultBlock(msg model.Message) anthropicModel.MessageContentBlock {
	block := anthropicModel.MessageContentBlock{
		Type:         "tool_result",
		ToolUseID:    msg.ToolCallID,
		CacheControl: convertCacheControl(msg.CacheControl),
		IsError:      msg.ToolCallIsError,
	}

	if msg.Content.Content != nil {
		block.Content = &anthropicModel.MessageContent{
			Content: msg.Content.Content,
		}
	} else if len(msg.Content.MultipleContent) > 0 {
		blocks := make([]anthropicModel.MessageContentBlock, 0, len(msg.Content.MultipleContent))
		for _, part := range msg.Content.MultipleContent {
			if part.Type == "text" && part.Text != nil {
				blocks = append(blocks, anthropicModel.MessageContentBlock{
					Type: "text",
					Text: part.Text,
				})
			}
		}
		block.Content = &anthropicModel.MessageContent{
			MultipleContent: blocks,
		}
	}

	return block
}

func (o *MessageOutbound) convertUserMessage(msg model.Message) []anthropicModel.MessageParam {
	content := o.buildMessageContent(msg)
	return []anthropicModel.MessageParam{{Role: "user", Content: content}}
}

func (o *MessageOutbound) convertAssistantMessage(msg model.Message) []anthropicModel.MessageParam {
	if len(msg.ToolCalls) > 0 {
		return o.convertAssistantWithToolCalls(msg)
	}

	content := o.buildMessageContent(msg)
	return []anthropicModel.MessageParam{{Role: "assistant", Content: content}}
}

func (o *MessageOutbound) convertAssistantWithToolCalls(msg model.Message) []anthropicModel.MessageParam {
	if len(msg.Content.MultipleContent) > 0 {
		return []anthropicModel.MessageParam{{Role: "assistant", Content: o.convertMultiplePartContent(msg)}}
	}
	var blocks []anthropicModel.MessageContentBlock

	// Thinking + redacted_thinking blocks, emitted in their original order so Anthropic
	// multi-turn signature verification does not fail on interleaved blocks.
	blocks = append(blocks, o.emitThinkingBlocks(msg)...)

	// Add text content if present
	if msg.Content.Content != nil && *msg.Content.Content != "" {
		blocks = append(blocks, anthropicModel.MessageContentBlock{
			Type:         "text",
			Text:         msg.Content.Content,
			CacheControl: convertCacheControl(msg.CacheControl),
		})
	} else if len(msg.Content.MultipleContent) > 0 {
		for _, part := range msg.Content.MultipleContent {
			if part.Type == "text" && part.Text != nil {
				blocks = append(blocks, anthropicModel.MessageContentBlock{
					Type:         "text",
					Text:         part.Text,
					CacheControl: convertCacheControl(part.CacheControl),
				})
			}
		}
	}

	// Add tool calls
	for _, toolCall := range msg.ToolCalls {
		input := json.RawMessage("{}")
		if toolCall.Function.Arguments != "" {
			if json.Valid([]byte(toolCall.Function.Arguments)) {
				input = json.RawMessage(toolCall.Function.Arguments)
			}
		}
		blocks = append(blocks, anthropicModel.MessageContentBlock{
			Type:         "tool_use",
			ID:           toolCall.ID,
			Name:         &toolCall.Function.Name,
			Input:        input,
			CacheControl: convertCacheControl(toolCall.CacheControl),
		})
	}

	if len(blocks) == 0 {
		return nil
	}

	return []anthropicModel.MessageParam{{
		Role:    "assistant",
		Content: anthropicModel.MessageContent{MultipleContent: blocks},
	}}
}

func (o *MessageOutbound) buildMessageContent(msg model.Message) anthropicModel.MessageContent {
	if len(msg.Content.MultipleContent) > 0 {
		return o.convertMultiplePartContent(msg)
	}
	// Handle simple string content
	if msg.Content.Content != nil {
		if msg.CacheControl != nil || hasThinkingContent(msg) {
			return o.buildMultipleContentWithThinking(msg)
		}
		return anthropicModel.MessageContent{Content: msg.Content.Content}
	}

	// Handle reasoning-only messages (no text content, but has thinking/redacted thinking)
	if hasThinkingContent(msg) || len(msg.RedactedThinkingBlocks) > 0 || len(msg.ReasoningBlocks) > 0 {
		return o.buildMultipleContentWithThinking(msg)
	}

	return anthropicModel.MessageContent{}
}

func (o *MessageOutbound) buildMultipleContentWithThinking(msg model.Message) anthropicModel.MessageContent {
	blocks := o.emitThinkingBlocks(msg)

	// Only add text block if content is non-nil and non-empty
	if msg.Content.Content != nil && *msg.Content.Content != "" {
		blocks = append(blocks, anthropicModel.MessageContentBlock{
			Type:         "text",
			Text:         msg.Content.Content,
			CacheControl: convertCacheControl(msg.CacheControl),
		})
	}

	return anthropicModel.MessageContent{MultipleContent: blocks}
}

func (o *MessageOutbound) convertMultiplePartContent(msg model.Message) anthropicModel.MessageContent {
	blocks := make([]anthropicModel.MessageContentBlock, 0, len(msg.Content.MultipleContent)+2)

	// Only emit thinking blocks when they carry a signature; without one, Anthropic rejects the
	// turn in subsequent extended-thinking rounds. emitThinkingBlocks already preserves order.
	for _, b := range o.emitThinkingBlocks(msg) {
		if b.Type == "thinking" && (b.Signature == nil || *b.Signature == "") {
			continue
		}
		blocks = append(blocks, b)
	}

	for _, part := range msg.Content.MultipleContent {
		switch part.Type {
		case "text":
			if part.Text != nil {
				blocks = append(blocks, anthropicModel.MessageContentBlock{
					Type:         "text",
					Text:         part.Text,
					Citations:    compat.AnthropicCitationsFromModel(part.Citations),
					CacheControl: convertCacheControl(part.CacheControl),
				})
			}
		case "image_url":
			if part.ImageURL != nil && part.ImageURL.URL != "" {
				block := convertImageURLToBlock(part)
				if block != nil {
					blocks = append(blocks, *block)
				}
			}
		case "document":
			if block := convertDocumentPartToBlock(part); block != nil {
				blocks = append(blocks, *block)
			}
		case "server_tool_use":
			if part.ServerToolUse == nil {
				continue
			}
			block := compat.AnthropicServerToolUseFromModel(part.ServerToolUse)
			block.CacheControl = convertCacheControl(part.CacheControl)
			blocks = append(blocks, block)
		case "server_tool_result":
			if part.ServerToolResult == nil {
				continue
			}
			block := compat.AnthropicServerToolResultFromModel(part.ServerToolResult)
			block.CacheControl = convertCacheControl(part.CacheControl)
			blocks = append(blocks, block)
		}
	}

	// Add tool calls if present
	for _, toolCall := range msg.ToolCalls {
		input := json.RawMessage("{}")
		if toolCall.Function.Arguments != "" {
			if json.Valid([]byte(toolCall.Function.Arguments)) {
				input = json.RawMessage(toolCall.Function.Arguments)
			}
		}
		blocks = append(blocks, anthropicModel.MessageContentBlock{
			Type:         "tool_use",
			ID:           toolCall.ID,
			Name:         &toolCall.Function.Name,
			Input:        input,
			CacheControl: convertCacheControl(toolCall.CacheControl),
		})
	}

	if len(blocks) == 0 {
		return anthropicModel.MessageContent{}
	}

	return anthropicModel.MessageContent{MultipleContent: blocks}
}

func convertImageURLToBlock(part model.MessageContentPart) *anthropicModel.MessageContentBlock {
	if part.ImageURL == nil || part.ImageURL.URL == "" {
		return nil
	}

	url := part.ImageURL.URL
	if parsed := dataurl.ParseDataURL(url); parsed != nil {
		return &anthropicModel.MessageContentBlock{
			Type: "image",
			Source: &anthropicModel.ImageSource{
				Type:      "base64",
				MediaType: parsed.MediaType,
				Data:      parsed.Data,
			},
			CacheControl: convertCacheControl(part.CacheControl),
		}
	}

	return &anthropicModel.MessageContentBlock{
		Type: "image",
		Source: &anthropicModel.ImageSource{
			Type: "url",
			URL:  part.ImageURL.URL,
		},
		CacheControl: convertCacheControl(part.CacheControl),
	}
}

// convertDocumentPartToBlock maps an internal MessageContentPart of type
// "document" into an Anthropic document content block. Anthropic accepts
// four source envelopes (base64 / url / text / content); we honour whatever
// the internal payload carries. Title / Context / Citations metadata is
// preserved, so citation-aware downstream callers keep working.
func convertDocumentPartToBlock(part model.MessageContentPart) *anthropicModel.MessageContentBlock {
	doc := part.Document
	if doc == nil {
		return nil
	}
	source := &anthropicModel.ImageSource{
		Type:      doc.Type,
		MediaType: doc.MediaType,
		Data:      doc.Data,
		URL:       doc.URL,
		Content:   doc.Content,
	}
	if doc.Type == "text" && doc.Data == "" && doc.Text != "" {
		source.Data = doc.Text
	}
	block := &anthropicModel.MessageContentBlock{
		Type:         "document",
		Source:       source,
		Title:        doc.Title,
		Context:      doc.Context,
		CacheControl: convertCacheControl(part.CacheControl),
	}
	if doc.Citations != nil {
		block.CitationConfig = &anthropicModel.DocumentCitationsControl{Enabled: doc.Citations.Enabled}
	}
	return block
}
