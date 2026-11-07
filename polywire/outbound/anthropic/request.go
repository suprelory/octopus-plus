package anthropic

import (
	"strings"

	"github.com/samber/lo"
	"github.com/suprelory/octopus-plus/polywire/model"
	anthropicModel "github.com/suprelory/octopus-plus/polywire/protocol/anthropic"
)

// convertToAnthropicRequest converts internal LLM request to Anthropic format
func (o *MessageOutbound) convertToAnthropicRequest(req *model.InternalLLMRequest) *anthropicModel.MessageRequest {
	result := o.convertToAnthropicRequestUnpruned(req)
	pruneCacheBreakpoints(result)
	return result
}

func (o *MessageOutbound) convertToAnthropicRequestUnpruned(req *model.InternalLLMRequest) *anthropicModel.MessageRequest {
	result := &anthropicModel.MessageRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		TopK:        req.TopK,
		Stream:      req.Stream,
		MaxTokens:   resolveMaxTokens(req),
		System:      convertSystemPrompt(req),
	}

	if req.ServiceTier != nil {
		result.ServiceTier = strings.TrimSpace(*req.ServiceTier)
	}

	if userID := resolveAnthropicUserID(req); userID != "" {
		result.Metadata = &anthropicModel.AnthropicMetadata{UserID: userID}
	}

	// mcp_servers / container (A-H6): write the raw payload back if the
	// inbound preserved one. Allocating fresh byte slices keeps the
	// outbound request independent from the shared InternalLLMRequest in
	// case downstream handlers re-emit the same request body.
	anthropicExt := req.GetAnthropicExtensions()
	if len(anthropicExt.MCPServers) > 0 {
		result.MCPServers = append(result.MCPServers[:0], anthropicExt.MCPServers...)
	}
	if len(anthropicExt.Container) > 0 {
		result.Container = append(result.Container[:0], anthropicExt.Container...)
	}

	// Convert messages
	result.Messages = o.convertMessages(req)

	// Convert tools
	if len(req.Tools) > 0 {
		result.Tools = o.convertTools(req.Tools)
	}

	// Convert stop sequences
	if req.Stop != nil {
		result.StopSequences = o.convertStopSequences(req.Stop)
	}

	// Convert thinking/reasoning
	if req.ReasoningEffort != "" {
		if req.AdaptiveThinking {
			result.Thinking = &anthropicModel.Thinking{
				Type:    anthropicModel.ThinkingTypeAdaptive,
				Display: req.ThinkingDisplay,
			}
			result.OutputConfig = &anthropicModel.OutputConfig{
				Effort: req.ReasoningEffort,
			}
		} else {
			result.Thinking = &anthropicModel.Thinking{
				Type:         anthropicModel.ThinkingTypeEnabled,
				BudgetTokens: getThinkingBudget(req.ReasoningEffort, req.ReasoningBudget),
				Display:      req.ThinkingDisplay,
			}
		}
	}

	// A-H4: Anthropic rejects temperature != 1 and any top_p/top_k when
	// extended thinking is active. Force the sampling knobs to the only
	// values the API accepts so downstream 400s don't leak to the caller.
	applyThinkingParamConstraints(result)

	// Convert tool choice
	if tc := convertToolChoice(req.ToolChoiceForTarget(model.APIFormatAnthropicMessage)); tc != nil {
		result.ToolChoice = tc
	}

	return result
}

// applyThinkingParamConstraints enforces Anthropic's documented restrictions
// on sampling parameters when extended thinking is active. The API requires
// temperature == 1.0 and rejects top_p / top_k outright; callers that set
// conflicting values would otherwise receive a 400 upstream. We normalise
// silently so pass-through requests keep working across clients that do not
// know the rule.
func applyThinkingParamConstraints(req *anthropicModel.MessageRequest) {
	if req == nil || req.Thinking == nil {
		return
	}
	switch req.Thinking.Type {
	case anthropicModel.ThinkingTypeEnabled, anthropicModel.ThinkingTypeAdaptive:
	default:
		return
	}
	req.Temperature = lo.ToPtr(1.0)
	req.TopP = nil
	req.TopK = nil
}

func resolveAnthropicUserID(req *model.InternalLLMRequest) string {
	if req == nil {
		return ""
	}
	if req.Metadata != nil {
		if userID := strings.TrimSpace(req.Metadata["user_id"]); userID != "" {
			return userID
		}
	}
	if userID := req.TransformerMetadataValue(model.TransformerMetadataAnthropicUserID); userID != "" {
		return userID
	}
	if req.User != nil {
		return strings.TrimSpace(*req.User)
	}
	return ""
}

// convertToolChoice maps the internal ToolChoice into the Anthropic wire
// shape: {type, name?, disable_parallel_tool_use?}. The string form
// ("auto"/"none"/"required"/"any") is normalised into the Anthropic enum,
// and OpenAI-style {type:"function", function:{name}} is re-expressed as
// {type:"tool", name}. Anthropic's schema rejects unknown types, so we drop
// anything we can't translate rather than passing it through.
func convertToolChoice(tc *model.ToolChoice) *anthropicModel.ToolChoice {
	if tc == nil {
		return nil
	}
	if tc.ToolChoice != nil {
		switch strings.ToLower(*tc.ToolChoice) {
		case "auto":
			return &anthropicModel.ToolChoice{Type: "auto"}
		case "none":
			return &anthropicModel.ToolChoice{Type: "none"}
		case "required", "any":
			return &anthropicModel.ToolChoice{Type: "any"}
		default:
			return nil
		}
	}
	named := tc.NamedToolChoice
	if named == nil {
		return nil
	}
	out := &anthropicModel.ToolChoice{
		DisableParallelToolUse: named.DisableParallelToolUse,
	}
	switch strings.ToLower(named.Type) {
	case "auto":
		out.Type = "auto"
	case "any", "required":
		out.Type = "any"
	case "none":
		out.Type = "none"
	case "tool", "function":
		out.Type = "tool"
		if name := named.ResolvedFunctionName(); name != "" {
			n := name
			out.Name = &n
		} else {
			// tool type requires a name on Anthropic; without one the
			// request would 400. Fall back to auto so the request stays
			// valid.
			out.Type = "auto"
		}
	default:
		return nil
	}
	return out
}

