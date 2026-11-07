package gemini

import (
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func geminiThoughtSignatureProviderExtension(signature string) *model.ProviderExtensions {
	if signature == "" {
		return nil
	}
	return &model.ProviderExtensions{Gemini: &model.GeminiExtension{ThoughtSignature: signature}}
}

func geminiOpaqueSignature(signature, toolCallID, toolCallName string) *model.OpaqueSignature {
	if strings.TrimSpace(signature) == "" {
		return nil
	}
	result := &model.OpaqueSignature{
		Provider: model.SignatureProviderGemini,
		Kind:     model.OpaqueSignatureKindGeminiThought,
		Value:    signature,
	}
	if toolCallID != "" || toolCallName != "" {
		result.ToolCallScope = &model.SignatureToolCallScope{ID: toolCallID, Name: toolCallName}
	}
	return result
}

func geminiReasoningBlock(kind model.ReasoningBlockKind, index int, text, signature, toolCallID, toolCallName string) model.ReasoningBlock {
	block := model.ReasoningBlock{
		Kind:     kind,
		Index:    index,
		Text:     text,
		Provider: string(model.SignatureProviderGemini),
	}
	if source := geminiOpaqueSignature(signature, toolCallID, toolCallName); source != nil {
		block.SetOpaqueSignature(*source)
	}
	return block
}

// collectGeminiSignaturesByToolCallID indexes Signature-kind blocks by the tool
// call ID they originated from. This is the strongest anchor for replaying a
// Gemini thoughtSignature onto the matching functionCall in multi-tool turns.
func collectGeminiSignaturesByToolCallID(blocks []model.ReasoningBlock) map[string]string {
	out := make(map[string]string, len(blocks))
	for _, b := range blocks {
		if b.Kind != model.ReasoningBlockKindSignature {
			continue
		}
		signature, ok := geminiReasoningSignature(b)
		if !ok || signature.ToolCallScope == nil {
			continue
		}
		id := strings.TrimSpace(signature.ToolCallScope.ID)
		if id == "" {
			continue
		}
		if _, exists := out[id]; exists {
			continue
		}
		out[id] = signature.Value
	}
	return out
}

// collectGeminiSignaturesByName indexes Signature-kind blocks by the tool
// call they originated from (ToolCallName). This lets the outbound replay
// attach each signature to its matching functionCall when an ID anchor is not
// available. See G-H7.
func collectGeminiSignaturesByName(blocks []model.ReasoningBlock) map[string]string {
	out := make(map[string]string, len(blocks))
	for _, b := range blocks {
		if b.Kind != model.ReasoningBlockKindSignature {
			continue
		}
		signature, ok := geminiReasoningSignature(b)
		if !ok || signature.ToolCallScope == nil || strings.TrimSpace(signature.ToolCallScope.ID) != "" {
			continue
		}
		name := strings.TrimSpace(signature.ToolCallScope.Name)
		if name == "" {
			continue
		}
		if _, exists := out[name]; exists {
			continue
		}
		out[name] = signature.Value
	}
	return out
}

func collectGeminiLooseSignatures(blocks []model.ReasoningBlock) []string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Kind != model.ReasoningBlockKindSignature {
			continue
		}
		signature, ok := geminiReasoningSignature(b)
		if !ok {
			continue
		}
		if signature.ToolCallScope != nil && (strings.TrimSpace(signature.ToolCallScope.ID) != "" || strings.TrimSpace(signature.ToolCallScope.Name) != "") {
			continue
		}
		out = append(out, signature.Value)
	}
	return out
}

func buildGeminiThoughtParts(blocks []model.ReasoningBlock) []*model.GeminiPart {
	parts := make([]*model.GeminiPart, 0, len(blocks))
	for _, b := range blocks {
		if b.Kind != model.ReasoningBlockKindThinking {
			continue
		}
		part := &model.GeminiPart{Thought: true}
		if b.Text != "" {
			part.Text = b.Text
		}
		if signature, ok := geminiReasoningSignature(b); ok {
			part.ThoughtSignature = signature.Value
		}
		parts = append(parts, part)
	}
	return parts
}

func geminiReasoningSignature(block model.ReasoningBlock) (model.OpaqueSignature, bool) {
	signature, ok := block.OpaqueSignature()
	if !ok {
		return model.OpaqueSignature{}, false
	}
	if block.SignatureSource == nil && strings.TrimSpace(block.Provider) == "" {
		signature.Provider = model.SignatureProviderGemini
		signature.Kind = model.OpaqueSignatureKindGeminiThought
	}
	if strings.TrimSpace(signature.Value) == "" ||
		signature.Provider != model.SignatureProviderGemini ||
		signature.Kind != model.OpaqueSignatureKindGeminiThought {
		return model.OpaqueSignature{}, false
	}
	return signature, true
}

// nextGeminiSignature pops the next signature string, advancing the caller-managed cursor.
// Returns false when no more signatures are available for the current assistant turn.
func nextGeminiSignature(sigs []string, cursor *int) (string, bool) {
	if cursor == nil || *cursor >= len(sigs) {
		return "", false
	}
	s := sigs[*cursor]
	*cursor++
	return s, true
}

// logGeminiSignatureAudit emits the audit counter for Gemini thoughtSignature
// extraction (signatures attached to text / function_call parts from the
// upstream response). direction is "extract" for now; inject is logged
// inline in convertLLMToGeminiRequest. Fixed event name
// `transformer.reasoning.signature.passthrough` allows downstream log
// pipelines to aggregate across providers.
func (o *MessagesOutbound) logGeminiSignatureAudit(direction string, blocks []model.ReasoningBlock) {
	var thinking, sigCount int
	for _, rb := range blocks {
		switch rb.Kind {
		case model.ReasoningBlockKindThinking:
			thinking++
			if rb.Signature != "" {
				sigCount++
			}
		case model.ReasoningBlockKindSignature:
			if rb.Signature != "" {
				sigCount++
			}
		}
	}
	if thinking == 0 && sigCount == 0 {
		return
	}
	o.config.Log().Debugw("transformer.reasoning.signature.passthrough",
		"provider", "gemini",
		"direction", direction,
		"thinking_count", thinking,
		"signature_count", sigCount,
	)
}
