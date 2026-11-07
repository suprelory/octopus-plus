package anthropic

import (
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestEmitThinkingBlocksDropsForeignSignature(t *testing.T) {
	reasoning := "summary"
	message := model.Message{ReasoningContent: &reasoning}
	message.SetOpaqueReasoningSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderOpenAI,
		Kind:     model.OpaqueSignatureKindOpenAIReasoning,
		Value:    "openai-signature",
	})

	blocks := (&MessageOutbound{}).emitThinkingBlocks(message)
	if len(blocks) != 1 || blocks[0].Signature != nil {
		t.Fatalf("foreign signature leaked into Anthropic block: %+v", blocks)
	}
}

func TestEmitThinkingBlocksPreservesLegacySignature(t *testing.T) {
	reasoning := "summary"
	signature := "legacy-signature"
	message := model.Message{
		ReasoningContent:   &reasoning,
		ReasoningSignature: &signature,
	}

	blocks := (&MessageOutbound{}).emitThinkingBlocks(message)
	if len(blocks) != 1 || blocks[0].Signature == nil || *blocks[0].Signature != signature {
		t.Fatalf("legacy signature was not preserved: %+v", blocks)
	}
}

func TestEmitThinkingBlocksPreservesLegacyBlockSignature(t *testing.T) {
	message := model.Message{ReasoningBlocks: []model.ReasoningBlock{{
		Kind:      model.ReasoningBlockKindThinking,
		Text:      "summary",
		Signature: "legacy-block-signature",
	}}}

	blocks := (&MessageOutbound{}).emitThinkingBlocks(message)
	if len(blocks) != 1 || blocks[0].Signature == nil || *blocks[0].Signature != "legacy-block-signature" {
		t.Fatalf("legacy block signature was not preserved: %+v", blocks)
	}
}