func resolveMaxTokens(req *model.InternalLLMRequest) int64 {
	var maxtoken int64 = 1
	switch {
	case req.MaxTokens != nil:
		maxtoken = *req.MaxTokens
	case req.MaxCompletionTokens != nil:
		maxtoken = *req.MaxCompletionTokens
	default:
		maxtoken = 8192
	}
	if maxtoken < 1 {
		maxtoken = 1
	}
	return maxtoken
}

func convertSystemPrompt(req *model.InternalLLMRequest) *anthropicModel.SystemPrompt {
	var systemMessages []model.Message
	for _, msg := range req.ConversationMessages() {
		if msg.Role == "system" || msg.Role == "developer" {
			systemMessages = append(systemMessages, msg)
		}
	}

	if len(systemMessages) == 0 {
		return nil
	}

	if len(systemMessages) == 1 {
		return &anthropicModel.SystemPrompt{
			MultiplePrompts: []anthropicModel.SystemPromptPart{{
				Type:         "text",
				Text:         lo.FromPtr(systemMessages[0].Content.Content),
				CacheControl: convertCacheControl(systemMessages[0].CacheControl),
			}},
		}
	}

	parts := make([]anthropicModel.SystemPromptPart, 0, len(systemMessages))
	for _, msg := range systemMessages {
		parts = append(parts, anthropicModel.SystemPromptPart{
			Type:         "text",
			Text:         lo.FromPtr(msg.Content.Content),
			CacheControl: convertCacheControl(msg.CacheControl),
		})
	}
	return &anthropicModel.SystemPrompt{
		MultiplePrompts: parts,
	}
}

func (o *MessageOutbound) convertTools(tools []model.Tool) []anthropicModel.Tool {
	result := make([]anthropicModel.Tool, 0, len(tools))
	for _, tool := range tools {
		switch tool.Type {
		case "function", "":
			result = append(result, anthropicModel.Tool{
				Name:         tool.Function.Name,
				Description:  tool.Function.Description,
				InputSchema:  tool.Function.Parameters,
				CacheControl: convertCacheControl(tool.CacheControl),
			})
		default:
			// Anthropic server tools (web_search_*, code_execution_*,
			// computer_*): replay the raw spec captured at inbound time so
			// provider-specific fields (max_uses, allowed_domains,
			// display_width_px, ...) survive without enumerating every
			// variant here. The MarshalJSON on anthropicModel.Tool handles
			// the raw-body passthrough.
			if len(tool.AnthropicServerSpec) == 0 {
				o.config.Log().Warnw("transformer.anthropic.server_tool.missing_spec",
					"tool_type", tool.Type,
					"tool_name", tool.Function.Name,
				)
				continue
			}
			result = append(result, anthropicModel.Tool{
				Type:         tool.Type,
				Name:         tool.Function.Name,
				RawBody:      tool.AnthropicServerSpec,
				CacheControl: convertCacheControl(tool.CacheControl),
			})
		}
	}
	return result
}

// MaxStopSequences is the documented/observed Anthropic limit for the
// stop_sequences array. The outbound capability planner consumes the same
// constant so static loss reports cannot drift from wire behavior.
const MaxStopSequences = 4

// anthropicMaxStopSequences caps the stop_sequences array sent to
// Anthropic. The API documents a limit but only surfaces it as an
// opaque "stop_sequences: too many items" 400 when exceeded; 4 is the
// empirically-observed ceiling as of 2026-04. Declared as a var so
// tests can tighten the threshold without allocating fixture entries.
// A-L5. Ref: https://docs.anthropic.com/en/api/messages
var anthropicMaxStopSequences = MaxStopSequences

func (o *MessageOutbound) convertStopSequences(stop *model.Stop) []string {
	if stop == nil {
		return nil
	}
	var seqs []string
	if stop.Stop != nil {
		seqs = []string{*stop.Stop}
	} else if len(stop.MultipleStop) > 0 {
		seqs = stop.MultipleStop
	}
	if len(seqs) > anthropicMaxStopSequences {
		o.config.Log().Warnf("anthropic: stop_sequences has %d entries; truncating to %d to avoid upstream 400", len(seqs), anthropicMaxStopSequences)
		seqs = seqs[:anthropicMaxStopSequences]
	}
	return seqs
}

func convertCacheControl(cc *model.CacheControl) *anthropicModel.CacheControl {
	if cc == nil {
		return nil
	}
	// Drop provider-rejected values before emitting Anthropic wire payloads.
	if cc.Type != "" && cc.Type != model.CacheControlTypeEphemeral {
		return nil
	}
	ttl := cc.TTL
	if ttl != "" && ttl != model.CacheTTL5m && ttl != model.CacheTTL1h {
		ttl = ""
	}
	return &anthropicModel.CacheControl{
		Type: cc.Type,
		TTL:  ttl,
	}
}

func getThinkingBudget(effort string, budget *int64) *int64 {
	if budget != nil {
		return budget
	}

	var result int64
	switch effort {
	case anthropicModel.EffortLow:
		result = 1024
	case anthropicModel.EffortMedium:
		result = 8192
	case anthropicModel.EffortHigh:
		result = 32768
	default:
		result = 8192
	}
	return &result
}
