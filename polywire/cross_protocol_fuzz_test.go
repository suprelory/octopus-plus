package polywire_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func FuzzCrossProtocolRequestIsolation(f *testing.F) {
	f.Add([]byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`), uint8(0), uint8(0))
	f.Add([]byte(`{"model":"m","input":"hello"}`), uint8(1), uint8(1))
	f.Add([]byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`), uint8(2), uint8(2))
	f.Add([]byte(`{"model":"m","input":["one","two"]}`), uint8(3), uint8(5))
	f.Fuzz(func(t *testing.T, body []byte, inboundSelector, outboundSelector uint8) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		inboundTypes := []inbound.InboundType{
			inbound.InboundTypeOpenAIChat,
			inbound.InboundTypeOpenAIResponse,
			inbound.InboundTypeAnthropic,
			inbound.InboundTypeOpenAIEmbedding,
		}
		outboundTypes := []outbound.OutboundType{
			outbound.OutboundTypeOpenAIChat,
			outbound.OutboundTypeOpenAIResponse,
			outbound.OutboundTypeAnthropic,
			outbound.OutboundTypeGemini,
			outbound.OutboundTypeOpenAIEmbedding,
		}
		inAdapter := inbound.Get(inboundTypes[int(inboundSelector)%len(inboundTypes)])
		request, err := inAdapter.TransformRequest(context.Background(), body)
		if err != nil || request.Validate() != nil {
			return
		}
		outType := outboundTypes[int(outboundSelector)%len(outboundTypes)]
		decision := outbound.PlanRequestForModel(request, request.Model, outType, false)
		if decision.Rejected() {
			return
		}
		before := request.Clone()
		_, _ = outbound.Get(outType).TransformRequest(context.Background(), request, "https://example.com/v1", "key")
		if !reflect.DeepEqual(before, request) {
			t.Fatalf("%v outbound mutated canonical request", outType)
		}
	})
}
