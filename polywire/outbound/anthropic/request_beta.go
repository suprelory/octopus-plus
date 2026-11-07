package anthropic

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
	anthropicModel "github.com/suprelory/octopus-plus/polywire/protocol/anthropic"
)

// collectAnthropicBetaHeaders scans the outbound MessageRequest for features
// that require an `anthropic-beta` header and returns the gated values.
// Covers:
//   - cache_control.ttl == "1h" → extended-cache-ttl-2025-04-11 (A-C3)
//   - server tools (web_search_*, code_execution_*, computer_*) → the matching
//     beta required for the respective server tool (A-H5)
//   - mcp_servers != nil → mcp-client-2025-11-20 (A-H7)
//   - response_format json_schema on the internal request →
//     structured-outputs-2025-11-13 (A-H7)
//   - extended-thinking + tool_use interleaved in any assistant turn →
//     interleaved-thinking-2025-05-14 (A-H7)
//   - TransformerMetadata["anthropic_context_1m"]=="true" on eligible
//     Sonnet 4 / 4.5 models → context-1m-2025-08-07 (A-H7)
//   - any content block sourced from the Files API (source.type=="file")
//     → files-api-2025-04-14 (A-H7)
//   - streaming request with tools → fine-grained-tool-streaming-2025-05-14
//     (A-H7). Latency-beneficial and documented as safe to always opt in.
//   - any tool with defer_loading=true → tool-search-tool-2025-10-19 (A-H7)
//
// Returning a slice (de-duplicated, order-preserving) lets callers join with
// a comma; multiple beta tags are valid in a single header per the Anthropic
// beta-headers spec.
// Ref: https://docs.anthropic.com/en/api/beta-headers
// Ref: https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
func collectAnthropicBetaHeaders(req *anthropicModel.MessageRequest, internal *model.InternalLLMRequest) []string {
	if req == nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 4)
	add := func(beta string) {
		if beta == "" {
			return
		}
		if _, ok := seen[beta]; ok {
			return
		}
		seen[beta] = struct{}{}
		out = append(out, beta)
	}

	inspect := func(cc *anthropicModel.CacheControl) {
		if cc == nil {
			return
		}
		if cc.TTL == model.CacheTTL1h {
			add("extended-cache-ttl-2025-04-11")
		}
	}

	if req.System != nil {
		for i := range req.System.MultiplePrompts {
			inspect(req.System.MultiplePrompts[i].CacheControl)
		}
	}
	for i := range req.Tools {
		inspect(req.Tools[i].CacheControl)
		add(anthropicServerToolBeta(req.Tools[i].Type))
	}
	for i := range req.Messages {
		msg := &req.Messages[i]
		for j := range msg.Content.MultipleContent {
			inspect(msg.Content.MultipleContent[j].CacheControl)
		}
	}

	// mcp_servers (A-H7).
	if len(req.MCPServers) > 0 {
		add("mcp-client-2025-11-20")
	}

	// structured-outputs (A-H7). Structured outputs are an OpenAI concept the
	// Anthropic side adopts via a beta header — the signal lives on the
	// internal (cross-provider) request rather than the Anthropic wire body.
	if internal != nil && internal.ResponseFormat != nil {
		rf := internal.ResponseFormat
		hasSchema := rf.Schema != nil || len(rf.RawSchema) > 0 || len(rf.JSONSchema) > 0
		if rf.Type == "json_schema" || hasSchema {
			add("structured-outputs-2025-11-13")
		}
	}

	// interleaved-thinking (A-H7): extended-thinking with tool_use blocks.
	if req.Thinking != nil && req.Thinking.Type == anthropicModel.ThinkingTypeEnabled {
		if hasInterleavedThinkingAndToolUse(req.Messages) {
			add("interleaved-thinking-2025-05-14")
		}
	}

	// context-1m (A-H7). Requires Sonnet 4 / 4.5 family and an explicit
	// opt-in metadata flag because enabling it changes per-token pricing.
	// Opus 4.6 and Sonnet 4.6 support 1M natively without the beta header.
	if internal != nil && isContext1MEligibleModel(req.Model) {
		if internal.TransformerMetadataBool(model.TransformerMetadataAnthropicContext1M) {
			add("context-1m-2025-08-07")
		}
	}

	// files-api (A-H7): any block sourced from a Files API upload.
	if hasFileSourceBlock(req.Messages) {
		add("files-api-2025-04-14")
	}

	// fine-grained-tool-streaming (A-H7): streaming + tools.
	if req.Stream != nil && *req.Stream && len(req.Tools) > 0 {
		add("fine-grained-tool-streaming-2025-05-14")
	}

	// tool-search-tool (A-H7).
	if hasDeferLoadingTool(req.Tools) {
		add("tool-search-tool-2025-10-19")
	}

	return out
}

