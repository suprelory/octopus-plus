package openai

import (
	"strings"

	"github.com/samber/lo"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func openAIStreamSignature(delta *model.StreamDelta) string {
	if delta == nil {
		return ""
	}
	if delta.SignatureSource == nil {
		return delta.Signature
	}
	if delta.SignatureSource.ValidForKind(model.SignatureProviderOpenAI, model.OpaqueSignatureKindOpenAIReasoning) {
		return delta.SignatureSource.Value
	}
	return ""
}

func openAIMessageReasoningItems(message *model.Message) []ResponsesItem {
	if message == nil {
		return nil
	}
	items := make([]ResponsesItem, 0, len(message.ReasoningBlocks)+1)
	hasBlockSummary := false
	for _, block := range message.ReasoningBlocks {
		text := ""
		if block.Kind == model.ReasoningBlockKindThinking {
			text = block.Text
			hasBlockSummary = hasBlockSummary || text != ""
		}
		signature, hasSignature := openAIMessageBlockSignature(block)
		if text == "" && !hasSignature {
			continue
		}
		if block.Kind == model.ReasoningBlockKindSignature && hasSignature && len(items) > 0 && items[len(items)-1].EncryptedContent == nil {
			value := signature.Value
			items[len(items)-1].EncryptedContent = &value
			continue
		}
		item := ResponsesItem{ID: generateItemID(), Type: "reasoning", Status: lo.ToPtr("completed")}
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
			items = append(items, ResponsesItem{ID: generateItemID(), Type: "reasoning", Status: lo.ToPtr("completed")})
		}
		items[0].Summary = []ResponsesReasoningSummary{{Type: "summary_text", Text: *message.ReasoningContent}}
	}
	if signature, ok := openAIMessageFlatSignature(message); ok && !openAIMessageBlocksContainSignature(message.ReasoningBlocks, signature) {
		for index := range items {
			if items[index].EncryptedContent == nil {
				value := signature.Value
				items[index].EncryptedContent = &value
				return items
			}
		}
		value := signature.Value
		items = append(items, ResponsesItem{ID: generateItemID(), Type: "reasoning", Status: lo.ToPtr("completed"), EncryptedContent: &value})
	}
	return items
}

func openAIMessageBlockSignature(block model.ReasoningBlock) (model.OpaqueSignature, bool) {
	if block.SignatureSource == nil && strings.TrimSpace(block.Provider) == "" {
		if strings.TrimSpace(block.Signature) == "" {
			return model.OpaqueSignature{}, false
		}
		return model.OpaqueSignature{Provider: model.SignatureProviderOpenAI, Kind: model.OpaqueSignatureKindOpenAIReasoning, Value: block.Signature}, true
	}
	signature, ok := block.OpaqueSignature()
	return signature, ok && signature.ValidForKind(model.SignatureProviderOpenAI, model.OpaqueSignatureKindOpenAIReasoning)
}

func openAIMessageFlatSignature(message *model.Message) (model.OpaqueSignature, bool) {
	if message == nil {
		return model.OpaqueSignature{}, false
	}
	if message.ReasoningSignatureSource != nil {
		signature := *message.ReasoningSignatureSource
		return signature, signature.ValidForKind(model.SignatureProviderOpenAI, model.OpaqueSignatureKindOpenAIReasoning)
	}
	if message.ReasoningSignature == nil || strings.TrimSpace(*message.ReasoningSignature) == "" {
		return model.OpaqueSignature{}, false
	}
	return model.OpaqueSignature{Provider: model.SignatureProviderOpenAI, Kind: model.OpaqueSignatureKindOpenAIReasoning, Value: *message.ReasoningSignature}, true
}

func openAIMessageBlocksContainSignature(blocks []model.ReasoningBlock, target model.OpaqueSignature) bool {
	for _, block := range blocks {
		signature, ok := openAIMessageBlockSignature(block)
		if ok && sameOpenAIMessageSignature(signature, target) {
			return true
		}
	}
	return false
}

func sameOpenAIMessageSignature(first, second model.OpaqueSignature) bool {
	if first.Provider != second.Provider || first.Kind != second.Kind || first.Value != second.Value {
		return false
	}
	if first.ToolCallScope == nil || second.ToolCallScope == nil {
		return first.ToolCallScope == nil && second.ToolCallScope == nil
	}
	return first.ToolCallScope.ID == second.ToolCallScope.ID && first.ToolCallScope.Name == second.ToolCallScope.Name
}
