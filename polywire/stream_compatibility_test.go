package polywire_test

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"regexp"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestLegacyInboundStreamUsesCanonicalWireAndAggregate(t *testing.T) {
	text, finish := "hello", "stop"
	chunks := []*model.InternalLLMResponse{
		{ID: "response", Model: "model", Created: 1234, Object: "chat.completion.chunk", SystemFingerprint: "fp", ServiceTier: "default", Choices: []model.Choice{{Delta: &model.Message{Role: "assistant", Content: model.MessageContent{Content: &text}}}}},
		{ID: "response", Model: "model", Object: "chat.completion.chunk", Choices: []model.Choice{{FinishReason: &finish}}, Usage: &model.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}},
		{Object: "[DONE]"},
	}
	for _, typ := range []inbound.InboundType{inbound.InboundTypeOpenAIChat, inbound.InboundTypeOpenAIResponse, inbound.InboundTypeAnthropic} {
		legacy, canonical := inbound.Get(typ), inbound.Get(typ)
		var oldWire, newWire bytes.Buffer
		for _, chunk := range chunks {
			old, err := legacy.TransformStream(context.Background(), chunk)
			if err != nil {
				t.Fatal(err)
			}
			current, err := canonical.TransformStreamEvents(context.Background(), model.StreamEventsFromInternalResponse(chunk))
			if err != nil {
				t.Fatal(err)
			}
			oldWire.Write(old)
			newWire.Write(current)
		}
		if normalizeGeneratedItemIDs(oldWire.String()) != normalizeGeneratedItemIDs(newWire.String()) {
			t.Fatalf("%v wire mismatch:\n%s\n%s", typ, &oldWire, &newWire)
		}
		old, _ := legacy.GetInternalResponse(context.Background())
		current, _ := canonical.GetInternalResponse(context.Background())
		if !reflect.DeepEqual(old, current) || old == nil || old.Created != 1234 || old.Usage.TotalTokens != 5 {
			t.Fatalf("%v aggregate mismatch: %+v / %+v", typ, old, current)
		}
	}
}

// Keep ID references and ordering in the comparison while allowing each
// independent encoder to allocate its own wire item identifiers.
func normalizeGeneratedItemIDs(wire string) string {
	ids := make(map[string]string)
	return regexp.MustCompile(`item_[A-Za-z0-9]+`).ReplaceAllStringFunc(wire, func(id string) string {
		if ids[id] == "" {
			ids[id] = fmt.Sprintf("item_%d", len(ids))
		}
		return ids[id]
	})
}

func TestLegacyOutboundStreamMatchesCanonicalProjection(t *testing.T) {
	fixtures := map[outbound.OutboundType][]string{
		outbound.OutboundTypeOpenAIChat:     {`{"id":"r","model":"m","created":123,"choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`, `[DONE]`},
		outbound.OutboundTypeOpenAIResponse: {`{"type":"response.created","response":{"id":"r","model":"m","status":"in_progress"}}`, `{"type":"response.output_text.delta","delta":"hello"}`, `{"type":"response.completed","response":{"status":"completed","usage":{"total_tokens":3}}}`},
		outbound.OutboundTypeAnthropic:      {`{"type":"message_start","message":{"id":"r","model":"m","role":"assistant"}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`, `{"type":"message_stop"}`},
		outbound.OutboundTypeGemini:         {`{"responseId":"r","modelVersion":"m","candidates":[{"index":0,"content":{"parts":[{"text":"hello"},{"inlineData":{"mimeType":"image/png","data":"AA=="}}]},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":3}}`, `[DONE]`},
	}
	for typ, chunks := range fixtures {
		legacy, canonical := outbound.Get(typ), outbound.Get(typ)
		for _, data := range chunks {
			old, err := legacy.TransformStream(context.Background(), []byte(data))
			if err != nil {
				t.Fatal(err)
			}
			events, err := canonical.TransformSourceEvent(context.Background(), model.SourceEvent{Data: []byte(data)})
			if err != nil {
				t.Fatal(err)
			}
			current := model.InternalResponseFromStreamEvents(events)
			if !reflect.DeepEqual(old, current) {
				t.Fatalf("%s lost legacy fields: %+v / %+v", typ, old, current)
			}
		}
	}
}
