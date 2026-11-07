package outbound

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestOutboundOperationOnlyMatchesLegacyPayload(t *testing.T) {
	text := "hello"
	messages := []model.Message{{Role: "user", Content: model.MessageContent{Content: &text}}}
	for _, typ := range []OutboundType{OutboundTypeOpenAIChat, OutboundTypeOpenAIResponse, OutboundTypeAnthropic, OutboundTypeGemini, OutboundTypeOpenAIEmbedding} {
		t.Run(typ.String(), func(t *testing.T) {
			legacy := &model.InternalLLMRequest{Model: "model", Messages: messages}
			canonical := &model.InternalLLMRequest{Model: "model", Operation: &model.RequestOperation{Chat: &model.ChatOperation{Messages: model.CloneMessages(messages)}}}
			if typ == OutboundTypeOpenAIEmbedding {
				dimensions := int64(16)
				legacy.Messages, legacy.EmbeddingInput, legacy.EmbeddingDimensions = nil, &model.EmbeddingInput{Single: &text}, &dimensions
				canonical.Operation = &model.RequestOperation{Embeddings: &model.EmbeddingsOperation{Input: *legacy.EmbeddingInput, Dimensions: &dimensions}}
			}
			before := canonical.Clone()
			bodies := make([]any, 0, 2)
			for _, request := range []*model.InternalLLMRequest{legacy, canonical} {
				httpRequest, err := Get(typ).TransformRequest(context.Background(), request, "https://example.com/v1", "key")
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(httpRequest.Body)
				_ = httpRequest.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				var value any
				if err := json.Unmarshal(body, &value); err != nil {
					t.Fatal(err)
				}
				bodies = append(bodies, value)
			}
			if !reflect.DeepEqual(bodies[0], bodies[1]) {
				t.Fatalf("wire mismatch: legacy=%+v operation=%+v", bodies[0], bodies[1])
			}
			if !reflect.DeepEqual(before, canonical) {
				t.Fatal("builder mutated authoritative payload")
			}
		})
	}
}

func TestBuildersAndPlannerRejectConflictingOperation(t *testing.T) {
	canonicalText, legacyText := "canonical", "stale"
	req := &model.InternalLLMRequest{Model: "model", Messages: []model.Message{{Role: "user", Content: model.MessageContent{Content: &legacyText}}}, Operation: &model.RequestOperation{Chat: &model.ChatOperation{Messages: []model.Message{{Role: "user", Content: model.MessageContent{Content: &canonicalText}}}}}}
	for _, typ := range []OutboundType{OutboundTypeOpenAIChat, OutboundTypeOpenAIResponse, OutboundTypeAnthropic, OutboundTypeGemini} {
		if _, err := Get(typ).TransformRequest(context.Background(), req, "https://example.com", "key"); err == nil {
			t.Fatalf("%s accepted conflicting payloads", typ)
		}
		if decision := PlanRequestForModel(req, "model", typ, false); !decision.Rejected() {
			t.Fatalf("%s planner accepted conflicting payloads", typ)
		}
	}
}
