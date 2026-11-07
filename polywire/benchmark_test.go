package polywire_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/inbound/anthropic"
	"github.com/suprelory/octopus-plus/polywire/inbound/openai"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound/gemini"
	openaiOutbound "github.com/suprelory/octopus-plus/polywire/outbound/openai"
)

func BenchmarkCrossProtocolTransform(b *testing.B) {
	tests := []struct {
		name        string
		body        []byte
		newInbound  func() model.Inbound
		newOutbound func() model.Outbound
	}{
		{
			name:        "OpenAIChatToGemini",
			body:        []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],"stream":false}`),
			newInbound:  func() model.Inbound { return &openai.ChatInbound{} },
			newOutbound: func() model.Outbound { return &gemini.MessagesOutbound{} },
		},
		{
			name:        "AnthropicToOpenAIResponses",
			body:        []byte(`{"model":"m","max_tokens":128,"messages":[{"role":"user","content":"hello"}],"stream":false}`),
			newInbound:  func() model.Inbound { return &anthropic.MessagesInbound{} },
			newOutbound: func() model.Outbound { return &openaiOutbound.ResponseOutbound{} },
		},
		{
			name:        "LargeOpenAIResponsesToGemini",
			body:        []byte(`{"model":"m","input":"` + strings.Repeat("x", 1<<20) + `","stream":false}`),
			newInbound:  func() model.Inbound { return &openai.ResponseInbound{} },
			newOutbound: func() model.Outbound { return &gemini.MessagesOutbound{} },
		},
	}
	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(test.body)))
			ctx := context.Background()
			for range b.N {
				request, err := test.newInbound().TransformRequest(ctx, test.body)
				if err != nil {
					b.Fatal(err)
				}
				if err := request.Validate(); err != nil {
					b.Fatal(err)
				}
				httpRequest, err := test.newOutbound().TransformRequest(ctx, request, "https://example.com/v1", "key")
				if err != nil {
					b.Fatal(err)
				}
				if _, err := io.Copy(io.Discard, httpRequest.Body); err != nil {
					b.Fatal(err)
				}
				httpRequest.Body.Close()
			}
		})
	}
}

func BenchmarkResponsesRawPassthrough(b *testing.B) {
	tests := []struct {
		name string
		body []byte
	}{
		{"Small", []byte(`{"model":"client","input":"hello","stream":false}`)},
		{"LargePayload", []byte(`{"model":"client","input":"` + strings.Repeat("x", 1<<20) + `","stream":false}`)},
	}
	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(test.body)))
			adapter := &openaiOutbound.ResponseOutbound{}
			for range b.N {
				httpRequest, err := adapter.TransformRequestRaw(context.Background(), test.body, "upstream", "https://example.com/v1", "key", nil)
				if err != nil {
					b.Fatal(err)
				}
				httpRequest.Body.Close()
			}
		})
	}
}
