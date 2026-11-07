package outbound

import (
	"errors"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
	geminiOutbound "github.com/suprelory/octopus-plus/polywire/outbound/gemini"
)

func evaluateFeature(req *model.InternalLLMRequest, effectiveModel string, outboundType OutboundType, feature SemanticFeature, decision *CapabilityDecision) {
	switch feature {
	case FeatureStructuredOutput:
		switch outboundType {
		case OutboundTypeAnthropic:
			degrade(decision, "response_format", "Anthropic transformer cannot emit the canonical response_format body")
		case OutboundTypeGemini:
			if _, err := req.ResponseFormat.ToGeminiSchema(); err != nil {
				action := LossActionReject
				if errors.Is(err, model.ErrSchemaLossy) {
					action = LossActionTranslate
				}
				reportLoss(decision, "response_format.schema", action, err.Error())
			}
		}
	case FeatureToolChoice:
		if req.ToolChoice != nil && req.ToolChoiceForTarget(decision.OutboundFormat) == nil {
			reportChange(decision, CapabilityLoss{
				Field:          "tool_choice",
				Action:         LossActionDrop,
				Reason:         "target protocol cannot honor a choice that forces a dropped native tool",
				NativeSemantic: true,
			})
			return
		}
		if outboundType == OutboundTypeAnthropic && req.ToolChoice != nil && req.ToolChoice.NamedToolChoice != nil {
			named := req.ToolChoice.NamedToolChoice
			typ := strings.ToLower(strings.TrimSpace(named.Type))
			if (typ == "tool" || typ == "function") && strings.TrimSpace(named.ResolvedFunctionName()) == "" {
				reportLoss(decision, "tool_choice.name", LossActionRepair,
					"Anthropic requires a tool name; the outbound adapter repairs this choice to auto")
			}
		}
		if req.ToolChoice != nil && req.ToolChoice.NamedToolChoice != nil && req.ToolChoice.NamedToolChoice.DisableParallelToolUse != nil && outboundType != OutboundTypeAnthropic {
			degrade(decision, "tool_choice.disable_parallel_tool_use", "target protocol has no equivalent disable_parallel_tool_use control")
		}
	case FeatureReasoning:
		evaluateReasoning(req, outboundType, decision)
		if outboundType == OutboundTypeGemini {
			for _, change := range geminiOutbound.DescribeThinkingConfigChanges(effectiveModel, req.ReasoningBudget, req.ReasoningEffort, req.AdaptiveThinking) {
				action := LossActionRepair
				if change.Dropped {
					action = LossActionDrop
				} else if change.Translated {
					action = LossActionTranslate
				}
				reportLoss(decision, change.Field, action, change.Reason)
			}
		}
	case FeatureMultimodal:
		evaluateMultimodal(req, outboundType, decision)
	case FeatureStreamUsage:
		// Every streaming chat adapter is required to expose canonical usage.
	}
}

func evaluateMultimodal(req *model.InternalLLMRequest, outboundType OutboundType, decision *CapabilityDecision) {
	for messageIndex, message := range req.ConversationMessages() {
		for partIndex, part := range message.Content.MultipleContent {
			typ := strings.ToLower(strings.TrimSpace(part.Type))
			if outboundType == OutboundTypeOpenAIChat && typ == "document" {
				reportLoss(decision, fmt.Sprintf("messages[%d].content[%d]", messageIndex, partIndex), LossActionTranslate,
					"OpenAI Chat converts document content to a plain-text hint")
				continue
			}
			if supportsContentPart(outboundType, typ) {
				continue
			}
			reportChange(decision, CapabilityLoss{
				Field:          fmt.Sprintf("messages[%d].content[%d]", messageIndex, partIndex),
				Action:         LossActionDrop,
				Reason:         fmt.Sprintf("content part %q is not natively representable on %s", typ, outboundType),
				NativeSemantic: req.RawAPIFormat == model.APIFormatAnthropicMessage && (typ == "server_tool_use" || typ == "server_tool_result"),
			})
		}
	}
	if len(req.Modalities) > 0 {
		for _, modality := range req.Modalities {
			if strings.EqualFold(modality, "text") {
				continue
			}
			if outboundType == OutboundTypeGemini {
				if !geminiOutbound.SupportsResponseModality(modality) {
					reportLoss(decision, "modalities", LossActionDrop,
						fmt.Sprintf("output modality %q is dropped because Gemini does not support it", modality))
				}
				continue
			}
			if outboundType == OutboundTypeAnthropic || outboundType == OutboundTypeOpenAIResponse {
				degrade(decision, "modalities", fmt.Sprintf("output modality %q is not supported by %s", modality, outboundType))
			}
		}
	}
}

func evaluateReasoning(req *model.InternalLLMRequest, outboundType OutboundType, decision *CapabilityDecision) {
	degradeIf := func(condition bool, field string) {
		if condition {
			degrade(decision, field, fmt.Sprintf("%s does not preserve %s", outboundType, field))
		}
	}
	options := req.GetOpenAIResponsesOptions()
	usesSummary := options.ReasoningSummary != nil || options.ReasoningGenerateSummary != nil
	switch outboundType {
	case OutboundTypeOpenAIChat:
		degradeIf(req.ReasoningBudget != nil, "reasoning_budget")
		degradeIf(req.AdaptiveThinking, "adaptive_thinking")
		degradeIf(req.EnableThinking != nil, "enable_thinking")
		degradeIf(usesSummary, "reasoning_summary")
	case OutboundTypeOpenAIResponse:
		degradeIf(req.AdaptiveThinking, "adaptive_thinking")
		degradeIf(req.EnableThinking != nil, "enable_thinking")
		degradeIf(req.Thinking != nil, "thinking")
	case OutboundTypeAnthropic:
		degradeIf(req.ReasoningBudget != nil && req.ReasoningEffort == "", "reasoning_budget")
		degradeIf(req.EnableThinking != nil, "enable_thinking")
		degradeIf(req.Thinking != nil, "thinking")
		degradeIf(usesSummary, "reasoning_summary")
	case OutboundTypeGemini:
		degradeIf(req.EnableThinking != nil, "enable_thinking")
		degradeIf(req.Thinking != nil, "thinking")
		degradeIf(usesSummary, "reasoning_summary")
	}
}

func supportsContentPart(outboundType OutboundType, typ string) bool {
	switch typ {
	case "", "text", "image_url":
		return true
	case "input_audio":
		return outboundType == OutboundTypeOpenAIChat || outboundType == OutboundTypeOpenAIResponse || outboundType == OutboundTypeGemini
	case "file":
		return outboundType == OutboundTypeOpenAIResponse || outboundType == OutboundTypeGemini
	case "document":
		return outboundType == OutboundTypeAnthropic || outboundType == OutboundTypeGemini
	case "server_tool_use", "server_tool_result":
		return outboundType == OutboundTypeAnthropic
	default:
		return false
	}
}

func hasStructuredOutput(format *model.ResponseFormat) bool {
	if format == nil {
		return false
	}
	return format.Type == "json_object" || format.Type == "json_schema" || format.Schema != nil || len(format.RawSchema) > 0 || len(format.JSONSchema) > 0
}

func hasMultimodalSemantics(req *model.InternalLLMRequest) bool {
	for _, modality := range req.Modalities {
		if !strings.EqualFold(modality, "text") {
			return true
		}
	}
	for _, message := range req.ConversationMessages() {
		for _, part := range message.Content.MultipleContent {
			if typ := strings.ToLower(strings.TrimSpace(part.Type)); typ != "" && typ != "text" {
				return true
			}
		}
	}
	return false
}
