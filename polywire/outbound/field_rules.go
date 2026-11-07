package outbound

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

// FieldConversionRule declares a semantic mapping and the condition that
// produces a report. Scalar rules compare the actual serialized wire value;
// provider rules share the adapter's preparation helpers for structured data.
type FieldConversionRule struct {
	SourceSemantic string     `json:"source_semantic"`
	TargetField    string     `json:"target_field"`
	Action         LossAction `json:"action"`
	Condition      string     `json:"condition"`
	Reason         string     `json:"reason"`
	applies        func(*model.InternalLLMRequest) bool
	evaluate       func(conversionInput) LossReport
}

type conversionInput struct {
	request      *model.InternalLLMRequest
	adapter      model.Outbound
	targetFormat model.APIFormat
	source, wire map[string]any
}

func scalarRule(source, target string, action LossAction, condition, reason string) FieldConversionRule {
	return FieldConversionRule{SourceSemantic: source, TargetField: target, Action: action, Condition: condition, Reason: reason}
}

func protocolFieldRules(typ OutboundType) []FieldConversionRule {
	var rules []FieldConversionRule
	drop := func(fields ...string) {
		for _, field := range fields {
			rules = append(rules, scalarRule(field, field, LossActionDrop, "source present and target absent", fmt.Sprintf("%s outbound does not preserve %s", typ, field)))
		}
	}
	switch typ {
	case OutboundTypeOpenAIChat:
		drop("top_k")
		rules = append(rules, scalarRule("stop", "stop", LossActionDrop, "source present and target absent", "OpenAI Chat drops Anthropic stop_sequences to avoid changing stop semantics"))
	case OutboundTypeOpenAIResponse:
		drop("audio", "frequency_penalty", "logit_bias", "logprobs", "prediction", "presence_penalty", "seed", "stop", "top_k", "user", "web_search_options")
	case OutboundTypeAnthropic:
		drop("audio", "frequency_penalty", "logit_bias", "logprobs", "prediction", "presence_penalty", "seed", "top_logprobs", "web_search_options")
		rules = append(rules,
			scalarRule("metadata", "metadata", LossActionDrop, "source differs from target", "Anthropic outbound preserves only metadata.user_id"),
			scalarRule("user", "metadata.user_id", LossActionDrop, "source differs from target", "Anthropic metadata.user_id overrides the generic user field"),
			scalarRule("stop", "stop_sequences", LossActionTruncate, "source differs from target", "Anthropic truncates stop_sequences to the wire builder's limit"),
			scalarRule("temperature", "temperature", LossActionRepair, "source differs from target", "Anthropic forces temperature to 1 when extended thinking is enabled"),
			scalarRule("top_p", "top_p", LossActionDrop, "source present and target absent", "Anthropic removes top_p when extended thinking is enabled"),
			scalarRule("top_k", "top_k", LossActionDrop, "source present and target absent", "Anthropic removes top_k when extended thinking is enabled"),
		)
		for _, field := range []string{"max_tokens", "max_completion_tokens"} {
			rule := scalarRule(field, "max_tokens", LossActionRepair, "source differs from target", "Anthropic repairs the output token limit to a positive value")
			rule.applies = func(req *model.InternalLLMRequest) bool { return field == "max_tokens" || req.MaxTokens == nil }
			rules = append(rules, rule)
		}
	case OutboundTypeGemini:
		drop("logit_bias", "prediction", "user", "web_search_options")
		rules = append(rules, scalarRule("top_logprobs", "generationConfig.logprobs", LossActionTruncate, "source differs from target", "Gemini clamps top_logprobs to the wire builder's supported range"))
	}
	if typ == OutboundTypeOpenAIEmbedding {
		return rules
	}
	for _, feature := range []SemanticFeature{FeatureStructuredOutput, FeatureToolChoice, FeatureReasoning, FeatureMultimodal} {
		rule := FieldConversionRule{SourceSemantic: string(feature), TargetField: targetSemanticField(typ, feature), Action: LossActionTranslate, Condition: "provider conversion changes semantic content", Reason: "provider preparation reports this conversion"}
		rule.evaluate = func(input conversionInput) LossReport {
			if !slices.Contains(requestedFeatures(input.request), string(feature)) {
				return nil
			}
			decision := CapabilityDecision{OutboundFormat: input.targetFormat}
			evaluateFeature(input.request, input.request.Model, typ, feature, &decision)
			return decision.Losses
		}
		rules = append(rules, rule)
	}
	rules = append(rules,
		FieldConversionRule{SourceSemantic: "tools", TargetField: "tools", Action: LossActionTranslate, Condition: "tool definition differs from actual wire", Reason: "tool type and schema must survive the target representation", evaluate: func(input conversionInput) LossReport { return reportWireTools(input, typ) }},
		FieldConversionRule{SourceSemantic: "messages", TargetField: targetSemanticField(typ, FeatureMultimodal), Action: LossActionRepair, Condition: "adapter preparation changes request", Reason: "adapter preparation reports repairs and native field conversion", evaluate: func(input conversionInput) LossReport {
			if reporter, ok := input.adapter.(model.RequestChangeReporter); ok {
				return reporter.DescribeRequestChanges(input.request, input.request.Model)
			}
			return nil
		}},
		FieldConversionRule{SourceSemantic: "provider_extensions", TargetField: "provider_extensions", Action: LossActionDrop, Condition: "target cannot represent provider extension", Reason: "provider extension has no target representation", evaluate: func(input conversionInput) LossReport {
			var decision CapabilityDecision
			evaluateInboundRepairs(input.request, &decision)
			return decision.Losses
		}},
	)
	if typ == OutboundTypeOpenAIResponse {
		rules = append(rules, FieldConversionRule{SourceSemantic: "responses.input", TargetField: "input", Action: LossActionPreserve, Condition: "raw input sidecar present", Reason: "Responses recovers native input items from the authoritative raw sidecar", evaluate: func(input conversionInput) LossReport {
			if len(input.request.OpenAIRawInputItems()) == 0 {
				return nil
			}
			var raw any
			if json.Unmarshal(input.request.OpenAIRawInputItems(), &raw) != nil {
				return nil
			}
			action, reason := LossActionPreserve, "Responses recovers native input items from the authoritative raw sidecar"
			if !reflect.DeepEqual(raw, input.wire["input"]) {
				action, reason = LossActionTranslate, "Responses sanitizes raw input items for the target request schema"
			}
			return LossReport{{Field: "responses.input", Action: action, Reason: reason}}
		}})
	}
	return rules
}

