package polywire_test

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/inbound/openai"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestGeminiToolAndResponseSchemasShareConversionAndLosses(t *testing.T) {
	for _, test := range []struct{ name, schema, loss string }{
		{"nullable", `{"type":"object","properties":{"value":{"type":["string","null"]}}}`, ""},
		{"union", `{"type":"object","properties":{"value":{"anyOf":[{"type":"string"},{"type":"integer"}]}}}`, ""},
		{"references", `{"$defs":{"value":{"type":"string"}},"type":"object","properties":{"value":{"$ref":"#/$defs/value"}}}`, ""},
		{"integer bounds", `{"type":"array","maxItems":9007199254740993,"items":{"type":"string"}}`, ""},
		{"raw unknown constraint", `{"type":"object","properties":{"value":{"type":"object","unevaluatedProperties":false}}}`, "unevaluatedProperties"},
		{"recursive", `{"type":"object","properties":{"value":{"$ref":"#"}}}`, "recursive reference"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"model":"gemini-test","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":` + test.schema + `}}],"response_format":{"type":"json_schema","json_schema":{"name":"result","schema":` + test.schema + `}}}`)
			request, err := (&openai.ChatInbound{}).TransformRequest(context.Background(), body)
			if err != nil {
				t.Fatal(err)
			}
			before := request.Clone()
			wire, report, err := outbound.BuildRequest(context.Background(), outbound.Get(outbound.OutboundTypeGemini), outbound.OutboundTypeGemini, request, "https://example.invalid", "test")
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := io.ReadAll(wire.Body)
			wire.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Tools []struct {
					Declarations []struct {
						Parameters map[string]any `json:"parameters"`
					} `json:"functionDeclarations"`
				} `json:"tools"`
				Config struct {
					Schema map[string]any `json:"responseSchema"`
				} `json:"generationConfig"`
			}
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Tools) != 1 || len(payload.Tools[0].Declarations) != 1 || !reflect.DeepEqual(payload.Tools[0].Declarations[0].Parameters, payload.Config.Schema) {
				t.Fatalf("tool and response schema disagree: %s", encoded)
			}
			if test.name == "integer bounds" && strings.Count(string(encoded), "9007199254740993") != 2 {
				t.Fatalf("integer schema bound lost precision: %s", encoded)
			}
			losses := make(map[string]string)
			for _, change := range report {
				if change.IsLossy() {
					losses[change.Field] = change.Reason
				}
			}
			for _, field := range []string{"tools[0].function.parameters", "response_format.schema"} {
				if test.loss == "" && losses[field] != "" || test.loss != "" && !strings.Contains(losses[field], test.loss) {
					t.Fatalf("incorrect schema loss for %s: %+v", field, report)
				}
			}
			if !reflect.DeepEqual(request, before) {
				t.Fatal("conversion mutated the source request")
			}
		})
	}
}
