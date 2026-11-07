package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

const anthropicPromptCacheRetention24h = "24h"

func anthropicCacheMetadataForResponses(req *model.InternalLLMRequest) (*string, *string) {
	if req == nil {
		return nil, nil
	}
	if req.ResponsesPromptCacheKey != nil || req.PromptCacheRetention != nil {
		return req.ResponsesPromptCacheKey, req.PromptCacheRetention
	}

	return derivedAnthropicCacheMetadata(req)
}

func derivedAnthropicCacheMetadata(req *model.InternalLLMRequest) (*string, *string) {
	projection := deriveAnthropicCacheProjection(req)
	if !projection.ok {
		return nil, nil
	}

	sum := sha256.Sum256(projection.payload)
	key := "anthropic-cache-" + hex.EncodeToString(sum[:])
	return &key, projection.retention
}

type anthropicCacheProjection struct {
	payload     []byte
	retention   *string
	anchor      string
	ok          bool
	cacheSignal bool
}

type anthropicCacheTool struct {
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
	TTL         string          `json:"ttl,omitempty"`
}

type anthropicCachePart struct {
	Type    string          `json:"type,omitempty"`
	Text    string          `json:"text,omitempty"`
	Name    string          `json:"name,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`
	TTL     string          `json:"ttl,omitempty"`
}

type anthropicCacheMessage struct {
	Role  string               `json:"role,omitempty"`
	Parts []anthropicCachePart `json:"parts,omitempty"`
}

type anthropicCachePayload struct {
	Instructions string                  `json:"instructions,omitempty"`
	Tools        []anthropicCacheTool    `json:"tools,omitempty"`
	Messages     []anthropicCacheMessage `json:"messages,omitempty"`
}

func deriveAnthropicCacheProjection(req *model.InternalLLMRequest) anthropicCacheProjection {
	return buildAnthropicCacheProjection(req)
}

func buildAnthropicCacheProjection(req *model.InternalLLMRequest) anthropicCacheProjection {
	if req == nil {
		return anthropicCacheProjection{}
	}

	projection := anthropicCacheProjection{cacheSignal: requestHasCacheControl(req)}
	if !projection.cacheSignal && req.RawAPIFormat != model.APIFormatAnthropicMessage {
		return projection
	}

	payload := anthropicCachePayload{
		Instructions: strings.TrimSpace(convertInstructionsFromMessages(req.ConversationMessages())),
	}
	selectedTTL := ""

	for _, tool := range req.Tools {
		if tool.Type != "function" {
			continue
		}
		payload.Tools = append(payload.Tools, anthropicCacheTool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  cloneRawJSON(tool.Function.Parameters),
			Strict:      tool.Function.Strict,
			TTL:         cacheControlTTL(tool.CacheControl),
		})
		if selectedTTL == "" {
			selectedTTL = cacheControlTTL(tool.CacheControl)
		}
	}

	stableMessageLimit, messageAnchor, messageTTL := stableAnthropicMessageLimit(req.ConversationMessages())
	if payload.Instructions != "" {
		projection.anchor = "system"
		if selectedTTL == "" {
			selectedTTL = stableSystemCacheTTL(req.ConversationMessages())
		}
	}
	if len(payload.Tools) > 0 {
		projection.anchor = "tools"
	}
	if stableMessageLimit >= 0 {
		if projection.anchor == "" || projection.anchor == "system" {
			projection.anchor = messageAnchor
		}
		if selectedTTL == "" {
			selectedTTL = messageTTL
		}
		for i := 0; i <= stableMessageLimit && i < len(req.ConversationMessages()); i++ {
			msg := req.ConversationMessages()[i]
			if msg.Role == "system" || msg.Role == "developer" {
				continue
			}
			if cacheMsg, ok := cacheMessageFromMessage(msg); ok {
				payload.Messages = append(payload.Messages, cacheMsg)
			}
		}
	}

	if payload.Instructions == "" && len(payload.Tools) == 0 && len(payload.Messages) == 0 {
		projection.anchor = "none"
		return projection
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return projection
	}
	projection.payload = data
	projection.ok = true
	if selectedTTL == model.CacheTTL1h {
		value := anthropicPromptCacheRetention24h
		projection.retention = &value
	}
	if projection.anchor == "" {
		projection.anchor = "unknown"
	}
	return projection
}

