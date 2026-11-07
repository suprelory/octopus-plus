package anthropic_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	inbound "github.com/suprelory/octopus-plus/polywire/inbound/anthropic"
	openai "github.com/suprelory/octopus-plus/polywire/inbound/openai"
	"github.com/suprelory/octopus-plus/polywire/model"
	outbound "github.com/suprelory/octopus-plus/polywire/outbound/anthropic"
)

const searchPayload = `[{"type":"web_search_result","url":"https://example.com/source","title":"Source","encrypted_content":"opaque-data","page_age":null,"future":{"rank":0}}]`
const webCitation = `{"type":"web_search_result_location","cited_text":"source","url":"https://example.com/source","title":"Source","encrypted_index":"opaque-index"}`
const documentCitation = `{"type":"char_location","document_index":0,"document_title":null,"start_char_index":0,"end_char_index":6,"cited_text":"source","file_id":"file_1","future":{"offset":0}}`

func nativeContent() string {
	return fmt.Sprintf(`[
		{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"source"},"caller":{"type":"direct"}},
		{"type":"web_search_tool_result","tool_use_id":"srv_1","content":%s},
		{"type":"text","text":"First.","citations":[%s]},
		{"type":"text","text":" "},
		{"type":"text","text":"Second.","citations":[%s]}
	]`, searchPayload, webCitation, documentCitation)
}

func responseBody(content, reason string) string {
	return fmt.Sprintf(`{"id":"msg_native","type":"message","role":"assistant","model":"claude-test","content":%s,"stop_reason":%q,"usage":{"input_tokens":7,"output_tokens":4}}`, content, reason)
}

