package polywire_test

import (
	"context"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/inbound/anthropic"
	"github.com/suprelory/octopus-plus/polywire/inbound/openai"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestRegisteredInboundTransformersCaptureTopLevelPresence(t *testing.T) {
	tests := []struct {
		name    string
		inbound model.Inbound
		body    string
		null    string
		empty   string
		absent  string
	}{
		{
			name:    "openai chat",
			inbound: &openai.ChatInbound{},
			body:    `{"model":"m","messages":[{"role":"user","content":"hi"}],"temperature":null,"metadata":{}}`,
			null:    "temperature",
			empty:   "metadata",
			absent:  "top_p",
		},
		{
			name:    "openai responses",
			inbound: &openai.ResponseInbound{},
			body:    `{"model":"m","input":"hi","background":null,"metadata":{}}`,
			null:    "background",
			empty:   "metadata",
			absent:  "temperature",
		},
		{
			name:    "openai embeddings",
			inbound: &openai.EmbeddingInbound{},
			body:    `{"model":"m","input":"hi","dimensions":null,"user":""}`,
			null:    "dimensions",
			empty:   "user",
			absent:  "encoding_format",
		},
		{
			name:    "anthropic messages",
			inbound: &anthropic.MessagesInbound{},
			body:    `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"temperature":null,"metadata":{}}`,
			null:    "temperature",
			empty:   "metadata",
			absent:  "top_p",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := test.inbound.TransformRequest(context.Background(), []byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			if got := request.FieldPresenceOf(test.null); got != model.FieldExplicitNull {
				t.Fatalf("%s presence = %d, want explicit null", test.null, got)
			}
			if got := request.FieldPresenceOf(test.empty); got != model.FieldPresent {
				t.Fatalf("%s presence = %d, want present", test.empty, got)
			}
			if got := request.FieldPresenceOf(test.absent); got != model.FieldAbsent {
				t.Fatalf("%s presence = %d, want absent", test.absent, got)
			}
		})
	}
}
