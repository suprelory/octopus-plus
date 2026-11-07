package polywire_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestNativeRequestRecoveryPreservesOrRejectsEveryRequiredSidecar(t *testing.T) {
	ctx := context.Background()
	req, err := inbound.Get(inbound.InboundTypeOpenAIResponse).TransformRequest(ctx, []byte(`{"model":"gpt-5","input":[{"type":"computer_call_output","call_id":"call_1","output":"ok","future_item":{"v":1}}],"tools":[{"type":"web_search","future_tool":{"v":2}}],"future_provider":{"v":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, exact := range []bool{false, true} {
		request := req.Clone()
		if exact {
			request.MarkOpenAIExactReplayRequest()
		}
		decision := outbound.PlanRequestForModel(request, request.Model, outbound.OutboundTypeOpenAIResponse, false)
		if decision.Rejected() || decision.Conversion.Mode != model.ConversionRawSidecar || !decision.Conversion.RawInputPreserved || !decision.Conversion.ReplayAvailable || decision.Conversion.ExactReplay != exact {
			t.Fatalf("conversion = %+v", decision)
		}
		wire, _, err := outbound.BuildRequest(ctx, outbound.Get(outbound.OutboundTypeOpenAIResponse), outbound.OutboundTypeOpenAIResponse, request, "https://example.invalid", "test")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(wire.Body)
		wire.Body.Close()
		var payload map[string]json.RawMessage
		if json.Unmarshal(body, &payload) != nil || string(payload["future_provider"]) != `{"v":3}` {
			t.Fatalf("native fields lost: %s", body)
		}
		for _, field := range []string{"input", "tools"} {
			if len(payload[field]) == 0 {
				t.Fatalf("missing %s", field)
			}
		}
	}
	for _, edit := range []func(*model.InternalLLMRequest){
		func(r *model.InternalLLMRequest) {
			r.SetOpenAIRawInputItems(json.RawMessage(`[{"type":"message","role":"user","content":"wrong input"}]`))
		},
		func(r *model.InternalLLMRequest) {
			options := r.GetOpenAIResponsesOptions()
			options.RawTools = nil
			r.SetOpenAIResponsesOptions(options)
		},
		func(r *model.InternalLLMRequest) { delete(r.Operation.Recovery.Fields, "future_provider") },
	} {
		broken := req.Clone()
		edit(broken)
		decision := outbound.PlanRequestForModel(broken, broken.Model, outbound.OutboundTypeOpenAIResponse, false)
		if !decision.Rejected() || decision.Conversion.ReplayAvailable {
			t.Fatalf("missing native data accepted: %+v", decision)
		}
		if _, err := outbound.Get(outbound.OutboundTypeOpenAIResponse).TransformRequest(ctx, broken, "https://example.invalid", "test"); err == nil {
			t.Fatal("builder accepted a missing sidecar")
		}
	}
	if decision := outbound.PlanRequestForModel(req, req.Model, outbound.OutboundTypeGemini, false); decision.Status != outbound.CapabilityDegraded || decision.Conversion.Mode != model.ConversionLossyCanonical {
		t.Fatalf("cross-provider native fallback = %+v", decision)
	}
}

func TestUnknownTopLevelRecoveryCanBeDroppedAcrossProtocols(t *testing.T) {
	ctx := context.Background()
	request, err := inbound.Get(inbound.InboundTypeAnthropic).TransformRequest(ctx, []byte(`{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"future_provider":{"v":1}}`))
	if err != nil {
		t.Fatal(err)
	}

	decision := outbound.PlanRequestForModel(request, request.Model, outbound.OutboundTypeOpenAIChat, false)
	if decision.Rejected() || decision.Status != outbound.CapabilityDegraded {
		t.Fatalf("unknown top-level fallback decision = %#v", decision)
	}
	if len(decision.Losses) != 1 || !decision.Losses[0].IsUnknownTopLevelFieldDrop() {
		t.Fatalf("unknown field loss was not classified separately: %#v", decision.Losses)
	}

	wire, report, err := outbound.BuildRequest(ctx, outbound.Get(outbound.OutboundTypeOpenAIChat), outbound.OutboundTypeOpenAIChat, request, "https://example.invalid", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Body.Close()
	if len(report) != 1 || !report[0].IsUnknownTopLevelFieldDrop() {
		t.Fatalf("wire report = %#v", report)
	}
	var payload map[string]json.RawMessage
	body, _ := io.ReadAll(wire.Body)
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["future_provider"]; ok {
		t.Fatalf("unknown field unexpectedly reached OpenAI Chat: %s", body)
	}
}

func TestUnknownTopLevelAndNativeResponsesLossesCanFallBackTogether(t *testing.T) {
	ctx := context.Background()
	request, err := inbound.Get(inbound.InboundTypeOpenAIResponse).TransformRequest(ctx, []byte(`{"model":"gpt-5","input":[{"type":"computer_call_output","call_id":"call_1","output":"ok"}],"future_provider":{"v":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.MarkOpenAIResponsesPassthroughRequired("input:computer_call_output")
	decision := outbound.PlanRequestForModel(request, request.Model, outbound.OutboundTypeGemini, false)
	if decision.Status != outbound.CapabilityDegraded || len(decision.Losses) != 2 {
		t.Fatalf("mixed availability fallback = %#v", decision)
	}
	for _, loss := range decision.Losses {
		if !loss.IsAvailabilityFallbackLoss() {
			t.Fatalf("unclassified fallback loss: %+v", loss)
		}
	}
}

func TestResponsesUnknownContainerIsNotAnthropicNativeState(t *testing.T) {
	request, err := inbound.Get(inbound.InboundTypeOpenAIResponse).TransformRequest(context.Background(), []byte(`{"model":"m","input":"hello","container":{"id":"future_1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	native := outbound.PlanRequestForModel(request, request.Model, outbound.OutboundTypeOpenAIResponse, false)
	if native.Status != outbound.CapabilitySupported {
		t.Fatalf("native Responses field was treated as Anthropic state: %+v", native)
	}
	fallback := outbound.PlanRequestForModel(request, request.Model, outbound.OutboundTypeOpenAIChat, false)
	if fallback.Status != outbound.CapabilityDegraded || len(fallback.Losses) != 1 || !fallback.Losses[0].IsUnknownTopLevelFieldDrop() || fallback.Losses[0].NativeSemantic {
		t.Fatalf("unknown field incorrectly classified: %+v", fallback)
	}
}

func TestNativeSemanticsPreferRecoveryButCanBuildAcrossProtocols(t *testing.T) {
	tests := []struct {
		name           string
		inbound        inbound.InboundType
		native         outbound.OutboundType
		body           string
		omitted        []string
		keepToolChoice bool
	}{
		{
			name:    "Responses native tools and input",
			inbound: inbound.InboundTypeOpenAIResponse, native: outbound.OutboundTypeOpenAIResponse,
			body:    `{"model":"m","input":[{"role":"user","content":"retained prompt"},{"type":"computer_call_output","call_id":"call_1","output":"ok","future_input":true}],"tools":[{"type":"web_search","future_tool":true}]}`,
			omitted: []string{"computer_call_output", "future_input", "future_tool", "web_search"},
		},
		{
			name:    "Responses native fields on canonical items",
			inbound: inbound.InboundTypeOpenAIResponse, native: outbound.OutboundTypeOpenAIResponse,
			body:    `{"model":"m","input":[{"role":"user","content":"retained prompt","future_input":true}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}},"future_tool":true}]}`,
			omitted: []string{"future_input", "future_tool"},
		},
		{
			name:    "Responses forced native tool",
			inbound: inbound.InboundTypeOpenAIResponse, native: outbound.OutboundTypeOpenAIResponse,
			body:    `{"model":"m","input":"retained prompt","tools":[{"type":"web_search"}],"tool_choice":{"type":"web_search"}}`,
			omitted: []string{"web_search", "tool_choice", "toolConfig"},
		},
		{
			name:    "Responses required native tools",
			inbound: inbound.InboundTypeOpenAIResponse, native: outbound.OutboundTypeOpenAIResponse,
			body:    `{"model":"m","input":"retained prompt","tools":[{"type":"web_search"}],"tool_choice":"required"}`,
			omitted: []string{"web_search", "tool_choice", "toolConfig"},
		},
		{
			name:    "Responses function choice survives native tool fallback",
			inbound: inbound.InboundTypeOpenAIResponse, native: outbound.OutboundTypeOpenAIResponse,
			body:    `{"model":"m","input":"retained prompt","tools":[{"type":"web_search"},{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}],"tool_choice":{"type":"function","name":"lookup"}}`,
			omitted: []string{"web_search"}, keepToolChoice: true,
		},
		{
			name:    "Anthropic MCP and container",
			inbound: inbound.InboundTypeAnthropic, native: outbound.OutboundTypeAnthropic,
			body:    `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"retained prompt"}],"mcp_servers":[{"type":"url","name":"lookup","url":"https://example.invalid/mcp"}],"container":{"id":"container_1"}}`,
			omitted: []string{"mcp_servers", "container_1"},
		},
		{
			name:    "Anthropic server tools",
			inbound: inbound.InboundTypeAnthropic, native: outbound.OutboundTypeAnthropic,
			body:    `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"retained prompt"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":3}]}`,
			omitted: []string{"web_search_20250305", "max_uses"},
		},
		{
			name:    "Anthropic forced native tool",
			inbound: inbound.InboundTypeAnthropic, native: outbound.OutboundTypeAnthropic,
			body:    `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"retained prompt"}],"tools":[{"type":"web_search_20250305","name":"web_search"}],"tool_choice":{"type":"tool","name":"web_search"}}`,
			omitted: []string{"web_search", "tool_choice", "toolConfig"},
		},
		{
			name:    "Anthropic server tool history",
			inbound: inbound.InboundTypeAnthropic, native: outbound.OutboundTypeAnthropic,
			body:    `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"retained prompt"},{"role":"assistant","content":[{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"example"}},{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[]}]},{"role":"user","content":"continue"}]}`,
			omitted: []string{"server_tool_use", "web_search_tool_result"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			request, err := inbound.Get(tt.inbound).TransformRequest(ctx, []byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			before := request.Clone()
			preserving := outbound.PlanRequestForModel(request, request.Model, tt.native, false)
			if preserving.Status != outbound.CapabilitySupported {
				t.Fatalf("native recovery = %+v", preserving)
			}
			for _, target := range []outbound.OutboundType{outbound.OutboundTypeOpenAIChat, outbound.OutboundTypeOpenAIResponse, outbound.OutboundTypeAnthropic, outbound.OutboundTypeGemini} {
				if target == tt.native {
					continue
				}
				t.Run(target.String(), func(t *testing.T) {
					decision := outbound.PlanRequestForModel(request, request.Model, target, false)
					if decision.Status != outbound.CapabilityDegraded || decision.Conversion.Mode != model.ConversionLossyCanonical || decision.Conversion.ReplayAvailable || decision.Conversion.ExactReplay {
						t.Fatalf("fallback conversion = %+v", decision)
					}
					for _, loss := range decision.Losses {
						if !loss.IsNativeSemanticLoss() {
							t.Fatalf("native loss was not classified: %+v", loss)
						}
					}
					wire, report, err := outbound.BuildRequest(ctx, outbound.Get(target), target, request, "https://example.invalid", "test")
					if err != nil {
						t.Fatal(err)
					}
					defer wire.Body.Close()
					body, err := io.ReadAll(wire.Body)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(report, decision.ConversionReport) {
						t.Fatalf("planner and builder reports differ: planner=%+v builder=%+v", decision.ConversionReport, report)
					}
					if !json.Valid(body) || !bytes.Contains(body, []byte("retained prompt")) {
						t.Fatalf("canonical prompt lost: %s", body)
					}
					if tt.keepToolChoice {
						var payload map[string]json.RawMessage
						if err := json.Unmarshal(body, &payload); err != nil {
							t.Fatal(err)
						}
						field := "tool_choice"
						if target == outbound.OutboundTypeGemini {
							field = "toolConfig"
						}
						if !bytes.Contains(payload[field], []byte("lookup")) {
							t.Fatalf("canonical function choice lost: %s", body)
						}
					}
					for _, field := range tt.omitted {
						if bytes.Contains(body, []byte(field)) {
							t.Fatalf("native field %q leaked to %s: %s", field, target, body)
						}
					}
				})
			}
			if !reflect.DeepEqual(request, before) {
				t.Fatal("fallback planning or construction mutated the native request")
			}
		})
	}
}