func nativeStream() []string {
	return []string{
		`{"type":"message_start","message":{"id":"msg_native","model":"claude-test","role":"assistant","usage":{"input_tokens":7,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{},"caller":{"type":"direct"}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"source\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		fmt.Sprintf(`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_1","content":%s}}`, searchPayload),
		`{"type":"content_block_stop","index":1}`,
		fmt.Sprintf(`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":"First","citations":[%s]}}`, webCitation),
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"."}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"text","text":" "}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"content_block_start","index":4,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":"Sec"}}`,
		`{"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":"ond."}}`,
		fmt.Sprintf(`{"type":"content_block_delta","index":4,"delta":{"type":"citations_delta","citation":%s}}`, documentCitation),
		`{"type":"content_block_stop","index":4}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
		`{"type":"message_stop"}`,
	}
}

func decodeJSON(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode JSON %s: %v", data, err)
	}
	return result
}

func assertJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	if !reflect.DeepEqual(decodeJSON(t, got), decodeJSON(t, []byte(want))) {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}

func responseFromUpstream(t *testing.T, body string) *model.InternalLLMResponse {
	t.Helper()
	response, err := (&outbound.MessageOutbound{}).TransformResponse(context.Background(), &http.Response{
		StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertResponseContent(t *testing.T, response *model.InternalLLMResponse, content string) {
	t.Helper()
	body, err := (&inbound.MessagesInbound{}).TransformResponse(context.Background(), response)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, wire.Content, content)
}

// Read the emitted SSE as a client would, independently of the model's event
// aggregation. Reject invalid block boundaries and collect text, JSON and citations.
func collectSSEContent(t *testing.T, data []byte) []byte {
	t.Helper()
	var blocks []map[string]json.RawMessage
	active := -1
	partialInputs := make(map[int]string)
	stopCount := 0
	messageStops := 0
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event struct {
			Type         string                     `json:"type"`
			Index        *int                       `json:"index"`
			ContentBlock map[string]json.RawMessage `json:"content_block"`
			Delta        struct {
				Type        string          `json:"type"`
				Text        string          `json:"text"`
				PartialJSON string          `json:"partial_json"`
				Citation    json.RawMessage `json:"citation"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
			t.Fatalf("invalid SSE data: %v", err)
		}
		switch event.Type {
		case "content_block_start":
			if active != -1 || event.Index == nil || *event.Index != len(blocks) {
				t.Fatalf("invalid block start: %+v, active=%d, blocks=%d", event, active, len(blocks))
			}
			active = *event.Index
			blocks = append(blocks, event.ContentBlock)
		case "content_block_delta":
			if event.Index == nil || active < 0 || *event.Index != active {
				t.Fatalf("delta has no matching open block: %+v", event)
			}
			switch event.Delta.Type {
			case "text_delta":
				var text string
				if err := json.Unmarshal(blocks[active]["text"], &text); err != nil {
					t.Fatal(err)
				}
				blocks[active]["text"], _ = json.Marshal(text + event.Delta.Text)
			case "input_json_delta":
				partialInputs[active] += event.Delta.PartialJSON
			case "citations_delta":
				var citations []json.RawMessage
				if existing := blocks[active]["citations"]; len(existing) > 0 {
					if err := json.Unmarshal(existing, &citations); err != nil {
						t.Fatal(err)
					}
				}
				citations = append(citations, event.Delta.Citation)
				blocks[active]["citations"], _ = json.Marshal(citations)
			default:
				t.Fatalf("unexpected delta type %q", event.Delta.Type)
			}
		case "content_block_stop":
			if event.Index == nil || active < 0 || *event.Index != active {
				t.Fatalf("stop has no matching open block: %+v", event)
			}
			if input, ok := partialInputs[active]; ok {
				if !json.Valid([]byte(input)) {
					t.Fatalf("invalid tool input after streaming: %q", input)
				}
				blocks[active]["input"] = json.RawMessage(input)
			}
			active = -1
			stopCount++
		case "message_stop":
			messageStops++
			if active != -1 {
				t.Fatal("message stopped with an open block")
			}
		}
	}
	if active != -1 || stopCount != len(blocks) || messageStops != 1 {
		t.Fatalf("incomplete SSE lifecycle: active=%d, blocks=%d, stops=%d, message_stops=%d\n%s", active, len(blocks), stopCount, messageStops, data)
	}
	result, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAnthropicNativeStreamAndResponseAgree(t *testing.T) {
	ctx := context.Background()
	expected := nativeContent()
	assertResponseContent(t, responseFromUpstream(t, responseBody(expected, "end_turn")), expected)

	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			provider := &outbound.MessageOutbound{}
			client := &inbound.MessagesInbound{}
			finalizer := model.NewStreamFinalizer()
			var sse []byte
			for _, raw := range nativeStream() {
				var encoded []byte
				var err error
				if legacy {
					chunk, parseErr := provider.TransformStream(ctx, []byte(raw))
					if parseErr != nil {
						t.Fatal(parseErr)
					}
					encoded, err = client.TransformStream(ctx, chunk)
				} else {
					events, parseErr := provider.TransformStreamEvent(ctx, []byte(raw))
					if parseErr != nil {
						t.Fatal(parseErr)
					}
					events, parseErr = finalizer.ProcessStreamEvents(events)
					if parseErr != nil {
						t.Fatal(parseErr)
					}
					encoded, err = client.TransformStreamEvents(ctx, events)
				}
				if err != nil {
					t.Fatalf("encode %s: %v", raw, err)
				}
				sse = append(sse, encoded...)
			}
			if legacy {
				tail, err := client.TransformStream(ctx, &model.InternalLLMResponse{Object: "[DONE]"})
				if err != nil {
					t.Fatal(err)
				}
				sse = append(sse, tail...)
			} else {
				finalized, err := finalizer.FinalizeStream()
				if err != nil {
					t.Fatal(err)
				}
				assertResponseContent(t, finalized.Response, expected)
				tail, err := client.TransformStreamEvents(ctx, finalized.TailEvents)
				if err != nil {
					t.Fatal(err)
				}
				sse = append(sse, tail...)
			}
			assertJSON(t, collectSSEContent(t, sse), expected)
			aggregate, err := client.GetInternalResponse(ctx)
			if err != nil {
				t.Fatal(err)
			}
			assertResponseContent(t, aggregate, expected)
			if len(aggregate.Choices[0].Message.ToolCalls) != 0 {
				t.Fatal("server tool became a client tool call")
			}
			if len(aggregate.Choices[0].Citations) != 2 {
				t.Fatalf("citation aggregate lost or duplicated: %+v", aggregate.Choices[0].Citations)
			}
			if aggregate.Usage == nil || aggregate.Usage.PromptTokens != 7 || aggregate.Usage.CompletionTokens != 4 {
				t.Fatalf("usage changed: %+v", aggregate.Usage)
			}
		})
	}
}

