package outbound

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestConversionReportMatchesEmittedRequest(t *testing.T) {
	temperature, topP, topK, budget := 0.4, 0.8, int64(12), int64(0)
	for _, tc := range []struct {
		name    string
		typ     OutboundType
		request *model.InternalLLMRequest
		field   string
		action  LossAction
		check   func(*testing.T, map[string]any)
	}{
		{name: "Anthropic thinking", typ: OutboundTypeAnthropic, request: &model.InternalLLMRequest{Model: "claude-sonnet", ReasoningEffort: "high", Temperature: &temperature, TopP: &topP, TopK: &topK}, field: "temperature", action: LossActionRepair, check: func(t *testing.T, body map[string]any) {
			if body["temperature"] != float64(1) || body["top_p"] != nil || body["top_k"] != nil {
				t.Fatalf("sampling = %v", body)
			}
		}},
		{name: "Anthropic stop limit", typ: OutboundTypeAnthropic, request: &model.InternalLLMRequest{Model: "claude-sonnet", Stop: &model.Stop{MultipleStop: []string{"a", "b", "c", "d", "e"}}}, field: "stop", action: LossActionTruncate, check: func(t *testing.T, body map[string]any) {
			if len(body["stop_sequences"].([]any)) != 4 {
				t.Fatalf("stops = %v", body["stop_sequences"])
			}
		}},
		{name: "Gemini thinking budget", typ: OutboundTypeGemini, request: &model.InternalLLMRequest{Model: "gemini-2.5-pro", ReasoningBudget: &budget}, field: "reasoning_budget", action: LossActionRepair, check: func(t *testing.T, body map[string]any) {
			if fieldAt(body, "generationConfig.thinkingConfig.thinkingBudget") != float64(128) {
				t.Fatalf("thinking = %v", body)
			}
		}},
		{name: "Gemini schema loss", typ: OutboundTypeGemini, request: &model.InternalLLMRequest{Model: "gemini-2.5-flash", Tools: []model.Tool{{Type: "function", Function: model.Function{Name: "lookup", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"q":{"type":"string"}}}`)}}}}, field: "tools[0].function.parameters", action: LossActionTranslate, check: func(t *testing.T, body map[string]any) {
			tool := body["tools"].([]any)[0].(map[string]any)
			schema := tool["functionDeclarations"].([]any)[0].(map[string]any)["parameters"].(map[string]any)
			if schema["additionalProperties"] != nil || schema["type"] != "OBJECT" {
				t.Fatalf("schema = %v", schema)
			}
		}},
		{name: "Gemini modality loss", typ: OutboundTypeGemini, request: &model.InternalLLMRequest{Model: "gemini-2.5-flash", Modalities: []string{"text", "video"}}, field: "modalities", action: LossActionDrop, check: func(t *testing.T, body map[string]any) {
			if !reflect.DeepEqual(fieldAt(body, "generationConfig.responseModalities"), []any{"TEXT"}) {
				t.Fatalf("modalities = %v", body)
			}
		}},
		{name: "Responses sidecar recovery", typ: OutboundTypeOpenAIResponse, request: &model.InternalLLMRequest{Model: "gpt-5", RawInputItems: json.RawMessage(`[{"type":"item_reference","id":"item_native"}]`)}, field: "responses.input", action: LossActionPreserve, check: func(t *testing.T, body map[string]any) {
			if body["input"].([]any)[0].(map[string]any)["id"] != "item_native" {
				t.Fatalf("input = %v", body)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.request.RequestType = model.RequestTypeChat
			before := tc.request.Clone()
			decision := PlanRequestForModel(tc.request, tc.request.Model, tc.typ, false)
			wire, report, err := BuildRequest(context.Background(), Get(tc.typ), tc.typ, tc.request, "https://example.invalid/v1", "test-key")
			if err != nil {
				t.Fatal(err)
			}
			defer wire.Body.Close()
			if !reflect.DeepEqual(report, decision.ConversionReport) {
				t.Fatalf("planner = %+v, build = %+v", decision, report)
			}
			found := false
			for _, change := range report {
				if change.TargetField == "" || change.Condition == "" {
					t.Fatalf("incomplete field rule = %+v", change)
				}
				if change.Field == tc.field && change.Action == tc.action {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s %s: %+v", tc.field, tc.action, report)
			}
			body, _ := io.ReadAll(wire.Body)
			var value map[string]any
			if err := json.Unmarshal(body, &value); err != nil {
				t.Fatal(err)
			}
			tc.check(t, value)
			if !reflect.DeepEqual(before, tc.request) {
				t.Fatal("conversion mutated the source request")
			}
		})
	}
}
