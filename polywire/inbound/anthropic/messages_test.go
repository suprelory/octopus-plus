package anthropic

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/compat"
	"github.com/suprelory/octopus-plus/polywire/config"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestAnthropicRequestEmptyThinkingSignatureBecomesGeminiSignature(t *testing.T) {
	inbound := &MessagesInbound{}
	body := []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":16,
		"messages":[{
			"role":"assistant",
			"content":[
				{"type":"thinking","thinking":"","signature":"sig-gemini"},
				{"type":"tool_use","id":"call_Bash_2","name":"Bash","input":{"command":"pwd"}}
			]
		}]
	}`)

	req, err := inbound.TransformRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("TransformRequest() error = %v", err)
	}
	msg := req.Messages[0]
	if msg.ReasoningContent != nil || msg.ReasoningSignature != nil {
		t.Fatalf("expected Gemini shim not to populate Anthropic flat reasoning fields, got content=%v signature=%v", msg.ReasoningContent, msg.ReasoningSignature)
	}
	if len(msg.ReasoningBlocks) != 1 {
		t.Fatalf("expected one reasoning block, got %+v", msg.ReasoningBlocks)
	}
	block := msg.ReasoningBlocks[0]
	if block.Kind != model.ReasoningBlockKindSignature || block.Provider != "gemini" || block.Signature != "sig-gemini" {
		t.Fatalf("unexpected reasoning block: %+v", block)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ThoughtSignature != "sig-gemini" {
		t.Fatalf("expected signature bound to tool call, got %+v", msg.ToolCalls)
	}
}

func TestAnthropicRequestKeepsNonEmptyThinkingAnthropic(t *testing.T) {
	inbound := &MessagesInbound{}
	body := []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":16,
		"messages":[{
			"role":"assistant",
			"content":[{"type":"thinking","thinking":"real thought","signature":"sig-anthropic"}]
		}]
	}`)

	req, err := inbound.TransformRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("TransformRequest() error = %v", err)
	}
	msg := req.Messages[0]
	if len(msg.ReasoningBlocks) != 1 {
		t.Fatalf("expected one reasoning block, got %+v", msg.ReasoningBlocks)
	}
	block := msg.ReasoningBlocks[0]
	if block.Kind != model.ReasoningBlockKindThinking || block.Provider != "anthropic" || block.Text != "real thought" || block.Signature != "sig-anthropic" {
		t.Fatalf("unexpected reasoning block: %+v", block)
	}
	if msg.ReasoningContent == nil || *msg.ReasoningContent != "real thought" || msg.ReasoningSignature == nil || *msg.ReasoningSignature != "sig-anthropic" {
		t.Fatalf("expected flat Anthropic reasoning fields, got content=%v signature=%v", msg.ReasoningContent, msg.ReasoningSignature)
	}
}