func TestAnthropicNativeContentSurvivesConversationHistory(t *testing.T) {
	for _, withClientTool := range []bool{false, true} {
		t.Run(fmt.Sprintf("client_tool=%v", withClientTool), func(t *testing.T) {
			content := nativeContent()
			if withClientTool {
				content = strings.TrimSuffix(strings.TrimSpace(content), "]") + `,{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"x"}}]`
			}
			document := `[{"type":"document","source":{"type":"text","media_type":"text/plain","data":"source"},"title":"Doc","citations":{"enabled":false}}]`
			body := fmt.Sprintf(`{"model":"claude-test","max_tokens":100,"messages":[{"role":"user","content":%s},{"role":"assistant","content":%s},{"role":"user","content":"continue"}]}`, document, content)
			request, err := (&inbound.MessagesInbound{}).TransformRequest(context.Background(), []byte(body))
			if err != nil {
				t.Fatal(err)
			}
			httpRequest, err := (&outbound.MessageOutbound{}).TransformRequest(context.Background(), request, "https://example.com/v1", "test-key")
			if err != nil {
				t.Fatal(err)
			}
			defer httpRequest.Body.Close()
			encoded, err := io.ReadAll(httpRequest.Body)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Messages []struct {
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Messages) != 3 {
				t.Fatalf("messages changed: %s", encoded)
			}
			assertJSON(t, wire.Messages[0].Content, document)
			assertJSON(t, wire.Messages[1].Content, content)
		})
	}
}

func TestAnthropicServerResultVariantsPreservePayload(t *testing.T) {
	tests := []struct {
		kind    string
		payload string
	}{
		{"web_search_tool_result", searchPayload},
		{"web_search_tool_result", `{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}`},
		{"web_fetch_tool_result", `{"type":"web_fetch_result","url":"https://example.com","content":{"type":"document","source":{"type":"text","data":"body"}},"retrieved_at":"2026-09-06T00:00:00Z"}`},
		{"code_execution_tool_result", `{"type":"code_execution_result","stdout":"ok\n","stderr":"","return_code":0,"content":[{"type":"code_execution_output","file_id":"file_1"}]}`},
		{"bash_code_execution_tool_result", `{"type":"bash_code_execution_result","stdout":"ok","stderr":"","return_code":0,"content":[]}`},
		{"text_editor_code_execution_tool_result", `{"type":"text_editor_code_execution_view_result","file_type":"text","content":"hello","start_line":0,"total_lines":1}`},
		{"mcp_tool_result", `[{"type":"text","text":"ok","annotations":{"audience":["assistant"]}}]`},
		{"future_tool_result", `{"unknown":[1,false,null],"large_id":9007199254740993}`},
		{"future_tool_result", `"opaque result"`},
		{"future_tool_result", `null`},
	}
	for index, test := range tests {
		t.Run(fmt.Sprintf("%s/%d", test.kind, index), func(t *testing.T) {
			block := fmt.Sprintf(`{"type":%q,"tool_use_id":"srv_1","is_error":false,"content":%s}`, test.kind, test.payload)
			expected := "[" + block + "]"
			response := responseFromUpstream(t, responseBody(expected, "end_turn"))
			assertResponseContent(t, response, expected)
			provider := &outbound.MessageOutbound{}
			client := &inbound.MessagesInbound{}
			var sse []byte
			chunks := []string{
				fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":%s}`, block),
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
				`[DONE]`,
			}
			for _, raw := range chunks {
				events, err := provider.TransformStreamEvent(context.Background(), []byte(raw))
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := client.TransformStreamEvents(context.Background(), events)
				if err != nil {
					t.Fatal(err)
				}
				sse = append(sse, encoded...)
			}
			assertJSON(t, collectSSEContent(t, sse), expected)
		})
	}
}