func targetSemanticField(typ OutboundType, feature SemanticFeature) string {
	switch feature {
	case FeatureTools:
		return "tools"
	case FeatureToolChoice:
		if typ == OutboundTypeGemini {
			return "toolConfig.functionCallingConfig"
		}
		return "tool_choice"
	case FeatureReasoning:
		switch typ {
		case OutboundTypeGemini:
			return "generationConfig.thinkingConfig"
		case OutboundTypeAnthropic:
			return "thinking"
		case OutboundTypeOpenAIResponse:
			return "reasoning"
		}
		return "reasoning_effort"
	case FeatureStructuredOutput:
		switch typ {
		case OutboundTypeGemini:
			return "generationConfig.responseSchema"
		case OutboundTypeOpenAIResponse:
			return "text.format"
		}
		return "response_format"
	default:
		switch typ {
		case OutboundTypeGemini:
			return "contents"
		case OutboundTypeOpenAIResponse:
			return "input"
		}
		return "messages"
	}
}

func fieldAt(object map[string]any, path string) any {
	var value any = object
	for _, part := range strings.Split(path, ".") {
		nested, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = nested[part]
	}
	return value
}

func (rule FieldConversionRule) report(input conversionInput) LossReport {
	if rule.applies != nil && !rule.applies(input.request) {
		return nil
	}
	var changes LossReport
	if rule.evaluate != nil {
		changes = rule.evaluate(input)
	} else {
		source, target := fieldAt(input.source, rule.SourceSemantic), fieldAt(input.wire, rule.TargetField)
		if source == nil {
			return nil
		}
		if rule.SourceSemantic == "stop" && rule.TargetField == "stop_sequences" {
			if text, ok := source.(string); ok {
				source = []any{text}
			}
		}
		if rule.SourceSemantic == "user" {
			if text, ok := source.(string); ok {
				source = strings.TrimSpace(text)
			}
		}
		if rule.Condition == "source present and target absent" && target != nil {
			return nil
		}
		if rule.Condition == "source differs from target" && reflect.DeepEqual(source, target) {
			return nil
		}
		changes = LossReport{{Field: rule.SourceSemantic, Action: rule.Action, Reason: rule.Reason}}
	}
	for i := range changes {
		if changes[i].TargetField == "" {
			changes[i].TargetField = rule.TargetField
		}
		if changes[i].Condition == "" {
			changes[i].Condition = rule.Condition
		}
	}
	return changes
}

func describeWireConversion(req *model.InternalLLMRequest, adapter model.Outbound, descriptor ProtocolDescriptor, body []byte) (LossReport, error) {
	var wire, source map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("decode conversion wire body: %w", err)
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(encoded, &source); err != nil {
		return nil, err
	}
	input := conversionInput{request: req, adapter: adapter, targetFormat: descriptor.APIFormat, source: source, wire: wire}
	report := LossReport(req.RequestRecoveryChanges(descriptor.APIFormat))
	for _, rule := range descriptor.FieldRules {
		report = append(report, rule.report(input)...)
	}
	report = append(report, reportChatEmptyFieldDrops(input, report)...)
	return uniqueLossReports(report), nil
}