func TestAnthropicNativeRecoveryStillRequiresValidSidecars(t *testing.T) {
	ctx := context.Background()
	request, err := inbound.Get(inbound.InboundTypeAnthropic).TransformRequest(ctx, []byte(`{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"mcp_servers":[{"type":"url","name":"lookup","url":"https://example.invalid/mcp"}],"container":{"id":"container_1"},"tools":[{"type":"web_search_20250305","name":"web_search"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"mcp_servers", "container", "tools"} {
		t.Run(field, func(t *testing.T) {
			broken := request.Clone()
			switch field {
			case "mcp_servers":
				broken.ProviderExtensions.Anthropic.MCPServers = nil
			case "container":
				broken.ProviderExtensions.Anthropic.Container = json.RawMessage(`{`)
			case "tools":
				broken.Tools[0].AnthropicServerSpec = nil
			}
			decision := outbound.PlanRequestForModel(broken, broken.Model, outbound.OutboundTypeAnthropic, false)
			if !decision.Rejected() || decision.Conversion.ReplayAvailable {
				t.Fatalf("invalid native sidecar accepted: %+v", decision)
			}
			if _, err := outbound.Get(outbound.OutboundTypeAnthropic).TransformRequest(ctx, broken, "https://example.invalid", "test"); err == nil {
				t.Fatal("native builder accepted an invalid sidecar")
			}
		})
	}
}

func TestConversionModesDistinguishCanonicalRawAndLossy(t *testing.T) {
	ctx := context.Background()
	req, err := inbound.Get(inbound.InboundTypeOpenAIChat).TransformRequest(ctx, []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	decision := outbound.PlanRequestForModel(req, req.Model, outbound.OutboundTypeOpenAIChat, false)
	if decision.Conversion.Mode != model.ConversionLosslessCanonical || decision.Conversion.RawInputPreserved {
		t.Fatalf("canonical = %+v", decision)
	}
	topK := int64(5)
	req.TopK = &topK
	decision = outbound.PlanRequestForModel(req, req.Model, outbound.OutboundTypeOpenAIChat, false)
	if decision.Conversion.Mode != model.ConversionLossyCanonical || decision.Conversion.ReplayAvailable {
		t.Fatalf("lossy = %+v", decision)
	}
	request, err := inbound.Get(inbound.InboundTypeAnthropic).TransformRequest(ctx, []byte(`{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"future_provider":{"v":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	decision = outbound.PlanRequestForModel(request, request.Model, outbound.OutboundTypeAnthropic, true)
	if decision.Conversion.Mode != model.ConversionRawSidecar || !decision.Conversion.RawInputPreserved {
		t.Fatalf("raw = %+v", decision)
	}
	if _, err := outbound.Get(outbound.OutboundTypeAnthropic).TransformRequest(ctx, request, "https://example.invalid", "test"); err != nil {
		t.Fatal(err)
	}
	delete(request.Operation.Recovery.Fields, "future_provider")
	if _, err := outbound.Get(outbound.OutboundTypeAnthropic).TransformRequest(ctx, request, "https://example.invalid", "test"); err == nil {
		t.Fatal("missing provider extension accepted")
	}
}