func TestTransformResponseEmitsGeminiThoughtSignatureShim(t *testing.T) {
	inbound := &MessagesInbound{}
	signatureBlock := model.ReasoningBlock{Kind: model.ReasoningBlockKindSignature, Index: -1}
	signatureBlock.SetOpaqueSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderGemini,
		Kind:     model.OpaqueSignatureKindGeminiThought,
		Value:    "sig-gemini",
		ToolCallScope: &model.SignatureToolCallScope{
			ID:   "call_Bash_2",
			Name: "Bash",
		},
	})
	out, err := inbound.TransformResponse(context.Background(), &model.InternalLLMResponse{
		ID:    "msg_1",
		Model: "gemini-3.1-pro",
		Choices: []model.Choice{{
			Message: &model.Message{
				Role:            "assistant",
				ReasoningBlocks: []model.ReasoningBlock{signatureBlock},
				ToolCalls: []model.ToolCall{{
					ID: "call_Bash_2",
					Function: model.FunctionCall{
						Name:      "Bash",
						Arguments: `{"command":"pwd"}`,
					},
					ThoughtSignature: "sig-gemini",
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("TransformResponse() error = %v", err)
	}

	var resp Message
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal response: %v\n%s", err, out)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("expected thinking shim and tool_use, got %+v", resp.Content)
	}
	shim := resp.Content[0]
	if shim.Type != "thinking" || shim.Thinking == nil || *shim.Thinking != "" || shim.Signature == nil || *shim.Signature != "sig-gemini" {
		t.Fatalf("unexpected shim block: %+v", shim)
	}
	if resp.Content[1].Type != "tool_use" {
		t.Fatalf("expected tool_use after shim, got %+v", resp.Content[1])
	}
}

func TestTransformResponseFiltersReasoningSignatureProvenance(t *testing.T) {
	tests := []struct {
		name          string
		configure     func(*model.Message)
		wantSignature bool
	}{
		{
			name: "legacy",
			configure: func(message *model.Message) {
				message.ReasoningSignature = stringPtr("legacy-signature")
			},
			wantSignature: true,
		},
		{
			name: "anthropic",
			configure: func(message *model.Message) {
				message.SetOpaqueReasoningSignature(model.OpaqueSignature{
					Provider: model.SignatureProviderAnthropic,
					Kind:     model.OpaqueSignatureKindAnthropicThinking,
					Value:    "anthropic-signature",
				})
			},
			wantSignature: true,
		},
		{
			name: "foreign",
			configure: func(message *model.Message) {
				message.SetOpaqueReasoningSignature(model.OpaqueSignature{
					Provider: model.SignatureProviderOpenAI,
					Kind:     model.OpaqueSignatureKindOpenAIReasoning,
					Value:    "openai-signature",
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := &model.Message{Role: "assistant", ReasoningContent: stringPtr("summary")}
			test.configure(message)
			out, err := (&MessagesInbound{}).TransformResponse(context.Background(), &model.InternalLLMResponse{
				ID:      "msg_1",
				Model:   "model",
				Choices: []model.Choice{{Message: message}},
			})
			if err != nil {
				t.Fatalf("TransformResponse() error = %v", err)
			}
			var response Message
			if err := json.Unmarshal(out, &response); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if len(response.Content) == 0 || response.Content[0].Type != "thinking" {
				t.Fatalf("missing thinking block: %+v", response.Content)
			}
			if got := response.Content[0].Signature != nil; got != test.wantSignature {
				t.Fatalf("signature present = %t, want %t: %+v", got, test.wantSignature, response.Content[0])
			}
		})
	}
}

func TestTransformResponseDropsForeignBlockSignature(t *testing.T) {
	block := model.ReasoningBlock{Kind: model.ReasoningBlockKindThinking, Index: -1, Text: "summary"}
	block.SetOpaqueSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderOpenAI,
		Kind:     model.OpaqueSignatureKindOpenAIReasoning,
		Value:    "openai-signature",
	})
	out, err := (&MessagesInbound{}).TransformResponse(context.Background(), &model.InternalLLMResponse{
		ID:      "msg_1",
		Model:   "model",
		Choices: []model.Choice{{Message: &model.Message{Role: "assistant", ReasoningBlocks: []model.ReasoningBlock{block}}}},
	})
	if err != nil {
		t.Fatalf("TransformResponse() error = %v", err)
	}
	var response Message
	if err := json.Unmarshal(out, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(response.Content) == 0 || response.Content[0].Thinking == nil || *response.Content[0].Thinking != "summary" {
		t.Fatalf("foreign block text was not preserved: %+v", response.Content)
	}
	if response.Content[0].Signature != nil {
		t.Fatalf("foreign block signature leaked: %+v", response.Content[0])
	}
}

func TestAnthropicRequestRestoresGeminiThoughtSignatureFromCache(t *testing.T) {
	cfg := config.Config{SignatureStore: compat.NewMemorySignatureStore()}
	inbound := New(cfg)
	_, err := inbound.TransformRequest(context.Background(), []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":16,
		"messages":[{"role":"user","content":"run a tool"}]
	}`))
	if err != nil {
		t.Fatalf("initialize request scope: %v", err)
	}
	_, err = inbound.TransformResponse(context.Background(), &model.InternalLLMResponse{
		ID:    "msg_1",
		Model: "gemini-3.1-pro",
		Choices: []model.Choice{{
			Message: &model.Message{
				Role: "assistant",
				ToolCalls: []model.ToolCall{{
					ID: "call_restore_1",
					Function: model.FunctionCall{
						Name:      "default_api:Bash",
						Arguments: `{"command":"pwd"}`,
					},
					ThoughtSignature: "sig-from-cache",
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("TransformResponse() error = %v", err)
	}

	body := []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":16,
		"messages":[{
			"role":"assistant",
			"content":[{"type":"tool_use","id":"call_restore_1","name":"default_api:Bash","input":{"command":"pwd"}}]
		}]
	}`)
	req, err := New(cfg).TransformRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("TransformRequest() error = %v", err)
	}
	if len(req.Messages) != 1 || len(req.Messages[0].ToolCalls) != 1 {
		t.Fatalf("expected one restored tool call, got %+v", req.Messages)
	}
	if got := req.Messages[0].ToolCalls[0].ThoughtSignature; got != "sig-from-cache" {
		t.Fatalf("restored signature = %q, want sig-from-cache", got)
	}
}

func TestTransformResponseOmitsOctopusExtension(t *testing.T) {
	inbound := &MessagesInbound{}
	out, err := inbound.TransformResponse(context.Background(), &model.InternalLLMResponse{
		ID:    "msg_1",
		Model: "gemini-3.1-pro",
		Choices: []model.Choice{{
			Message: &model.Message{
				Role: "assistant",
				ToolCalls: []model.ToolCall{{
					ID: "call_Bash_2",
					Function: model.FunctionCall{
						Name:      "Bash",
						Arguments: `{}`,
					},
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("TransformResponse() error = %v", err)
	}
	if strings.Contains(string(out), "_octopus") {
		t.Fatalf("unexpected _octopus extension without signature: %s", out)
	}
}

func TestTransformStreamEmitsGeminiThoughtSignatureShim(t *testing.T) {
	inbound := &MessagesInbound{}
	signatureBlock := model.ReasoningBlock{Kind: model.ReasoningBlockKindSignature, Index: -1}
	signatureBlock.SetOpaqueSignature(model.OpaqueSignature{
		Provider: model.SignatureProviderGemini,
		Kind:     model.OpaqueSignatureKindGeminiThought,
		Value:    "sig-gemini",
		ToolCallScope: &model.SignatureToolCallScope{
			ID:   "call_Bash_2",
			Name: "Bash",
		},
	})
	out, err := inbound.TransformStream(context.Background(), &model.InternalLLMResponse{
		ID:     "msg_1",
		Model:  "gemini-3.1-pro",
		Object: "chat.completion.chunk",
		Choices: []model.Choice{{
			Delta: &model.Message{
				Role:            "assistant",
				ReasoningBlocks: []model.ReasoningBlock{signatureBlock},
				ToolCalls: []model.ToolCall{{
					Index: 0,
					ID:    "call_Bash_2",
					Function: model.FunctionCall{
						Name:      "Bash",
						Arguments: `{"command":"pwd"}`,
					},
					ThoughtSignature: "sig-gemini",
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("TransformStream() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`"type":"thinking"`,
		`"type":"signature_delta"`,
		`"signature":"sig-gemini"`,
		`"type":"tool_use"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in stream, got %s", want, text)
		}
	}
	if strings.Index(text, `"type":"signature_delta"`) > strings.Index(text, `"type":"tool_use"`) {
		t.Fatalf("expected signature_delta before tool_use, got %s", text)
	}
}

func TestTransformStreamFiltersReasoningSignatureProvenance(t *testing.T) {
	tests := []struct {
		name          string
		configure     func(*model.ReasoningBlock)
		wantSignature bool
	}{
		{
			name:          "legacy",
			configure:     func(*model.ReasoningBlock) {},
			wantSignature: true,
		},
		{
			name: "anthropic",
			configure: func(block *model.ReasoningBlock) {
				block.SetOpaqueSignature(model.OpaqueSignature{
					Provider: model.SignatureProviderAnthropic,
					Kind:     model.OpaqueSignatureKindAnthropicThinking,
					Value:    "signature",
				})
			},
			wantSignature: true,
		},
		{
			name: "foreign",
			configure: func(block *model.ReasoningBlock) {
				block.SetOpaqueSignature(model.OpaqueSignature{
					Provider: model.SignatureProviderOpenAI,
					Kind:     model.OpaqueSignatureKindOpenAIReasoning,
					Value:    "signature",
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			block := model.ReasoningBlock{Kind: model.ReasoningBlockKindThinking, Index: -1, Text: "summary", Signature: "signature"}
			test.configure(&block)
			out, err := (&MessagesInbound{}).TransformStream(context.Background(), &model.InternalLLMResponse{
				ID:      "msg_1",
				Model:   "model",
				Object:  "chat.completion.chunk",
				Choices: []model.Choice{{Delta: &model.Message{Role: "assistant", ReasoningBlocks: []model.ReasoningBlock{block}}}},
			})
			if err != nil {
				t.Fatalf("TransformStream() error = %v", err)
			}
			got := strings.Contains(string(out), `"type":"signature_delta"`)
			if got != test.wantSignature {
				t.Fatalf("signature_delta present = %t, want %t: %s", got, test.wantSignature, out)
			}
		})
	}
}

func TestTransformRequestCapturesMCPServersAndContainer(t *testing.T) {
	inbound := &MessagesInbound{}
	body := []byte(`{
		"model":"claude-opus-4",
		"max_tokens":16,
		"messages":[{"role":"user","content":"hi"}],
		"mcp_servers":[{"type":"url","url":"https://example.invalid/mcp","name":"demo"}],
		"container":{"id":"cntr-1"}
	}`)
	req, err := inbound.TransformRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("TransformRequest() error = %v", err)
	}
	anthropicExt := req.GetAnthropicExtensions()
	if !strings.Contains(string(anthropicExt.MCPServers), "example.invalid/mcp") {
		t.Errorf("expected extension mcp_servers captured, got %s", anthropicExt.MCPServers)
	}
	if !strings.Contains(string(anthropicExt.Container), "cntr-1") {
		t.Errorf("expected extension container captured, got %s", anthropicExt.Container)
	}
}

func TestTransformRequestPreservesAnthropicUserIDInTransformerMetadataOnly(t *testing.T) {
	inbound := &MessagesInbound{}
	body := []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":16,
		"messages":[{"role":"user","content":"hello"}],
		"metadata":{"user_id":"user-123"}
	}`)

	req, err := inbound.TransformRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("TransformRequest() error = %v", err)
	}
	if req.User != nil {
		t.Fatalf("expected user to remain unset for cross-provider safety, got %+v", req.User)
	}
	if got := req.TransformerMetadataValue(model.TransformerMetadataAnthropicUserID); got != "user-123" {
		t.Fatalf("expected transformer metadata to keep anthropic user id, got %q", got)
	}
	if req.Metadata["user_id"] != "" {
		t.Fatalf("expected generic metadata.user_id to stay empty, got %q", req.Metadata["user_id"])
	}
}

// A-H3: TransformRequest should surface Anthropic `top_k` and `service_tier`
// onto the internal request so outbound transformers (Anthropic, Gemini, and
// OpenAI-compat models such as Qwen) can forward them upstream.
func TestTransformRequestExtractsTopKAndServiceTier(t *testing.T) {
	inbound := &MessagesInbound{}
	body := []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":16,
		"messages":[{"role":"user","content":"hello"}],
		"top_k":32,
		"service_tier":"priority"
	}`)

	req, err := inbound.TransformRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	if req.TopK == nil || *req.TopK != 32 {
		t.Fatalf("expected top_k=32 on internal request, got %+v", req.TopK)
	}
	if req.ServiceTier == nil || *req.ServiceTier != "priority" {
		t.Fatalf("expected service_tier=priority on internal request, got %+v", req.ServiceTier)
	}
}

func TestTransformRequestPreservesUnknownCacheControlValues(t *testing.T) {
	inbound := &MessagesInbound{}
	body := []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":16,
		"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"future_type","ttl":"future_ttl"}}]}]
	}`)

	req, err := inbound.TransformRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	if len(req.Messages) != 1 || req.Messages[0].CacheControl == nil {
		t.Fatalf("expected cache_control on simplified message, got %+v", req.Messages)
	}
	cc := req.Messages[0].CacheControl
	if cc.Type != "future_type" || cc.TTL != "future_ttl" {
		t.Fatalf("expected raw cache_control values preserved, got %+v", cc)
	}
}

func TestTransformStreamDoesNotStopMissingContentBlock(t *testing.T) {
	inbound := &MessagesInbound{}

	first, err := inbound.TransformStream(context.Background(), &model.InternalLLMResponse{
		ID:    "msg_1",
		Model: "gemini-3.1-pro",
		Choices: []model.Choice{
			{
				Index:        0,
				FinishReason: stringPtr("stop"),
			},
		},
	})
	if err != nil {
		t.Fatalf("first TransformStream() error = %v", err)
	}
	text := string(first)
	if strings.Contains(text, "content_block_stop") {
		t.Fatalf("expected no content_block_stop when no block was opened, got %s", text)
	}
	if strings.Contains(text, "message_stop") {
		t.Fatalf("expected message_stop to wait until usage or done, got %s", text)
	}

	done, err := inbound.TransformStream(context.Background(), &model.InternalLLMResponse{Object: "[DONE]"})
	if err != nil {
		t.Fatalf("done TransformStream() error = %v", err)
	}
	doneText := string(done)
	if !strings.Contains(doneText, "message_delta") || !strings.Contains(doneText, "message_stop") {
		t.Fatalf("expected done to finalize stream, got %s", doneText)
	}
}

func TestTransformResponseSynthesizesMissingStopReason(t *testing.T) {
	tests := []struct {
		name       string
		message    *model.Message
		wantReason string
	}{
		{
			name: "text",
			message: &model.Message{
				Role:    "assistant",
				Content: model.MessageContent{Content: stringPtr("hello")},
			},
			wantReason: "end_turn",
		},
		{
			name: "tool call",
			message: &model.Message{
				Role: "assistant",
				ToolCalls: []model.ToolCall{{
					ID:       "call_1",
					Type:     "function",
					Function: model.FunctionCall{Name: "lookup", Arguments: `{}`},
				}},
			},
			wantReason: "tool_use",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inbound := &MessagesInbound{}
			out, err := inbound.TransformResponse(context.Background(), &model.InternalLLMResponse{
				ID: "msg_1",
				Choices: []model.Choice{{
					Index:   0,
					Message: tt.message,
				}},
			})
			if err != nil {
				t.Fatalf("TransformResponse: %v", err)
			}
			var response Message
			if err := json.Unmarshal(out, &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.StopReason == nil || *response.StopReason != tt.wantReason {
				t.Fatalf("stop reason = %#v, want %q; response=%s", response.StopReason, tt.wantReason, out)
			}
		})
	}
}

func TestTransformStreamSynthesizesMissingStopReasonAtDone(t *testing.T) {
	tests := []struct {
		name       string
		chunk      *model.InternalLLMResponse
		wantReason string
		wantBlock  string
	}{
		{
			name: "text",
			chunk: &model.InternalLLMResponse{
				ID: "msg_1",
				Choices: []model.Choice{{Index: 0, Delta: &model.Message{
					Role:    "assistant",
					Content: model.MessageContent{Content: stringPtr("hello")},
				}}},
			},
			wantReason: "end_turn",
			wantBlock:  "text_delta",
		},
		{
			name: "tool call",
			chunk: &model.InternalLLMResponse{
				ID: "msg_1",
				Choices: []model.Choice{{Index: 0, Delta: &model.Message{
					Role: "assistant",
					ToolCalls: []model.ToolCall{{
						Index:    0,
						ID:       "call_1",
						Type:     "function",
						Function: model.FunctionCall{Name: "lookup", Arguments: `{}`},
					}},
				}}},
			},
			wantReason: "tool_use",
			wantBlock:  "tool_use",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inbound := &MessagesInbound{}
			first, err := inbound.TransformStream(context.Background(), tt.chunk)
			if err != nil {
				t.Fatalf("stream chunk: %v", err)
			}
			if !strings.Contains(string(first), tt.wantBlock) {
				t.Fatalf("expected %q block, got %s", tt.wantBlock, first)
			}

			usageChunk := &model.InternalLLMResponse{Usage: &model.Usage{PromptTokens: 7, CompletionTokens: 3}}
			if _, err := inbound.TransformStream(context.Background(), usageChunk); err != nil {
				t.Fatalf("usage chunk: %v", err)
			}
			done, err := inbound.TransformStream(context.Background(), &model.InternalLLMResponse{Object: "[DONE]"})
			if err != nil {
				t.Fatalf("done: %v", err)
			}
			text := string(done)
			if !strings.Contains(text, `"stop_reason":"`+tt.wantReason+`"`) || !strings.Contains(text, "event:message_stop") || !strings.Contains(text, `"input_tokens":7`) {
				t.Fatalf("missing synthesized termination or usage: %s", text)
			}
		})
	}
}

func TestTransformStreamEventsDirectAnthropicSSE(t *testing.T) {
	inbound := &MessagesInbound{}
	events := []model.StreamEvent{
		{Kind: model.StreamEventKindMessageStart, ID: "msg_1", Model: "claude-test", Role: "assistant"},
		{Kind: model.StreamEventKindThinkingDelta, ID: "msg_1", Model: "claude-test", Delta: &model.StreamDelta{Thinking: "think", Signature: "sig"}},
		{Kind: model.StreamEventKindTextDelta, ID: "msg_1", Model: "claude-test", Delta: &model.StreamDelta{Text: "hello"}},
		{Kind: model.StreamEventKindToolCallStart, ID: "msg_1", Model: "claude-test", Index: 0, ToolCall: &model.ToolCall{Index: 0, ID: "call_1", Function: model.FunctionCall{Name: "lookup"}}},
		{Kind: model.StreamEventKindToolCallDelta, ID: "msg_1", Model: "claude-test", Index: 0, ToolCall: &model.ToolCall{Index: 0, ID: "call_1", Function: model.FunctionCall{Name: "lookup"}}, Delta: &model.StreamDelta{Arguments: `{"q":"x"}`}},
		{Kind: model.StreamEventKindMessageStop, ID: "msg_1", Model: "claude-test", StopReason: model.FinishReasonToolCalls},
		{Kind: model.StreamEventKindUsageDelta, ID: "msg_1", Model: "claude-test", Usage: &model.Usage{PromptTokens: 3, CompletionTokens: 4}},
	}

	out, err := inbound.TransformStreamEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("TransformStreamEvents() error = %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"event:message_start",
		`"type":"thinking_delta"`,
		`"type":"signature_delta"`,
		`"type":"text_delta"`,
		`"type":"tool_use"`,
		`"partial_json":"{\"q\":\"x\"}"`,
		`"stop_reason":"tool_use"`,
		`"input_tokens":3`,
		"event:message_stop",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in SSE, got %s", want, text)
		}
	}
}

func TestTransformStreamEventsDropsForeignSignatureSource(t *testing.T) {
	inbound := &MessagesInbound{}
	events := []model.StreamEvent{
		{Kind: model.StreamEventKindMessageStart, ID: "msg_1", Model: "model", Role: "assistant"},
		{
			Kind:  model.StreamEventKindThinkingDelta,
			ID:    "msg_1",
			Model: "model",
			Delta: &model.StreamDelta{
				Thinking:  "summary",
				Signature: "openai-signature",
				SignatureSource: &model.OpaqueSignature{
					Provider: model.SignatureProviderOpenAI,
					Kind:     model.OpaqueSignatureKindOpenAIReasoning,
					Value:    "openai-signature",
				},
			},
		},
	}

	out, err := inbound.TransformStreamEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("TransformStreamEvents() error = %v", err)
	}
	text := string(out)
	if !strings.Contains(text, `"type":"thinking_delta"`) {
		t.Fatalf("foreign block text was not preserved: %s", text)
	}
	if strings.Contains(text, `"type":"signature_delta"`) || strings.Contains(text, "openai-signature") {
		t.Fatalf("foreign signature leaked into Anthropic SSE: %s", text)
	}
}

func TestTransformStreamEventsStartsRepeatedToolIndexAfterStop(t *testing.T) {
	inbound := &MessagesInbound{}
	first := []model.StreamEvent{
		{Kind: model.StreamEventKindMessageStart, ID: "msg_1", Model: "gemini-test", Role: "assistant"},
		{Kind: model.StreamEventKindToolCallStart, ID: "msg_1", Model: "gemini-test", Index: 0, ToolCall: &model.ToolCall{Index: 0, ID: "call_search_0", Function: model.FunctionCall{Name: "Search"}}},
		{Kind: model.StreamEventKindToolCallDelta, ID: "msg_1", Model: "gemini-test", Index: 0, ToolCall: &model.ToolCall{Index: 0, ID: "call_search_0", Function: model.FunctionCall{Name: "Search"}}, Delta: &model.StreamDelta{Arguments: `{"query":"one"}`}},
		{Kind: model.StreamEventKindToolCallStop, ID: "msg_1", Model: "gemini-test", Index: 0},
	}
	firstOut, err := inbound.TransformStreamEvents(context.Background(), first)
	if err != nil {
		t.Fatalf("first TransformStreamEvents() error = %v", err)
	}
	if !strings.Contains(string(firstOut), `"index":0`) || !strings.Contains(string(firstOut), `"type":"tool_use"`) {
		t.Fatalf("expected first tool block at index 0, got %s", firstOut)
	}

	second := []model.StreamEvent{
		{Kind: model.StreamEventKindToolCallStart, ID: "msg_1", Model: "gemini-test", Index: 0, ToolCall: &model.ToolCall{Index: 0, ID: "call_list_0", Function: model.FunctionCall{Name: "List"}}},
		{Kind: model.StreamEventKindToolCallDelta, ID: "msg_1", Model: "gemini-test", Index: 0, ToolCall: &model.ToolCall{Index: 0, ID: "call_list_0", Function: model.FunctionCall{Name: "List"}}, Delta: &model.StreamDelta{Arguments: `{"path":"."}`}},
		{Kind: model.StreamEventKindToolCallStop, ID: "msg_1", Model: "gemini-test", Index: 0},
	}
	secondOut, err := inbound.TransformStreamEvents(context.Background(), second)
	if err != nil {
		t.Fatalf("second TransformStreamEvents() error = %v", err)
	}
	secondText := string(secondOut)
	start := strings.Index(secondText, `"type":"content_block_start"`)
	delta := strings.Index(secondText, `"type":"content_block_delta"`)
	if start < 0 || delta < 0 || start > delta {
		t.Fatalf("expected second repeated-index tool to start before delta, got %s", secondText)
	}
	for _, want := range []string{`"index":1`, `"type":"tool_use"`, `"id":"call_list_0"`, `"partial_json":"{\"path\":\".\"}"`} {
		if !strings.Contains(secondText, want) {
			t.Fatalf("expected %q in second tool SSE, got %s", want, secondText)
		}
	}
}

func TestTransformStreamEventsDirectErrorAndDone(t *testing.T) {
	inbound := &MessagesInbound{}
	errOut, err := inbound.TransformStreamEvents(context.Background(), []model.StreamEvent{{Kind: model.StreamEventKindError, Error: &model.ResponseError{Detail: model.ErrorDetail{Message: "boom"}}}})
	if err != nil {
		t.Fatalf("error event: %v", err)
	}
	if !strings.Contains(string(errOut), `"type":"api_error"`) || !strings.Contains(string(errOut), `"message":"boom"`) {
		t.Fatalf("expected api_error SSE, got %s", errOut)
	}

	inbound = &MessagesInbound{}
	doneOut, err := inbound.TransformStreamEvents(context.Background(), []model.StreamEvent{
		{Kind: model.StreamEventKindMessageStart, ID: "msg_1", Model: "claude-test"},
		{Kind: model.StreamEventKindTextDelta, Delta: &model.StreamDelta{Text: "hi"}},
		{Kind: model.StreamEventKindMessageStop, StopReason: model.FinishReasonStop},
		{Kind: model.StreamEventKindDone},
	})
	if err != nil {
		t.Fatalf("done event: %v", err)
	}
	if !strings.Contains(string(doneOut), "event:message_stop") {
		t.Fatalf("expected done to finalize message, got %s", doneOut)
	}
}

func stringPtr(v string) *string {
	return &v
}

// A-C2: when the outbound layer surfaces an upstream error chunk, the
// Anthropic inbound must emit an Anthropic-compatible `event: error` SSE frame
// so clients see the failure reason instead of a truncated response.
func TestTransformStreamSurfacesErrorAsSSE(t *testing.T) {
	inbound := &MessagesInbound{}

	out, err := inbound.TransformStream(context.Background(), &model.InternalLLMResponse{
		Error: &model.ResponseError{
			StatusCode: 529,
			Detail: model.ErrorDetail{
				Type:    "overloaded_error",
				Message: "Overloaded",
			},
		},
	})
	if err != nil {
		t.Fatalf("TransformStream() error = %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "event:error") {
		t.Fatalf("expected `event:error` SSE frame, got %q", text)
	}
	if !strings.Contains(text, `"type":"overloaded_error"`) {
		t.Fatalf("expected error type to be preserved, got %q", text)
	}
	if !strings.Contains(text, `"message":"Overloaded"`) {
		t.Fatalf("expected error message to be preserved, got %q", text)
	}
}

// A-C2 (fallback): missing error.type should degrade to `api_error` so the
// Anthropic SSE payload remains schema-valid.
func TestTransformStreamErrorDefaultsTypeWhenEmpty(t *testing.T) {
	inbound := &MessagesInbound{}

	out, err := inbound.TransformStream(context.Background(), &model.InternalLLMResponse{
		Error: &model.ResponseError{
			Detail: model.ErrorDetail{Message: "unknown"},
		},
	})
	if err != nil {
		t.Fatalf("TransformStream() error = %v", err)
	}
	if !strings.Contains(string(out), `"type":"api_error"`) {
		t.Fatalf("expected fallback type=api_error, got %q", string(out))
	}
}

// A-H5: TransformRequest must preserve the server-tool Type and raw spec
// payload on the InternalLLMRequest so the outbound anthropic transformer can
// rehydrate the wire object and attach the matching beta header.
func TestTransformRequestPreservesServerToolOnInternalRequest(t *testing.T) {
	inbound := &MessagesInbound{}
	body := []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":16,
		"messages":[{"role":"user","content":"hello"}],
		"tools":[
			{"type":"web_search_20250305","name":"web_search","max_uses":3,"allowed_domains":["a.com"]},
			{"name":"lookup","description":"look","input_schema":{"type":"object"}}
		]
	}`)
	req, err := inbound.TransformRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	if len(req.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(req.Tools))
	}
	srv := req.Tools[0]
	if srv.Type != "web_search_20250305" {
		t.Fatalf("server tool Type lost, got %q", srv.Type)
	}
	if len(srv.AnthropicServerSpec) == 0 {
		t.Fatalf("server tool raw spec lost")
	}
	if !strings.Contains(string(srv.AnthropicServerSpec), "allowed_domains") {
		t.Fatalf("spec-specific fields missing, got %s", string(srv.AnthropicServerSpec))
	}
	fn := req.Tools[1]
	if fn.Type != "function" {
		t.Fatalf("function tool Type = %q, want function", fn.Type)
	}
	if fn.Function.Name != "lookup" {
		t.Fatalf("function tool name mismatch, got %q", fn.Function.Name)
	}
}

func TestTransformRequestRecordsMaxTokensRepair(t *testing.T) {
	req, err := (&MessagesInbound{}).TransformRequest(context.Background(), []byte(`{
		"model":"claude-test",
		"max_tokens":0,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 1 {
		t.Fatalf("max_tokens was not repaired: %#v", req.MaxTokens)
	}
	if got := req.TransformerMetadataValue(model.TransformerMetadataAnthropicMaxTokensRepairFrom); got != "0" {
		t.Fatalf("repair provenance = %q, want 0", got)
	}
}

// TestSeedRequestStateMatchesParseResult verifies that seeding a fresh
// adapter from the canonical request reproduces exactly the request-derived
// state TransformRequest produced, so retry attempts need neither the raw
// body nor a re-parse.
func TestSeedRequestStateMatchesParseResult(t *testing.T) {
	parsed := &MessagesInbound{}
	body := []byte(`{
		"model":"claude-opus-4",
		"max_tokens":16,
		"system":[{"type":"text","text":"You are helpful."}],
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[{"type":"text","text":"hi there"}]}
		],
		"tools":[{"name":"lookup","description":"look things up","input_schema":{"type":"object"}}]
	}`)
	req, err := parsed.TransformRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("TransformRequest() error = %v", err)
	}
	if req.EstimatedInputTokens != parsed.inputToken {
		t.Fatalf("EstimatedInputTokens = %d, want adapter inputToken %d", req.EstimatedInputTokens, parsed.inputToken)
	}
	if parsed.inputToken == 0 {
		t.Fatal("expected a non-zero token estimate for a non-trivial request")
	}

	seeded := &MessagesInbound{}
	seeded.SeedRequestState(req)
	if seeded.requestModel != parsed.requestModel {
		t.Fatalf("seeded requestModel = %q, want %q", seeded.requestModel, parsed.requestModel)
	}
	if seeded.inputToken != parsed.inputToken {
		t.Fatalf("seeded inputToken = %d, want %d", seeded.inputToken, parsed.inputToken)
	}

	// The seeded adapter must synthesize the same message_start usage the
	// parsed adapter would.
	events := []model.StreamEvent{
		{Kind: model.StreamEventKindMessageStart, ID: "msg_1", Model: "claude-opus-4", Role: "assistant"},
		{Kind: model.StreamEventKindMessageStop, ID: "msg_1", Model: "claude-opus-4", StopReason: model.FinishReasonStop},
	}
	out, err := seeded.TransformStreamEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("TransformStreamEvents() error = %v", err)
	}
	want := `"input_tokens":` + strconv.FormatInt(seeded.inputToken, 10)
	if !strings.Contains(string(out), want) {
		t.Fatalf("expected %q in message_start SSE, got %s", want, out)
	}
}

// TestSeedRequestStateNilGuards keeps the optional-interface contract safe
// for zero-value callers.
func TestSeedRequestStateNilGuards(t *testing.T) {
	seeded := &MessagesInbound{}
	seeded.SeedRequestState(nil)
	if seeded.requestModel != "" || seeded.inputToken != 0 {
		t.Fatalf("nil seed mutated state: requestModel=%q inputToken=%d", seeded.requestModel, seeded.inputToken)
	}
}