func TestAnthropicCitationsKeepNativeLocations(t *testing.T) {
	citations := []string{
		documentCitation,
		`{"type":"page_location","document_index":0,"document_title":"PDF","start_page_number":1,"end_page_number":2,"cited_text":"source"}`,
		`{"type":"content_block_location","document_index":0,"document_title":null,"start_block_index":0,"end_block_index":1,"cited_text":"source"}`,
		`{"type":"search_result_location","search_result_index":0,"start_block_index":0,"end_block_index":1,"source":"https://example.com","title":null,"cited_text":"source"}`,
		`{"type":"future_location","cited_text":"source","position":{"start":0},"optional":null}`,
	}
	for index, citation := range citations {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			expected := fmt.Sprintf(`[{"type":"text","text":"answer","citations":[%s]}]`, citation)
			assertResponseContent(t, responseFromUpstream(t, responseBody(expected, "end_turn")), expected)
		})
	}
}

func TestAnthropicNativeContentProjectsToChatText(t *testing.T) {
	ctx := context.Background()
	response := responseFromUpstream(t, responseBody(nativeContent(), "end_turn"))
	client := &openai.ChatInbound{}
	body, err := client.TransformResponse(ctx, response)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Choices []struct {
			Message struct {
				Content   string            `json:"content"`
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("invalid Chat response: %s: %v", body, err)
	}
	if len(wire.Choices) != 1 || wire.Choices[0].Message.Content != "First. Second." || len(wire.Choices[0].Message.ToolCalls) != 0 {
		t.Fatalf("invalid Chat content: %s", body)
	}
	for _, forbidden := range []string{"server_tool", "citations", "opaque-data", "opaque-index"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("native content leaked to Chat: %s", body)
		}
	}
	// Encoding for another protocol must not mutate the native representation.
	assertResponseContent(t, response, nativeContent())
}

func TestAnthropicServerAndClientToolInputsStaySeparate(t *testing.T) {
	provider := &outbound.MessageOutbound{}
	var aggregator model.StreamAggregator
	chunks := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"client_1","name":"lookup","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"client\":1}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":4,"content_block":{"type":"mcp_tool_use","id":"srv_1","name":"search","server_name":"docs","input":{}}}`,
		`{"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"{\"server\":"}}`,
		`{"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"true}"}}`,
		`{"type":"content_block_stop","index":4}`,
		`{"type":"content_block_start","index":9,"content_block":{"type":"tool_use","id":"client_2","name":"write","input":{}}}`,
		`{"type":"content_block_delta","index":9,"delta":{"type":"input_json_delta","partial_json":"{\"client\":2}"}}`,
		`{"type":"content_block_stop","index":9}`,
	}
	for _, raw := range chunks {
		events, err := provider.TransformStreamEvent(context.Background(), []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Index != 0 {
				t.Fatalf("content-block index became choice index: %+v", event)
			}
			if event.BlockIndex != nil && *event.BlockIndex == 4 && event.ToolCall != nil {
				t.Fatalf("server tool became a client tool: %+v", event)
			}
		}
		aggregator.Add(model.InternalResponseFromStreamEvents(events))
	}
	response := aggregator.Response()
	calls := response.Choices[0].Message.ToolCalls
	if len(calls) != 2 || calls[0].Index != 0 || calls[1].Index != 1 || calls[0].Function.Arguments != `{"client":1}` || calls[1].Function.Arguments != `{"client":2}` {
		t.Fatalf("client arguments changed: %+v", calls)
	}
	parts := response.Choices[0].Message.Content.MultipleContent
	if len(parts) != 1 || parts[0].ServerToolUse == nil {
		t.Fatalf("server invocation missing: %+v", parts)
	}
	use := parts[0].ServerToolUse
	if string(use.Input) != `{"server":true}` || use.BlockType != "mcp_tool_use" || use.ServerName != "docs" {
		t.Fatalf("server arguments or metadata changed: %+v", use)
	}
}

