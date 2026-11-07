package polywire_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
	"github.com/suprelory/octopus-plus/polywire/streamio"
)

func convertChatRegressionStream(t *testing.T, clientType inbound.InboundType, chunks []string) ([]byte, *model.InternalLLMResponse) {
	t.Helper()
	ctx := context.Background()
	policy, _ := outbound.TerminalPolicy(outbound.OutboundTypeOpenAIChat)
	converter := model.NewStreamConverter(outbound.Get(outbound.OutboundTypeOpenAIChat), policy)
	client := inbound.Get(clientType)
	var wire bytes.Buffer
	for index, chunk := range chunks {
		events, err := converter.Push(ctx, model.SourceEvent{Data: []byte(chunk), Sequence: int64(index + 1), Transport: model.SourceTransportSSE})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := client.TransformStreamEvents(ctx, events)
		if err != nil {
			t.Fatal(err)
		}
		wire.Write(encoded)
	}
	tail, err := converter.Finish(ctx, model.StreamFinishCauseCleanEOF)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := client.TransformStreamEvents(ctx, tail)
	if err != nil {
		t.Fatal(err)
	}
	wire.Write(encoded)
	return wire.Bytes(), converter.Finalization().Response
}

// Check the emitted protocol, including block lifetime and argument association;
// parsing the result into our own permissive IR alone would miss malformed IDs.
func anthropicRegressionTools(t *testing.T, wire []byte) map[string]string {
	t.Helper()
	source := streamio.NewSSESource(io.NopCloser(bytes.NewReader(wire)), 0)
	defer source.Close()
	activeIndex := -1
	activeID := ""
	arguments := map[string]string{}
	stopped := false
	for {
		event, err := source.ReadSourceEvent(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Block struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if stopped {
			t.Fatalf("event after message_stop: %s", event.Data)
		}
		switch payload.Type {
		case "content_block_start":
			if activeIndex != -1 {
				t.Fatalf("overlapping blocks: %s", wire)
			}
			activeIndex, activeID = payload.Index, payload.Block.ID
			if payload.Block.Type == "tool_use" {
				if activeID == "" || payload.Block.Name == "" {
					t.Fatalf("tool identity lost: %s", event.Data)
				}
				if _, exists := arguments[activeID]; exists {
					t.Fatalf("tool %s started twice", activeID)
				}
				arguments[activeID] = ""
			}
		case "content_block_delta":
			if payload.Index != activeIndex {
				t.Fatalf("delta for closed or unrelated block: %s", event.Data)
			}
			if activeID != "" {
				arguments[activeID] += payload.Delta.PartialJSON
			}
		case "content_block_stop":
			if payload.Index != activeIndex {
				t.Fatalf("stop for closed or unrelated block: %s", event.Data)
			}
			activeIndex, activeID = -1, ""
		case "message_stop":
			if activeIndex != -1 {
				t.Fatal("message ended with an open block")
			}
			stopped = true
		}
	}
	if !stopped {
		t.Fatal("missing message_stop")
	}
	return arguments
}

func TestChatParallelToolsRemainAssociatedInAnthropicStream(t *testing.T) {
	for _, first := range []int{0, 1} {
		t.Run(fmt.Sprintf("first_index_%d", first), func(t *testing.T) {
			second := 1 - first
			chunks := []string{
				fmt.Sprintf(`{"id":"r","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":%d,"id":"call_a","type":"function","function":{"name":"a","arguments":""}},{"index":%d,"id":"call_b","type":"function","function":{"name":"b","arguments":""}}]}}]}`, first, second),
				fmt.Sprintf(`{"id":"r","choices":[{"index":0,"delta":{"tool_calls":[{"index":%d,"function":{"arguments":"{\"a\":"}},{"index":%d,"function":{"arguments":"{\"b\":"}}]}}]}`, first, second),
				fmt.Sprintf(`{"id":"r","choices":[{"index":0,"delta":{"tool_calls":[{"index":%d,"function":{"arguments":"2}"}},{"index":%d,"function":{"arguments":"1}"}}]}}]}`, second, first),
				`{"id":"r","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			}
			wire, _ := convertChatRegressionStream(t, inbound.InboundTypeAnthropic, chunks)
			calls := anthropicRegressionTools(t, wire)
			if len(calls) != 2 || calls["call_a"] != `{"a":1}` || calls["call_b"] != `{"b":2}` {
				t.Fatalf("tool arguments changed: %#v\n%s", calls, wire)
			}
		})
	}
}

func TestRefusalSurvivesAllClientStreamsAndAggregation(t *testing.T) {
	const refusal = "I cannot help with that."
	chunks := []string{
		`{"id":"r","model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"r","choices":[{"index":0,"delta":{"refusal":"I cannot "}}]}`,
		`{"id":"r","choices":[{"index":0,"delta":{"refusal":"help with that."}}]}`,
		`{"id":"r","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`,
	}
	for _, typ := range []inbound.InboundType{inbound.InboundTypeOpenAIChat, inbound.InboundTypeOpenAIResponse, inbound.InboundTypeAnthropic} {
		t.Run(fmt.Sprint(typ), func(t *testing.T) {
			wire, aggregate := convertChatRegressionStream(t, typ, chunks)
			if got := aggregate.Choices[0].Message.Refusal; got != refusal {
				t.Errorf("aggregate refusal = %q, want %q", got, refusal)
			}
			if !bytes.Contains(wire, []byte("I cannot ")) || !bytes.Contains(wire, []byte("help with that.")) {
				t.Errorf("refusal text missing from client stream: %s", wire)
			}
			if typ == inbound.InboundTypeAnthropic && !bytes.Contains(wire, []byte(`"stop_reason":"refusal"`)) {
				t.Errorf("refusal finish reason missing: %s", wire)
			}
		})
	}
}

func TestNonStreamingRefusalSurvivesAnthropicEncoding(t *testing.T) {
	response, err := outbound.Get(outbound.OutboundTypeOpenAIChat).TransformResponse(context.Background(), jsonHTTPResponse([]byte(`{"id":"r","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,"refusal":"I cannot help with that."},"finish_reason":"stop"}]}`)))
	if err != nil {
		t.Fatal(err)
	}
	body, err := inbound.Get(inbound.InboundTypeAnthropic).TransformResponse(context.Background(), response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"text":"I cannot help with that."`) || !strings.Contains(string(body), `"stop_reason":"refusal"`) {
		t.Fatalf("refusal lost from non-streaming response: %s", body)
	}
}

func TestAnthropicInactiveToolStopDoesNotCloseActiveTool(t *testing.T) {
	client := inbound.Get(inbound.InboundTypeAnthropic)
	callA := &model.ToolCall{Index: 0, ID: "call_a", Function: model.FunctionCall{Name: "a"}}
	callB := &model.ToolCall{Index: 1, ID: "call_b", Function: model.FunctionCall{Name: "b"}}
	first, err := client.TransformStreamEvents(context.Background(), []model.StreamEvent{
		{Kind: model.StreamEventKindMessageStart, ID: "r", Model: "m", Role: "assistant"},
		{Kind: model.StreamEventKindToolCallStart, ToolCall: callA},
		{Kind: model.StreamEventKindToolCallStart, ToolCall: callB},
		{Kind: model.StreamEventKindToolCallDelta, ToolCall: callB, Delta: &model.StreamDelta{Arguments: `{"b":2}`}},
		{Kind: model.StreamEventKindToolCallStop, ToolCall: callB},
		{Kind: model.StreamEventKindTextDelta, Delta: &model.StreamDelta{Text: "after tools"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(first, []byte("call_a")) || bytes.Contains(first, []byte("call_b")) || bytes.Contains(first, []byte("content_block_stop")) {
		t.Fatalf("inactive tool affected the active block: %s", first)
	}
	last, err := client.TransformStreamEvents(context.Background(), []model.StreamEvent{
		{Kind: model.StreamEventKindToolCallDelta, ToolCall: callA, Delta: &model.StreamDelta{Arguments: `{"a":1}`}},
		{Kind: model.StreamEventKindToolCallStop, ToolCall: callA},
		{Kind: model.StreamEventKindMessageStop, StopReason: model.FinishReasonToolCalls},
		{Kind: model.StreamEventKindDone},
	})
	if err != nil {
		t.Fatal(err)
	}
	wire := append(first, last...)
	calls := anthropicRegressionTools(t, wire)
	if len(calls) != 2 || calls["call_a"] != `{"a":1}` || calls["call_b"] != `{"b":2}` || !bytes.Contains(wire, []byte("after tools")) {
		t.Fatalf("deferred content lost: %#v\n%s", calls, wire)
	}
}

func TestAnthropicLegacyParallelToolsFlushAtDone(t *testing.T) {
	ctx := context.Background()
	client := inbound.Get(inbound.InboundTypeAnthropic)
	provider := outbound.Get(outbound.OutboundTypeOpenAIChat)
	chunks := []string{
		`{"id":"r","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"a"}},{"index":1,"id":"call_b","type":"function","function":{"name":"b"}}]}}]}`,
		`{"id":"r","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"b\":2}"}},{"index":0,"function":{"arguments":"{\"a\":1}"}}]}}]}`,
		`[DONE]`,
	}
	var wire []byte
	for _, data := range chunks {
		chunk, err := provider.TransformStream(ctx, []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := client.TransformStream(ctx, chunk)
		if err != nil {
			t.Fatal(err)
		}
		wire = append(wire, encoded...)
	}
	calls := anthropicRegressionTools(t, wire)
	if len(calls) != 2 || calls["call_a"] != `{"a":1}` || calls["call_b"] != `{"b":2}` {
		t.Fatalf("legacy tool stream lost arguments: %#v\n%s", calls, wire)
	}
}

func TestAnthropicToolStreamErrorDoesNotFlushDeferredCalls(t *testing.T) {
	client := inbound.Get(inbound.InboundTypeAnthropic)
	_, err := client.TransformStreamEvents(context.Background(), []model.StreamEvent{
		{Kind: model.StreamEventKindToolCallStart, ToolCall: &model.ToolCall{Index: 0, ID: "call_a", Function: model.FunctionCall{Name: "a"}}},
		{Kind: model.StreamEventKindToolCallStart, ToolCall: &model.ToolCall{Index: 1, ID: "call_b", Function: model.FunctionCall{Name: "b"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := client.TransformStreamEvents(context.Background(), []model.StreamEvent{
		{Kind: model.StreamEventKindError, Error: &model.ResponseError{Detail: model.ErrorDetail{Message: "stream interrupted"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wire, []byte("stream interrupted")) || bytes.Contains(wire, []byte("call_b")) || bytes.Contains(wire, []byte("message_stop")) {
		t.Fatalf("error falsely completed deferred calls: %s", wire)
	}
}

func TestRefusalDeltasInOneBatchAreConcatenated(t *testing.T) {
	client := inbound.Get(inbound.InboundTypeOpenAIChat)
	wire, err := client.TransformStreamEvents(context.Background(), []model.StreamEvent{
		{Kind: model.StreamEventKindTextDelta, Delta: &model.StreamDelta{Refusal: "I cannot "}},
		{Kind: model.StreamEventKindTextDelta, Delta: &model.StreamDelta{Refusal: "help with that."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wire, []byte(`"refusal":"I cannot help with that."`)) {
		t.Fatalf("a refusal fragment disappeared from the event batch: %s", wire)
	}
}
