package polywire_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/polywire"
	"github.com/suprelory/octopus-plus/polywire/compat"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/internal/testlog"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
	"github.com/suprelory/octopus-plus/polywire/streamio"
)

func TestEngineTokenEstimationSurvivesAttemptSeeding(t *testing.T) {
	ctx := context.Background()
	const body = `{"model":"client-model","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`
	calls := 0
	engine := polywire.New(polywire.Config{TokenCounter: func(content, modelName string) int {
		calls++
		if content != "hello" || modelName != "client-model" {
			t.Fatalf("unexpected estimator input: %q, %q", content, modelName)
		}
		return 7
	}})
	request, err := engine.Inbound(inbound.InboundTypeAnthropic).TransformRequest(ctx, []byte(body))
	if err != nil || request.EstimatedInputTokens != 7 || calls != 1 {
		t.Fatalf("estimate: request=%+v calls=%d err=%v", request, calls, err)
	}
	attempt := engine.Inbound(inbound.InboundTypeAnthropic)
	attempt.(model.RequestStateSeedable).SeedRequestState(request)
	text := "hello"
	wire, err := attempt.TransformStream(ctx, &model.InternalLLMResponse{
		ID: "msg_1", Model: "provider-model", Object: "chat.completion.chunk",
		Choices: []model.Choice{{Index: 0, Delta: &model.Message{Role: "assistant", Content: model.MessageContent{Content: &text}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	decoder := streamio.NewSSEDecoder(4096)
	if err := decoder.Feed(wire, func(event model.SourceEvent) error {
		if event.Type != "message_start" {
			return nil
		}
		var start struct {
			Message struct {
				Usage struct {
					InputTokens int64 `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(event.Data, &start); err != nil {
			return err
		}
		seen = true
		if start.Message.Usage.InputTokens != 7 {
			t.Fatalf("seeded stream estimate = %d", start.Message.Usage.InputTokens)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !seen || calls != 1 {
		t.Fatalf("seed must preserve usage without recounting: seen=%v calls=%d", seen, calls)
	}
	withoutCounter, err := polywire.New(polywire.Config{}).Inbound(inbound.InboundTypeAnthropic).TransformRequest(ctx, []byte(body))
	if err != nil || withoutCounter.EstimatedInputTokens != 0 {
		t.Fatalf("disabled estimator: %+v, %v", withoutCounter, err)
	}
}

func TestEngineSignatureStoreLifetime(t *testing.T) {
	ctx := compat.WithGeminiSignatureScope(context.Background(), compat.GeminiSignatureScope{APIKeyID: "key-a", SessionID: "turns"})
	const initial = `{"model":"client-model","max_tokens":16,"messages":[{"role":"user","content":"run a tool"}]}`
	const followup = `{"model":"client-model","max_tokens":16,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{}}]}]}`
	const signature = " \tsignature-bytes\n "
	save := func(engine *polywire.Engine) {
		t.Helper()
		adapter := engine.Inbound(inbound.InboundTypeAnthropic)
		if _, err := adapter.TransformRequest(ctx, []byte(initial)); err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.TransformResponse(ctx, &model.InternalLLMResponse{ID: "msg_1", Model: "gemini-3.1-pro", Choices: []model.Choice{{Message: &model.Message{
			Role: "assistant", ToolCalls: []model.ToolCall{{ID: "call_1", Function: model.FunctionCall{Name: "lookup", Arguments: "{}"}, ThoughtSignature: signature}},
		}}}}); err != nil {
			t.Fatal(err)
		}
	}
	restore := func(engine *polywire.Engine, requestContext context.Context) string {
		t.Helper()
		request, err := engine.Inbound(inbound.InboundTypeAnthropic).TransformRequest(requestContext, []byte(followup))
		if err != nil {
			t.Fatal(err)
		}
		return request.ConversationMessages()[0].ToolCalls[0].GetGeminiExtensions().ThoughtSignature
	}
	engine := polywire.New(polywire.Config{})
	save(engine)
	if got := restore(engine, ctx); got != signature {
		t.Fatalf("fresh adapter lost opaque signature: %q", got)
	}
	if got := restore(polywire.New(polywire.Config{}), ctx); got != "" {
		t.Fatalf("signature crossed engine instances: %q", got)
	}
	otherScope := compat.WithGeminiSignatureScope(ctx, compat.GeminiSignatureScope{APIKeyID: "key-b", SessionID: "turns"})
	if got := restore(engine, otherScope); got != "" {
		t.Fatalf("signature crossed scope: %q", got)
	}
	shared := compat.NewMemorySignatureStore()
	save(polywire.New(polywire.Config{SignatureStore: shared}))
	if got := restore(polywire.New(polywire.Config{SignatureStore: shared}), ctx); got != signature {
		t.Fatalf("injected store was not shared: %q", got)
	}
	disabled := polywire.New(polywire.Config{SignatureStore: compat.NoopSignatureStore{}})
	save(disabled)
	if got := restore(disabled, ctx); got != "" {
		t.Fatalf("disabled store retained signature: %q", got)
	}
}

func TestEnginePlanningUsesInjectedLoggerAndLeavesRequestIntact(t *testing.T) {
	logger := &testlog.Logger{}
	engine := polywire.New(polywire.Config{Logger: logger})
	request, err := engine.Inbound(inbound.InboundTypeOpenAIChat).TransformRequest(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stop":["1","2","3","4","5"]}`))
	if err != nil {
		t.Fatal(err)
	}
	decision := engine.PlanRequestForModel(request, "claude-model", outbound.OutboundTypeAnthropic, false)
	if decision.Status != outbound.CapabilityDegraded {
		t.Fatalf("expected stop truncation report: %+v", decision)
	}
	if len(logger.Entries()) == 0 {
		t.Fatal("planner did not use injected logger")
	}
	if request.Model != "m" || len(request.Stop.MultipleStop) != 5 {
		t.Fatalf("planner mutated source request: %+v", request)
	}
	before := len(logger.Entries())
	wire, _, err := outbound.BuildRequest(context.Background(), engine.Outbound(outbound.OutboundTypeAnthropic), outbound.OutboundTypeAnthropic, request, "https://example.invalid/v1", "")
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Body.Close()
	if len(logger.Entries()) <= before {
		t.Fatal("wire builder did not use injected logger")
	}
}

func TestEngineResponseDiagnosticIsolation(t *testing.T) {
	for _, tc := range []struct {
		typ  outbound.OutboundType
		body string
	}{
		{outbound.OutboundTypeAnthropic, `{"id":"msg_1","role":"assistant","model":"claude","content":[{"type":"thinking","thinking":"reason","signature":"sig"}],"stop_reason":"end_turn"}`},
		{outbound.OutboundTypeGemini, `{"candidates":[{"content":{"role":"model","parts":[{"text":"reason","thought":true,"thoughtSignature":"sig"}]},"finishReason":"STOP"}]}`},
	} {
		t.Run(tc.typ.String(), func(t *testing.T) {
			firstLog, secondLog := &testlog.Logger{}, &testlog.Logger{}
			first := polywire.New(polywire.Config{Logger: firstLog})
			second := polywire.New(polywire.Config{Logger: secondLog})
			response := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body))}
			defer response.Body.Close()
			if _, err := first.Outbound(tc.typ).TransformResponse(context.Background(), response); err != nil {
				t.Fatal(err)
			}
			entries := firstLog.Entries()
			if len(entries) != 1 || entries[0].Message != "transformer.reasoning.signature.passthrough" || entries[0].Fields["direction"] != "extract" {
				t.Fatalf("missing provider diagnostic: %+v", entries)
			}
			if len(secondLog.Entries()) != 0 {
				t.Fatal("diagnostics crossed engine instances")
			}
			if second.Outbound(tc.typ) == nil {
				t.Fatal("second engine has no adapter")
			}
		})
	}
}