// hasInterleavedThinkingAndToolUse reports whether any assistant message
// contains both a thinking block and a tool_use block in the same turn —
// the trigger condition for interleaved-thinking-2025-05-14.
func hasInterleavedThinkingAndToolUse(messages []anthropicModel.MessageParam) bool {
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		var sawThinking, sawToolUse bool
		for _, b := range m.Content.MultipleContent {
			switch b.Type {
			case "thinking", "redacted_thinking":
				sawThinking = true
			case "tool_use", "server_tool_use":
				sawToolUse = true
			}
			if sawThinking && sawToolUse {
				return true
			}
		}
	}
	return false
}

// isContext1MEligibleModel reports whether the model family accepts the
// context-1m beta. Sonnet 4 and Sonnet 4.5 need the header explicitly;
// Opus 4.6 and Sonnet 4.6 do NOT (1M is native there).
func isContext1MEligibleModel(modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return false
	}
	// Exclude models that support 1M natively without the beta — matching
	// them would produce a harmless but noisy header.
	if strings.Contains(id, "sonnet-4-6") || strings.Contains(id, "sonnet-4.6") ||
		strings.Contains(id, "opus-4-6") || strings.Contains(id, "opus-4.6") {
		return false
	}
	return strings.Contains(id, "sonnet-4") || strings.Contains(id, "sonnet-4-5") ||
		strings.Contains(id, "sonnet-4.5")
}

// hasFileSourceBlock scans the message history for any content block whose
// source is a File API handle. Triggers the files-api-2025-04-14 beta.
func hasFileSourceBlock(messages []anthropicModel.MessageParam) bool {
	for _, m := range messages {
		for _, b := range m.Content.MultipleContent {
			if b.Source != nil && b.Source.Type == "file" {
				return true
			}
		}
	}
	return false
}

// hasDeferLoadingTool reports whether any tool in the request has
// defer_loading=true. Scans both the explicit DeferLoading field (function
// tools) and the RawBody (server tools preserve the full JSON including
// defer_loading).
func hasDeferLoadingTool(tools []anthropicModel.Tool) bool {
	for i := range tools {
		if tools[i].DeferLoading != nil && *tools[i].DeferLoading {
			return true
		}
		if tools[i].IsServerTool() && len(tools[i].RawBody) > 0 {
			if bytes.Contains(tools[i].RawBody, []byte(`"defer_loading":true`)) ||
				bytes.Contains(tools[i].RawBody, []byte(`"defer_loading": true`)) {
				return true
			}
		}
	}
	return false
}

// anthropicServerToolBeta maps an Anthropic server-tool `type` (e.g.
// "web_search_20250305") to its required anthropic-beta value. Returns an
// empty string for function/custom tools that need no beta.
func anthropicServerToolBeta(toolType string) string {
	switch {
	case toolType == "" || toolType == "function" || toolType == "custom":
		return ""
	case strings.HasPrefix(toolType, "web_search_"):
		return "web-search-2025-03-05"
	case strings.HasPrefix(toolType, "code_execution_"):
		return "code-execution-2025-05-22"
	case strings.HasPrefix(toolType, "computer_"):
		return "computer-use-2025-01-24"
	default:
		return ""
	}
}

// pruneCacheBreakpoints walks the Anthropic request after conversion and drops cache_control
// entries that exceed the provider-enforced ceiling. Anthropic currently allows up to 4
// breakpoints per request; we keep the first N in encounter order (system → tools → messages)
// because callers typically mark the most reusable prefixes first.
func pruneCacheBreakpoints(req *anthropicModel.MessageRequest) []string {
	if req == nil {
		return nil
	}

	kept := 0
	var dropped []string
	keepOrClear := func(field string, cc **anthropicModel.CacheControl) {
		if cc == nil || *cc == nil {
			return
		}
		if kept >= model.AnthropicMaxCacheBreakpoints {
			*cc = nil
			dropped = append(dropped, field)
			return
		}
		kept++
	}

	if req.System != nil {
		for i := range req.System.MultiplePrompts {
			keepOrClear(fmt.Sprintf("system[%d].cache_control", i), &req.System.MultiplePrompts[i].CacheControl)
		}
	}
	for i := range req.Tools {
		keepOrClear(fmt.Sprintf("tools[%d].cache_control", i), &req.Tools[i].CacheControl)
	}
	for i := range req.Messages {
		msg := &req.Messages[i]
		for j := range msg.Content.MultipleContent {
			keepOrClear(fmt.Sprintf("messages[%d].content[%d].cache_control", i, j), &msg.Content.MultipleContent[j].CacheControl)
		}
	}
	return dropped
}
