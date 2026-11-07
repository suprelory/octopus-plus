package relay

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/internal/transformer"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	transformerModel "github.com/suprelory/octopus-plus/polywire/model"
)

func TestNewAttemptRelayRequestIsolatesMutableState(t *testing.T) {
	rawBody := []byte(`{"model":"client-model","messages":[{"role":"user","content":"hello"}]}`)
	base := &relayRequest{
		ctx:         context.Background(),
		inAdapter:   transformer.Inbound(inbound.InboundTypeOpenAIChat),
		inboundType: inbound.InboundTypeOpenAIChat,
		internalRequest: &transformerModel.InternalLLMRequest{
			Model:               "client-model",
			Query:               url.Values{"trace": {"base"}},
			TransformerMetadata: map[string]string{"source": "client"},
			Messages: []transformerModel.Message{{
				Role:    "user",
				Content: transformerModel.MessageContent{Content: stringPtr("hello")},
			}},
		},
		rawBody: rawBody,
	}

	first, err := newAttemptRelayRequest(base, context.Background(), "upstream-one")
	if err != nil {
		t.Fatalf("newAttemptRelayRequest(first) error = %v", err)
	}
	second, err := newAttemptRelayRequest(base, context.Background(), "upstream-two")
	if err != nil {
		t.Fatalf("newAttemptRelayRequest(second) error = %v", err)
	}

	if base.internalRequest.Model != "client-model" {
		t.Fatalf("base model = %q, want client-model", base.internalRequest.Model)
	}
	if first.internalRequest == base.internalRequest || second.internalRequest == base.internalRequest || first.internalRequest == second.internalRequest {
		t.Fatal("attempt requests share canonical request pointers")
	}
	if first.inAdapter == base.inAdapter || second.inAdapter == base.inAdapter || first.inAdapter == second.inAdapter {
		t.Fatal("attempts share inbound adapters")
	}
	if first.internalRequest.Model != "upstream-one" || second.internalRequest.Model != "upstream-two" {
		t.Fatalf("attempt models = %q, %q", first.internalRequest.Model, second.internalRequest.Model)
	}
	if first.streamPayloadWritten.Load() || second.streamPayloadWritten.Load() || first.responseCollected.Load() || second.responseCollected.Load() {
		t.Fatal("attempt atomic state must start clear")
	}

	first.internalRequest.Query.Set("trace", "first")
	first.internalRequest.TransformerMetadata["source"] = "first"
	*first.internalRequest.Messages[0].Content.Content = "changed"
	first.streamPayloadWritten.Store(true)
	first.responseCollected.Store(true)

	if got := base.internalRequest.Query.Get("trace"); got != "base" {
		t.Fatalf("base query mutated to %q", got)
	}
	if got := second.internalRequest.Query.Get("trace"); got != "base" {
		t.Fatalf("second attempt query mutated to %q", got)
	}
	if got := base.internalRequest.TransformerMetadata["source"]; got != "client" {
		t.Fatalf("base metadata mutated to %q", got)
	}
	if got := *second.internalRequest.Messages[0].Content.Content; got != "hello" {
		t.Fatalf("second attempt message mutated to %q", got)
	}
	if second.streamPayloadWritten.Load() || second.responseCollected.Load() || base.streamPayloadWritten.Load() || base.responseCollected.Load() {
		t.Fatal("attempt atomic state leaked to base or sibling attempt")
	}
}

// TestNewAttemptRelayRequestSeedsAnthropicAdapterState proves retry attempts
// get their request-derived inbound state from the canonical request (via
// RequestStateSeedable), not from re-parsing rawBody: with rawBody nil the
// old re-parse implementation could not initialize any state at all.
func TestNewAttemptRelayRequestSeedsAnthropicAdapterState(t *testing.T) {
	rawBody := []byte(`{
		"model":"client-model",
		"max_tokens":32,
		"system":"You are helpful.",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{"name":"lookup","description":"look things up","input_schema":{"type":"object"}}]
	}`)
	baseAdapter := transformer.Inbound(inbound.InboundTypeAnthropic)
	internalRequest, err := baseAdapter.TransformRequest(context.Background(), rawBody)
	if err != nil {
		t.Fatalf("base TransformRequest error = %v", err)
	}
	if internalRequest.EstimatedInputTokens <= 0 {
		t.Fatalf("expected positive EstimatedInputTokens, got %d", internalRequest.EstimatedInputTokens)
	}
	base := &relayRequest{
		ctx:             context.Background(),
		inAdapter:       baseAdapter,
		inboundType:     inbound.InboundTypeAnthropic,
		internalRequest: internalRequest,
		rawBody:         nil, // impossible to serve via re-parse; state must come from seeding
	}

	attempt, err := newAttemptRelayRequest(base, context.Background(), "mapped-upstream-model")
	if err != nil {
		t.Fatalf("newAttemptRelayRequest error = %v", err)
	}

	if attempt.inAdapter == base.inAdapter {
		t.Fatal("attempt shares inbound adapter with base")
	}
	if attempt.internalRequest.Model != "mapped-upstream-model" {
		t.Fatalf("attempt model = %q, want mapped-upstream-model", attempt.internalRequest.Model)
	}
	if attempt.internalRequest.EstimatedInputTokens != internalRequest.EstimatedInputTokens {
		t.Fatalf("attempt EstimatedInputTokens = %d, want %d (clone propagation)",
			attempt.internalRequest.EstimatedInputTokens, internalRequest.EstimatedInputTokens)
	}

	// The seeded adapter must synthesize the same message_start input tokens
	// the base adapter would produce for the identical stream.
	events := []transformerModel.StreamEvent{
		{Kind: transformerModel.StreamEventKindMessageStart, ID: "msg_1", Model: "client-model", Role: "assistant"},
		{Kind: transformerModel.StreamEventKindMessageStop, ID: "msg_1", Model: "client-model", StopReason: transformerModel.FinishReasonStop},
	}
	want := strconv.Itoa(int(internalRequest.EstimatedInputTokens))
	out, err := attempt.inAdapter.TransformStreamEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("TransformStreamEvents error = %v", err)
	}
	if !strings.Contains(string(out), `"input_tokens":`+want) {
		t.Fatalf("expected input_tokens:%s in message_start SSE, got %s", want, out)
	}
}
