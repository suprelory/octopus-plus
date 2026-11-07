package polywire_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestChatUnknownFieldsPreservedNativelyAndReportedAcrossProtocols(t *testing.T) {
	ctx := context.Background()
	req, err := inbound.Get(inbound.InboundTypeOpenAIChat).TransformRequest(ctx, []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],"vendor_hint":{"integer":9007199254740993},"vendor_null":null}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []outbound.OutboundType{outbound.OutboundTypeOpenAIChat, outbound.OutboundTypeOpenAIResponse, outbound.OutboundTypeAnthropic, outbound.OutboundTypeGemini} {
		t.Run(typ.String(), func(t *testing.T) {
			decision := outbound.PlanRequestForModel(req, "m", typ, false)
			wire, report, err := outbound.BuildRequest(ctx, outbound.Get(typ), typ, req, "https://example.invalid/v1", "test")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(wire.Body)
			wire.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			if typ == outbound.OutboundTypeOpenAIChat {
				if string(payload["vendor_hint"]) != `{"integer":9007199254740993}` || string(payload["vendor_null"]) != "null" {
					t.Fatalf("native extension lost: %s", body)
				}
				if decision.Status != outbound.CapabilitySupported || len(report) != 0 {
					t.Fatalf("native extension reported as lossy: %+v, %+v", decision, report)
				}
			} else {
				if payload["vendor_hint"] != nil || payload["vendor_null"] != nil {
					t.Fatalf("foreign extension leaked: %s", body)
				}
				if decision.Status != outbound.CapabilityDegraded || len(report) != 2 {
					t.Fatalf("extension losses unreported: %+v, %+v", decision, report)
				}
				for _, loss := range report {
					if !loss.IsUnknownTopLevelFieldDrop() {
						t.Fatalf("extension loss classified as known semantics: %+v", loss)
					}
				}
			}
		})
	}
}

func TestChatExplicitEmptyFieldsSurviveNativeBuild(t *testing.T) {
	ctx := context.Background()
	req, err := inbound.Get(inbound.InboundTypeOpenAIChat).TransformRequest(ctx, []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],"temperature":null,"tools":[],"metadata":{},"reasoning_effort":""}`))
	if err != nil {
		t.Fatal(err)
	}
	wire, report, err := outbound.BuildRequest(ctx, outbound.Get(outbound.OutboundTypeOpenAIChat), outbound.OutboundTypeOpenAIChat, req, "https://example.invalid/v1", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Body.Close()
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(wire.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{"temperature": "null", "tools": "[]", "metadata": "{}", "reasoning_effort": `""`} {
		if string(payload[field]) != want {
			t.Errorf("%s = %s, want %s", field, payload[field], want)
		}
	}
	if _, exists := payload["top_p"]; exists {
		t.Fatal("absent top_p was synthesized")
	}
	if len(report) != 0 {
		t.Fatalf("native empty values reported as lossy: %+v", report)
	}
}

func TestChatUnsupportedChoiceCountIsRejected(t *testing.T) {
	for _, value := range []string{"2", "0", "-1", "1.5", `"2"`} {
		t.Run(value, func(t *testing.T) {
			_, err := inbound.Get(inbound.InboundTypeOpenAIChat).TransformRequest(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],"n":`+value+`}`))
			if err == nil {
				t.Fatal("unsupported n was silently accepted")
			}
		})
	}
	for _, value := range []string{"1", "null"} {
		t.Run("supported_"+value, func(t *testing.T) {
			req, err := inbound.Get(inbound.InboundTypeOpenAIChat).TransformRequest(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],"n":`+value+`}`))
			if err != nil {
				t.Fatal(err)
			}
			if decision := outbound.PlanRequestForModel(req, "m", outbound.OutboundTypeAnthropic, false); decision.Rejected() {
				t.Fatalf("single-choice request rejected: %+v", decision)
			}
		})
	}
}

func TestChatMissingRecoveryIsRejectedBeforeNativeSubmission(t *testing.T) {
	req, err := inbound.Get(inbound.InboundTypeOpenAIChat).TransformRequest(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],"vendor_hint":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.Operation.Recovery == nil {
		t.Fatal("missing recovery sidecar")
	}
	broken := req.Clone()
	delete(broken.Operation.Recovery.Fields, "vendor_hint")
	if decision := outbound.PlanRequestForModel(broken, "m", outbound.OutboundTypeOpenAIChat, false); !decision.Rejected() {
		t.Fatalf("incomplete recovery accepted: %+v", decision)
	}
	if req.ConversionState(model.APIFormatOpenAIChatCompletion, false, false).Mode != model.ConversionRawSidecar {
		t.Fatal("native recovery not reflected in conversion state")
	}
}

func TestChatExplicitUnsupportedEmptyFieldsAreReported(t *testing.T) {
	for _, test := range []struct {
		field  string
		value  string
		target outbound.OutboundType
	}{
		{"top_k", "null", outbound.OutboundTypeOpenAIChat},
		{"metadata", "{}", outbound.OutboundTypeGemini},
		{"temperature", "null", outbound.OutboundTypeGemini},
	} {
		t.Run(test.field, func(t *testing.T) {
			body := `{"model":"m","messages":[{"role":"user","content":"hello"}],"` + test.field + `":` + test.value + `}`
			req, err := inbound.Get(inbound.InboundTypeOpenAIChat).TransformRequest(context.Background(), []byte(body))
			if err != nil {
				t.Fatal(err)
			}
			decision := outbound.PlanRequestForModel(req, "m", test.target, false)
			if decision.Status != outbound.CapabilityDegraded || len(decision.Losses) != 1 || decision.Losses[0].Field != test.field || decision.Losses[0].IsUnknownTopLevelFieldDrop() {
				t.Fatalf("known empty value was silently dropped or misclassified: %+v", decision)
			}
		})
	}
}

func TestChatEmptyFieldRecoveryHonorsEditsAndAbsentFields(t *testing.T) {
	ctx := context.Background()
	req, err := inbound.Get(inbound.InboundTypeOpenAIChat).TransformRequest(ctx, []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],"temperature":null,"tools":[ ],"metadata":{ }}`))
	if err != nil {
		t.Fatal(err)
	}
	prepared := req.Clone()
	temperature := 0.25
	prepared.Temperature = &temperature
	prepared.Metadata["tag"] = "changed"
	prepared.SetFieldPresence("tools", model.FieldAbsent)
	wire, report, err := outbound.BuildRequest(ctx, outbound.Get(outbound.OutboundTypeOpenAIChat), outbound.OutboundTypeOpenAIChat, prepared, "https://example.invalid/v1", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Body.Close()
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(wire.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if string(payload["temperature"]) != "0.25" || string(payload["metadata"]) != `{"tag":"changed"}` || payload["tools"] != nil || len(report) != 0 {
		t.Fatalf("stale presence overrode a prepared request: %+v, %+v", payload, report)
	}
	if req.FieldPresenceOf("tools") != model.FieldPresent || len(req.EmptyFields["tools"]) == 0 || len(req.Metadata) != 0 {
		t.Fatal("preparing an attempt mutated the original request")
	}
}
