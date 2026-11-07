package openai

import (
	"context"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestChatInboundTransformStreamEventsPreservesBatchBeforeDone(t *testing.T) {
	inbound := &ChatInbound{}
	usage := &model.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}
	encoded, err := inbound.TransformStreamEvents(context.Background(), []model.StreamEvent{
		{Kind: model.StreamEventKindMessageStart, ID: "chat_1", Model: "model_1", Role: "assistant"},
		{Kind: model.StreamEventKindTextDelta, ID: "chat_1", Model: "model_1", Delta: &model.StreamDelta{Text: "hello"}},
		{Kind: model.StreamEventKindMessageStop, ID: "chat_1", Model: "model_1", StopReason: model.FinishReasonStop},
		{Kind: model.StreamEventKindUsageDelta, ID: "chat_1", Model: "model_1", Usage: usage},
		{Kind: model.StreamEventKindDone},
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := string(encoded)
	for _, expected := range []string{`"content":"hello"`, `"finish_reason":"stop"`, `"prompt_tokens":3`, "data: [DONE]"} {
		if !strings.Contains(stream, expected) {
			t.Fatalf("stream is missing %q: %s", expected, stream)
		}
	}
	if !strings.HasSuffix(stream, "data: [DONE]\n\n") {
		t.Fatalf("done marker must be the stream tail: %q", stream)
	}
}
