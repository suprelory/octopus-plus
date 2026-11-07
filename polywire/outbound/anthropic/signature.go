package anthropic

import (
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
	anthropicModel "github.com/suprelory/octopus-plus/polywire/protocol/anthropic"
)

func hasThinkingContent(msg model.Message) bool {
	if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
		return true
	}
	for _, rb := range msg.ReasoningBlocks {
		if rb.Kind == model.ReasoningBlockKindThinking && (rb.Text != "" || anthropicReasoningSignature(rb) != "") {
			return true
		}
	}
	return false
}

// emitThinkingBlocks reproduces Anthropic thinking / redacted_thinking blocks in their original
// order so multi-turn extended-thinking requests pass signature verification. It prefers the
// per-block ReasoningBlocks representation; when absent (e.g. the upstream was OpenRouter or
// the turn predates this refactor), it falls back to the flat ReasoningContent/Signature pair.
func (o *MessageOutbound) emitThinkingBlocks(msg model.Message) []anthropicModel.MessageContentBlock {
	anthropicBlocks := make([]model.ReasoningBlock, 0, len(msg.ReasoningBlocks))
	for _, block := range msg.ReasoningBlocks {
		if block.SignatureSource == nil {
			provider := strings.TrimSpace(block.Provider)
			if provider == "" || provider == string(model.SignatureProviderAnthropic) {
				anthropicBlocks = append(anthropicBlocks, block)
			}
			continue
		}
		if block.SignatureSource.ValidForKind(model.SignatureProviderAnthropic, model.OpaqueSignatureKindAnthropicThinking) {
			anthropicBlocks = append(anthropicBlocks, block)
		}
	}

	if len(anthropicBlocks) == 0 {
		return emitThinkingBlocksLegacy(msg)
	}

	out := make([]anthropicModel.MessageContentBlock, 0, len(anthropicBlocks))
	// signature-only blocks attach to the most recent thinking block.
	var lastThinking *anthropicModel.MessageContentBlock
	for _, rb := range anthropicBlocks {
		switch rb.Kind {
		case model.ReasoningBlockKindThinking:
			block := anthropicModel.MessageContentBlock{Type: "thinking"}
			if rb.Text != "" {
				t := rb.Text
				block.Thinking = &t
			}
			if signature := anthropicReasoningSignature(rb); signature != "" {
				s := signature
				block.Signature = &s
			}
			out = append(out, block)
			lastThinking = &out[len(out)-1]
		case model.ReasoningBlockKindRedacted:
			if rb.Data != "" {
				out = append(out, anthropicModel.MessageContentBlock{
					Type: "redacted_thinking",
					Data: rb.Data,
				})
				lastThinking = nil
			}
		case model.ReasoningBlockKindSignature:
			if signature := anthropicReasoningSignature(rb); signature != "" && lastThinking != nil && lastThinking.Signature == nil {
				s := signature
				lastThinking.Signature = &s
			}
		}
	}

	o.logAnthropicSignatureAudit("inject", anthropicBlocks)

	return out
}

// logAnthropicSignatureAudit emits the audit counter for Anthropic
// reasoning signature passthrough. direction is one of inject / extract;
// the event name `transformer.reasoning.signature.passthrough` is fixed so
// downstream log pipelines can aggregate by (provider, direction). Called
// at Debug level so it only fires when diagnostic logging is enabled.
func (o *MessageOutbound) logAnthropicSignatureAudit(direction string, blocks []model.ReasoningBlock) {
	var thinking, redacted, sigCount int
	for _, rb := range blocks {
		switch rb.Kind {
		case model.ReasoningBlockKindThinking:
			thinking++
			if anthropicReasoningSignature(rb) != "" {
				sigCount++
			}
		case model.ReasoningBlockKindRedacted:
			redacted++
			sigCount++
		case model.ReasoningBlockKindSignature:
			if anthropicReasoningSignature(rb) != "" {
				sigCount++
			}
		}
	}
	if thinking == 0 && redacted == 0 && sigCount == 0 {
		return
	}
	o.config.Log().Debugw("transformer.reasoning.signature.passthrough",
		"provider", "anthropic",
		"direction", direction,
		"thinking_count", thinking,
		"redacted_count", redacted,
		"signature_count", sigCount,
	)
}

// truncateForAudit keeps audit log fields bounded to avoid logging entire
// multi-KB provider error payloads. Byte-level truncation is fine for
// audit purposes.
func truncateForAudit(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func emitThinkingBlocksLegacy(msg model.Message) []anthropicModel.MessageContentBlock {
	var out []anthropicModel.MessageContentBlock
	if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
		var signature *string
		if msg.ReasoningSignatureSource == nil && msg.ReasoningSignature != nil && *msg.ReasoningSignature != "" {
			value := *msg.ReasoningSignature
			signature = &value
		} else if msg.ReasoningSignatureSource != nil && msg.ReasoningSignatureSource.ValidForKind(model.SignatureProviderAnthropic, model.OpaqueSignatureKindAnthropicThinking) {
			value := msg.ReasoningSignatureSource.Value
			signature = &value
		}
		out = append(out, anthropicModel.MessageContentBlock{
			Type:      "thinking",
			Thinking:  msg.ReasoningContent,
			Signature: signature,
		})
	}
	for _, data := range msg.RedactedThinkingBlocks {
		out = append(out, anthropicModel.MessageContentBlock{
			Type: "redacted_thinking",
			Data: data,
		})
	}
	return out
}

func anthropicReasoningSignature(block model.ReasoningBlock) string {
	if block.SignatureSource == nil && strings.TrimSpace(block.Provider) == "" {
		if strings.TrimSpace(block.Signature) == "" {
			return ""
		}
		return block.Signature
	}
	if signature, ok := block.OpaqueSignature(); ok && signature.ValidForKind(model.SignatureProviderAnthropic, model.OpaqueSignatureKindAnthropicThinking) {
		return signature.Value
	}
	return ""
}
