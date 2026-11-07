package openai

import (
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func openAIReasoningItems(message model.Message) []ResponsesItem {
	items := make([]ResponsesItem, 0, len(message.ReasoningBlocks)+1)
	hasBlockSummary := false
	for _, block := range message.ReasoningBlocks {
		text := ""
		if block.Kind == model.ReasoningBlockKindThinking {
			text = block.Text
			hasBlockSummary = hasBlockSummary || text != ""
		}
		signature, hasSignature := openAIReasoningBlockSignature(block)
		if text == "" && !hasSignature {
			continue
		}
		if block.Kind == model.ReasoningBlockKindSignature && hasSignature && len(items) > 0 && items[len(items)-1].EncryptedContent == nil {
			value := signature.Value
			items[len(items)-1].EncryptedContent = &value
			continue
		}
		item := ResponsesItem{Type: "reasoning"}
		if text != "" {
			item.Summary = []ResponsesReasoningSummary{{Type: "summary_text", Text: text}}
		}
		if hasSignature {
			value := signature.Value
			item.EncryptedContent = &value
		}
		items = append(items, item)
	}
	if message.ReasoningContent != nil && *message.ReasoningContent != "" && !hasBlockSummary {
		if len(items) == 0 {
			items = append(items, ResponsesItem{Type: "reasoning"})
		}
		items[0].Summary = []ResponsesReasoningSummary{{Type: "summary_text", Text: *message.ReasoningContent}}
	}
	if signature, ok := openAIFlatReasoningSignature(message); ok && !openAIReasoningBlocksContainSignature(message.ReasoningBlocks, signature) {
		for index := range items {
			if items[index].EncryptedContent == nil {
				value := signature.Value
				items[index].EncryptedContent = &value
				return items
			}
		}
		value := signature.Value
		items = append(items, ResponsesItem{Type: "reasoning", EncryptedContent: &value})
	}
	return items
}

func openAIReasoningBlockSignature(block model.ReasoningBlock) (model.OpaqueSignature, bool) {
	if block.SignatureSource == nil && strings.TrimSpace(block.Provider) == "" {
		if strings.TrimSpace(block.Signature) == "" {
			return model.OpaqueSignature{}, false
		}
		return model.OpaqueSignature{Provider: model.SignatureProviderOpenAI, Kind: model.OpaqueSignatureKindOpenAIReasoning, Value: block.Signature}, true
	}
	signature, ok := block.OpaqueSignature()
	return signature, ok && signature.ValidForKind(model.SignatureProviderOpenAI, model.OpaqueSignatureKindOpenAIReasoning)
}

func openAIFlatReasoningSignature(message model.Message) (model.OpaqueSignature, bool) {
	if message.ReasoningSignatureSource != nil {
		signature := *message.ReasoningSignatureSource
		return signature, signature.ValidForKind(model.SignatureProviderOpenAI, model.OpaqueSignatureKindOpenAIReasoning)
	}
	if message.ReasoningSignature == nil || strings.TrimSpace(*message.ReasoningSignature) == "" {
		return model.OpaqueSignature{}, false
	}
	return model.OpaqueSignature{Provider: model.SignatureProviderOpenAI, Kind: model.OpaqueSignatureKindOpenAIReasoning, Value: *message.ReasoningSignature}, true
}

func openAIReasoningBlocksContainSignature(blocks []model.ReasoningBlock, target model.OpaqueSignature) bool {
	for _, block := range blocks {
		signature, ok := openAIReasoningBlockSignature(block)
		if ok && sameOpenAIOpaqueSignature(signature, target) {
			return true
		}
	}
	return false
}

func sameOpenAIOpaqueSignature(first, second model.OpaqueSignature) bool {
	if first.Provider != second.Provider || first.Kind != second.Kind || first.Value != second.Value {
		return false
	}
	if first.ToolCallScope == nil || second.ToolCallScope == nil {
		return first.ToolCallScope == nil && second.ToolCallScope == nil
	}
	return first.ToolCallScope.ID == second.ToolCallScope.ID && first.ToolCallScope.Name == second.ToolCallScope.Name
}
