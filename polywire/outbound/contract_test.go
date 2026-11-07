package outbound

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestPlannerSupportedFeaturesReachWireBody(t *testing.T) {
	text := "hello"
	req := &model.InternalLLMRequest{
		RequestType:  model.RequestTypeChat,
		RawAPIFormat: model.APIFormatOpenAIChatCompletion,
		Model:        "gpt-test",
		Messages:     []model.Message{{Role: "user", Content: model.MessageContent{Content: &text}}},
		Tools: []model.Tool{{
			Type:     "function",
			Function: model.Function{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)},
		}},
	}
	decision := PlanRequestForModel(req, req.Model, OutboundTypeOpenAIChat, false)
	if decision.Status != CapabilitySupported {
		t.Fatalf("planner rejected supported function tool: %#v", decision)
	}
	request, err := Get(OutboundTypeOpenAIChat).TransformRequest(context.Background(), req, "https://example.com/v1", "test-key")
	if err != nil {
		t.Fatalf("TransformRequest() error = %v", err)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read wire body: %v", err)
	}
	var payload struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode wire body: %v", err)
	}
	if len(payload.Tools) != 1 {
		t.Fatalf("planner supported tools but wire body has %d tools", len(payload.Tools))
	}
}