func TestAnthropicServerUseVariantsRoundTrip(t *testing.T) {
	for _, kind := range []string{"server_tool_use", "mcp_tool_use", "future_tool_use"} {
		t.Run(kind, func(t *testing.T) {
			block := fmt.Sprintf(`{"type":%q,"id":"srv_1","name":"lookup","server_name":"docs","input":{"q":"x"},"caller":{"type":"code_execution_20250825","tool_id":"srv_parent"}}`, kind)
			content := "[" + block + "]"
			assertResponseContent(t, responseFromUpstream(t, responseBody(content, "pause_turn")), content)
			body := fmt.Sprintf(`{"model":"claude-test","max_tokens":100,"messages":[{"role":"user","content":"start"},{"role":"assistant","content":%s},{"role":"user","content":"continue"}]}`, content)
			request, err := (&inbound.MessagesInbound{}).TransformRequest(context.Background(), []byte(body))
			if err != nil {
				t.Fatal(err)
			}
			httpRequest, err := (&outbound.MessageOutbound{}).TransformRequest(context.Background(), request, "https://example.com/v1", "test-key")
			if err != nil {
				t.Fatal(err)
			}
			defer httpRequest.Body.Close()
			encoded, err := io.ReadAll(httpRequest.Body)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Messages []struct {
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			assertJSON(t, wire.Messages[1].Content, content)
		})
	}
}

func TestAnthropicNativeStreamReportsLossToOtherProtocols(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		client interface {
			TransformStreamEvents(context.Context, []model.StreamEvent) ([]byte, error)
		}
	}{
		{"chat", &openai.ChatInbound{}},
		{"responses", &openai.ResponseInbound{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &outbound.MessageOutbound{}
			var output []byte
			sawLoss := false
			for _, raw := range append(nativeStream(), "[DONE]") {
				events, err := provider.TransformStreamEvent(ctx, []byte(raw))
				if err != nil {
					t.Fatal(err)
				}
				data, err := test.client.TransformStreamEvents(ctx, events)
				if err != nil {
					var loss *model.StreamConversionLoss
					if errors.As(err, &loss) && loss.SourceFormat == model.APIFormatAnthropicMessage && len(data) == 0 {
						sawLoss = true
						continue
					}
					t.Fatal(err)
				}
				output = append(output, data...)
			}
			var text strings.Builder
			if !sawLoss {
				t.Fatal("native semantics were silently dropped")
			}
			for _, line := range strings.Split(string(output), "\n") {
				if !strings.HasPrefix(line, "data:") {
					continue
				}
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if data == "[DONE]" {
					continue
				}
				if test.name == "chat" {
					var event struct {
						Choices []struct {
							Delta struct {
								Content   *string           `json:"content"`
								ToolCalls []json.RawMessage `json:"tool_calls"`
							} `json:"delta"`
						} `json:"choices"`
					}
					if err := json.Unmarshal([]byte(data), &event); err != nil {
						t.Fatalf("invalid Chat event: %s: %v", data, err)
					}
					for _, choice := range event.Choices {
						if choice.Delta.Content != nil {
							text.WriteString(*choice.Delta.Content)
						}
						if len(choice.Delta.ToolCalls) != 0 {
							t.Fatalf("server call surfaced as a client tool: %s", data)
						}
					}
				} else {
					var event struct {
						Type  string `json:"type"`
						Delta string `json:"delta"`
					}
					if err := json.Unmarshal([]byte(data), &event); err != nil {
						t.Fatal(err)
					}
					if event.Type == "response.output_text.delta" {
						text.WriteString(event.Delta)
					}
				}
			}
			if text.String() != "First. Second." {
				t.Fatalf("text lost or duplicated: %q\n%s", text.String(), output)
			}
			for _, forbidden := range []string{"server_tool", "mcp_tool", "opaque-data", "opaque-index", "function_call"} {
				if strings.Contains(string(output), forbidden) {
					t.Fatalf("native content leaked: %s", output)
				}
			}
		})
	}
}
