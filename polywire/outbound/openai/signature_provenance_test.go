package openai

import (
	"context"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestResponsesRequestOnlyEmitsOpenAISignature(t *testing.T) {
	reasoning := "summary"
	message := model.Message{Role: "assistant", ReasoningContent: &reasoning}
	message.SetOpaqueReasoningSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderAnthropic,
		Kind:     model.OpaqueSignatureKindAnthropicThinking,
		Value:    "anthropic-signature",
	})
	items := convertAssistantMessageToResponses(message)
	if len(items) == 0 || items[0].EncryptedContent != nil {
		t.Fatalf("Anthropic signature leaked into Responses item: %+v", items)
	}

	message.SetOpaqueReasoningSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderOpenAI,
		Kind:     model.OpaqueSignatureKindOpenAIReasoning,
		Value:    "openai-signature",
	})
	items = convertAssistantMessageToResponses(message)
	if len(items) == 0 || items[0].EncryptedContent == nil || *items[0].EncryptedContent != "openai-signature" {
		t.Fatalf("OpenAI signature not preserved: %+v", items)
	}

	legacySignature := "legacy-signature"
	message = model.Message{
		Role:               "assistant",
		ReasoningContent:   &reasoning,
		ReasoningSignature: &legacySignature,
	}
	items = convertAssistantMessageToResponses(message)
	if len(items) == 0 || items[0].EncryptedContent == nil || *items[0].EncryptedContent != legacySignature {
		t.Fatalf("legacy signature not preserved: %+v", items)
	}
}

func TestResponsesRequestKeepsMultipleOpenAISignaturesOpaque(t *testing.T) {
	first := model.ReasoningBlock{Kind: model.ReasoningBlockKindSignature, Index: 0}
	first.SetOpaqueSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderOpenAI,
		Kind:     model.OpaqueSignatureKindOpenAIReasoning,
		Value:    "signature-one",
	})
	second := model.ReasoningBlock{Kind: model.ReasoningBlockKindSignature, Index: 1}
	second.SetOpaqueSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderOpenAI,
		Kind:     model.OpaqueSignatureKindOpenAIReasoning,
		Value:    "signature-two",
	})

	items := convertAssistantMessageToResponses(model.Message{
		Role:            "assistant",
		ReasoningBlocks: []model.ReasoningBlock{first, second},
	})
	if len(items) != 2 {
		t.Fatalf("reasoning items = %d, want 2: %+v", len(items), items)
	}
	if items[0].EncryptedContent == nil || *items[0].EncryptedContent != "signature-one" ||
		items[1].EncryptedContent == nil || *items[1].EncryptedContent != "signature-two" {
		t.Fatalf("opaque signatures were altered: %+v", items)
	}
}

func TestResponsesRequestKeepsReasoningBlockAssociationsAndDuplicates(t *testing.T) {
	first := model.ReasoningBlock{Kind: model.ReasoningBlockKindThinking, Index: 0, Text: "summary-one"}
	first.SetOpaqueSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderOpenAI,
		Kind:     model.OpaqueSignatureKindOpenAIReasoning,
		Value:    "repeated-signature",
	})
	second := model.ReasoningBlock{Kind: model.ReasoningBlockKindThinking, Index: 1, Text: "summary-two"}
	second.SetOpaqueSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderOpenAI,
		Kind:     model.OpaqueSignatureKindOpenAIReasoning,
		Value:    "repeated-signature",
	})

	items := openAIReasoningItems(model.Message{ReasoningBlocks: []model.ReasoningBlock{first, second}})
	if len(items) != 2 {
		t.Fatalf("reasoning items = %d, want 2: %+v", len(items), items)
	}
	for index, summary := range []string{"summary-one", "summary-two"} {
		if len(items[index].Summary) != 1 || items[index].Summary[0].Text != summary {
			t.Fatalf("item %d summary association lost: %+v", index, items[index])
		}
		if items[index].EncryptedContent == nil || *items[index].EncryptedContent != "repeated-signature" {
			t.Fatalf("item %d duplicate opaque signature lost: %+v", index, items[index])
		}
	}
}

func TestResponsesRequestPreservesLegacyOpaqueBytesAndRejectsWrongKind(t *testing.T) {
	legacy := " \topaque-signature\n "
	items := openAIReasoningItems(model.Message{ReasoningSignature: &legacy})
	if len(items) != 1 || items[0].EncryptedContent == nil || *items[0].EncryptedContent != legacy {
		t.Fatalf("legacy opaque bytes changed: %+v", items)
	}

	block := model.ReasoningBlock{Kind: model.ReasoningBlockKindSignature}
	block.SetOpaqueSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderOpenAI,
		Kind:     model.OpaqueSignatureKindGeminiThought,
		Value:    "wrong-kind",
	})
	if items := openAIReasoningItems(model.Message{ReasoningBlocks: []model.ReasoningBlock{block}}); len(items) != 0 {
		t.Fatalf("wrong-kind signature was replayed: %+v", items)
	}
}

func TestResponsesStreamTagsEncryptedContentAsOpenAI(t *testing.T) {
	outbound := &ResponseOutbound{}
	events, err := outbound.TransformStreamEvent(context.Background(), []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","encrypted_content":"openai-signature"}}`))
	if err != nil {
		t.Fatalf("TransformStreamEvent() error = %v", err)
	}
	events = eventsOfKind(events, model.StreamEventKindSignatureDelta)
	if len(events) != 1 || events[0].Delta == nil || events[0].Delta.SignatureSource == nil {
		t.Fatalf("missing signature provenance event: %+v", events)
	}
	if !events[0].Delta.SignatureSource.ValidFor(model.SignatureProviderOpenAI) || events[0].Delta.Signature != "openai-signature" {
		t.Fatalf("unexpected signature provenance: %+v", events[0].Delta)
	}
}
