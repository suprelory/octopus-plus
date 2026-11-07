package polywire_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestStructuredOutputStrictRoundTrip(t *testing.T) {
	for _, strict := range []string{"true", "false", "absent"} {
		for _, source := range []inbound.InboundType{inbound.InboundTypeOpenAIChat, inbound.InboundTypeOpenAIResponse} {
			for _, target := range []outbound.OutboundType{outbound.OutboundTypeOpenAIChat, outbound.OutboundTypeOpenAIResponse} {
				t.Run(fmt.Sprintf("%d-to-%d/%s", source, target, strict), func(t *testing.T) {
					format := map[string]any{"name": "result", "description": "structured result", "schema": map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}, "required": []string{"answer"}, "additionalProperties": false}}
					if strict != "absent" {
						format["strict"] = strict == "true"
					}
					body := map[string]any{"model": "test-model"}
					if source == inbound.InboundTypeOpenAIResponse {
						body["input"] = "hello"
						format["type"] = "json_schema"
						body["text"] = map[string]any{"format": format}
					} else {
						body["messages"] = []map[string]any{{"role": "user", "content": "hello"}}
						body["response_format"] = map[string]any{"type": "json_schema", "json_schema": format}
					}
					encoded, _ := json.Marshal(body)
					ctx := context.Background()
					canonical, err := inbound.Get(source).TransformRequest(ctx, encoded)
					if err != nil {
						t.Fatal(err)
					}
					wire, _, err := outbound.BuildRequest(ctx, outbound.Get(target), target, canonical, "https://example.invalid/v1", "test-key")
					if err != nil {
						t.Fatal(err)
					}
					defer wire.Body.Close()
					data, err := io.ReadAll(wire.Body)
					if err != nil {
						t.Fatal(err)
					}
					var result map[string]any
					if err := json.Unmarshal(data, &result); err != nil {
						t.Fatal(err)
					}
					var output map[string]any
					if target == outbound.OutboundTypeOpenAIResponse {
						output = result["text"].(map[string]any)["format"].(map[string]any)
					} else {
						output = result["response_format"].(map[string]any)["json_schema"].(map[string]any)
					}
					value, present := output["strict"]
					if present != (strict != "absent") || (present && value != (strict == "true")) {
						t.Fatalf("strict lost: %s", data)
					}
					if output["name"] != "result" || output["description"] != "structured result" || output["schema"] == nil {
						t.Fatalf("schema wrapper lost: %s", data)
					}
				})
			}
		}
	}
}