func stableAnthropicMessageLimit(messages []model.Message) (int, string, string) {
	userIndexes := make([]int, 0, len(messages))
	for i, msg := range messages {
		if msg.Role == "user" {
			userIndexes = append(userIndexes, i)
		}
	}
	if len(userIndexes) >= 2 {
		idx := userIndexes[len(userIndexes)-2]
		return idx, "previous_user", messageCacheTTL(messages[idx])
	}
	if len(userIndexes) == 1 {
		idx := userIndexes[0]
		return idx, "final_user_fallback", messageCacheTTL(messages[idx])
	}
	return -1, "", ""
}

func cacheMessageFromMessage(msg model.Message) (anthropicCacheMessage, bool) {
	cacheMessage := anthropicCacheMessage{Role: msg.Role}
	if msg.Content.Content != nil {
		cacheMessage.Parts = append(cacheMessage.Parts, anthropicCachePart{Type: "text", Text: strings.TrimSpace(*msg.Content.Content), TTL: cacheControlTTL(msg.CacheControl)})
	}
	for _, part := range msg.Content.MultipleContent {
		cachePart := anthropicCachePart{Type: part.Type, TTL: cacheControlTTL(part.CacheControl)}
		switch part.Type {
		case "text":
			if part.Text != nil {
				cachePart.Text = strings.TrimSpace(*part.Text)
			}
		case "server_tool_use":
			if part.ServerToolUse != nil {
				cachePart.Name = part.ServerToolUse.Name
				cachePart.Input = cloneRawJSON(part.ServerToolUse.Input)
			}
		case "server_tool_result":
			if part.ServerToolResult != nil {
				cachePart.IsError = part.ServerToolResult.IsError != nil && *part.ServerToolResult.IsError
				cachePart.Content = cloneRawJSON(part.ServerToolResult.Content)
			}
		default:
			continue
		}
		cacheMessage.Parts = append(cacheMessage.Parts, cachePart)
	}
	if msg.ToolCallID != nil && msg.Content.Content != nil {
		cacheMessage.Parts = append(cacheMessage.Parts, anthropicCachePart{Type: "tool_result", Text: strings.TrimSpace(*msg.Content.Content), TTL: cacheControlTTL(msg.CacheControl)})
	}
	for _, toolCall := range msg.ToolCalls {
		cacheMessage.Parts = append(cacheMessage.Parts, anthropicCachePart{Type: "tool_use", Name: toolCall.Function.Name, Text: strings.TrimSpace(toolCall.Function.Arguments), TTL: cacheControlTTL(toolCall.CacheControl)})
	}
	return cacheMessage, len(cacheMessage.Parts) > 0
}

func requestHasCacheControl(req *model.InternalLLMRequest) bool {
	if req == nil {
		return false
	}
	for _, msg := range req.ConversationMessages() {
		if msg.CacheControl != nil {
			return true
		}
		for _, part := range msg.Content.MultipleContent {
			if part.CacheControl != nil {
				return true
			}
		}
		for _, toolCall := range msg.ToolCalls {
			if toolCall.CacheControl != nil {
				return true
			}
		}
	}
	for _, tool := range req.Tools {
		if tool.CacheControl != nil {
			return true
		}
	}
	return false
}

func cacheControlTTL(cacheControl *model.CacheControl) string {
	if cacheControl == nil {
		return ""
	}
	return cacheControl.TTL
}

func stableSystemCacheTTL(messages []model.Message) string {
	for _, msg := range messages {
		if msg.Role != "system" && msg.Role != "developer" {
			continue
		}
		if ttl := cacheControlTTL(msg.CacheControl); ttl != "" {
			return ttl
		}
		for _, part := range msg.Content.MultipleContent {
			if ttl := cacheControlTTL(part.CacheControl); ttl != "" {
				return ttl
			}
		}
	}
	return ""
}

func messageCacheTTL(msg model.Message) string {
	if ttl := cacheControlTTL(msg.CacheControl); ttl != "" {
		return ttl
	}
	for _, part := range msg.Content.MultipleContent {
		if ttl := cacheControlTTL(part.CacheControl); ttl != "" {
			return ttl
		}
	}
	for _, toolCall := range msg.ToolCalls {
		if ttl := cacheControlTTL(toolCall.CacheControl); ttl != "" {
			return ttl
		}
	}
	return ""
}

func cloneRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
