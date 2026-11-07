package outbound

import (
	"context"
	"errors"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestProtocolTerminalPolicies(t *testing.T) {
	for typ, descriptor := range protocolDescriptors {
		policy, ok := TerminalPolicy(typ)
		if !ok || policy.DefaultFinishReason.IsZero() || policy.CleanEOFCompletes {
			t.Fatalf("invalid policy for %s: %+v", descriptor.Name, policy)
		}
		if typ != OutboundTypeOpenAIEmbedding && (len(policy.TerminalEvents) == 0 || len(policy.RequiredLifecycleEvents) == 0) {
			t.Fatalf("missing lifecycle declaration for %s", descriptor.Name)
		}
	}
	if policy, ok := TerminalPolicy(99); ok || policy.CleanEOFCompletes {
		t.Fatalf("unknown protocol policy = %+v, %v", policy, ok)
	}
}

func TestGeminiCandidateStopDoesNotCompleteOtherCandidates(t *testing.T) {
	adapter := Get(OutboundTypeGemini)
	policy, _ := TerminalPolicy(OutboundTypeGemini)
	f := model.NewStreamFinalizer(policy)
	for _, data := range []string{
		`{"candidates":[{"index":0,"content":{"parts":[{"text":"a"}]}},{"index":1,"content":{"parts":[{"text":"b"}]}}]}`,
		`{"candidates":[{"index":0,"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":2}}`,
		``,
	} {
		events, err := adapter.TransformSourceEvent(context.Background(), model.SourceEvent{Data: []byte(data)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.ProcessStreamEvents(events); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.FinalizeStream(); !errors.Is(err, model.ErrStreamIncomplete) {
		t.Fatalf("missing candidate stop accepted: %v", err)
	}
}

func TestEmptyProviderErrorCannotBecomeCompletion(t *testing.T) {
	for _, tt := range []struct {
		typ   OutboundType
		event string
	}{
		{OutboundTypeAnthropic, "error"},
		{OutboundTypeOpenAIResponse, "error"},
		{OutboundTypeOpenAIResponse, "response.failed"},
	} {
		t.Run(tt.typ.String()+"/"+tt.event, func(t *testing.T) {
			adapter := Get(tt.typ)
			events, err := adapter.TransformSourceEvent(context.Background(), model.SourceEvent{Type: tt.event})
			if err == nil {
				_, err = model.NewStreamFinalizer().ProcessStreamEvents(events)
			}
			var providerError *model.ResponseError
			if !errors.As(err, &providerError) {
				t.Fatalf("empty provider error = %v, events = %+v", err, events)
			}
		})
	}
}

func TestMalformedDonePayloadIsNotATerminalMarker(t *testing.T) {
	for _, typ := range []OutboundType{OutboundTypeOpenAIChat, OutboundTypeOpenAIResponse, OutboundTypeAnthropic, OutboundTypeGemini} {
		if events, err := Get(typ).TransformSourceEvent(context.Background(), model.SourceEvent{Data: []byte("[DONE]garbage")}); err == nil {
			t.Fatalf("%s accepted malformed marker: %+v", typ, events)
		}
	}
}
