package anthropic

import (
	"context"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/compat"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func (i *MessagesInbound) geminiSignatureScope(ctx context.Context, fallbackModel string) compat.GeminiSignatureScope {
	scope := compat.GeminiSignatureScopeFromContext(ctx)
	if strings.TrimSpace(scope.Model) == "" {
		scope.Model = strings.TrimSpace(i.requestModel)
	}
	if strings.TrimSpace(scope.Model) == "" {
		scope.Model = strings.TrimSpace(fallbackModel)
	}
	if strings.TrimSpace(scope.Format) == "" {
		scope.Format = string(model.APIFormatAnthropicMessage)
	}
	return scope
}

func (i *MessagesInbound) saveGeminiThoughtSignature(scope compat.GeminiSignatureScope, toolCallID, toolName, signature string) {
	if i.config.SignatureStore != nil {
		i.config.SignatureStore.Save(scope, toolCallID, toolName, signature)
	}
}

func (i *MessagesInbound) restoreGeminiThoughtSignature(scope compat.GeminiSignatureScope, toolCallID, toolName string) string {
	if i.config.SignatureStore == nil {
		return ""
	}
	return i.config.SignatureStore.Restore(scope, toolCallID, toolName)
}

func signatureValueForProvider(signature *model.OpaqueSignature, provider model.SignatureProvider, kind model.OpaqueSignatureKind) string {
	if signature == nil || signature.Kind != kind || !signature.ValidFor(provider) {
		return ""
	}
	return signature.Value
}

func reasoningBlockSignatureForProvider(block model.ReasoningBlock, provider model.SignatureProvider, kind model.OpaqueSignatureKind) string {
	if block.SignatureSource == nil && strings.TrimSpace(block.Provider) == "" {
		return block.Signature
	}
	signature, ok := block.OpaqueSignature()
	if !ok {
		return ""
	}
	return signatureValueForProvider(&signature, provider, kind)
}

func messageReasoningSignatureForProvider(message *model.Message, provider model.SignatureProvider, kind model.OpaqueSignatureKind) string {
	if message == nil {
		return ""
	}
	if message.ReasoningSignatureSource == nil {
		if message.ReasoningSignature == nil {
			return ""
		}
		return *message.ReasoningSignature
	}
	return signatureValueForProvider(message.ReasoningSignatureSource, provider, kind)
}

func geminiToolCallShimSignature(block model.ReasoningBlock) string {
	signature, ok := block.OpaqueSignature()
	if !ok || signature.ToolCallScope == nil ||
		(strings.TrimSpace(signature.ToolCallScope.ID) == "" && strings.TrimSpace(signature.ToolCallScope.Name) == "") {
		return ""
	}
	return signatureValueForProvider(&signature, model.SignatureProviderGemini, model.OpaqueSignatureKindGeminiThought)
}

func anthropicWireStreamSignature(delta *model.StreamDelta) string {
	if delta == nil {
		return ""
	}
	if delta.SignatureSource == nil {
		return delta.Signature
	}
	if signature := signatureValueForProvider(delta.SignatureSource, model.SignatureProviderAnthropic, model.OpaqueSignatureKindAnthropicThinking); signature != "" {
		return signature
	}
	if delta.SignatureSource.ToolCallScope == nil ||
		(strings.TrimSpace(delta.SignatureSource.ToolCallScope.ID) == "" && strings.TrimSpace(delta.SignatureSource.ToolCallScope.Name) == "") {
		return ""
	}
	return signatureValueForProvider(delta.SignatureSource, model.SignatureProviderGemini, model.OpaqueSignatureKindGeminiThought)
}

func geminiThoughtSignatureShim(block MessageContentBlock) string {
	if block.Type != "thinking" || block.Thinking == nil || *block.Thinking != "" || block.Signature == nil {
		return ""
	}
	if strings.TrimSpace(*block.Signature) == "" {
		return ""
	}
	return *block.Signature
}

func countGeminiSignatureShims(blocks []MessageContentBlock) int {
	count := 0
	for _, block := range blocks {
		if geminiThoughtSignatureShim(block) != "" {
			count++
		}
	}
	return count
}
