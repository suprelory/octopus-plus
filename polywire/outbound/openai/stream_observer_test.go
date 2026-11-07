package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestResponsesTerminalObservationMatchesCanonicalOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		reason     model.FinishReason
		code       string
		failed     bool
	}{
		{name: "done", wire: `{"type":"response.done","response":{"status":"completed"}}`, reason: model.FinishReasonStop},
		{name: "done incomplete", wire: `{"type":"response.done","response":{"status":"incomplete"}}`, reason: model.FinishReasonLength},
		{name: "cancelled", wire: `{"type":"response.cancelled"}`, failed: true},
		{name: "canceled", wire: `{"type":"response.canceled"}`, failed: true},
		{name: "done failed", wire: `{"type":"response.done","response":{"status":"failed","error":{"code":"rate_limit","message":"retry later"}}}`, failed: true, code: "rate_limit"},
		{name: "nested error", wire: `{"type":"error","error":{"code":"invalid_request","message":"invalid"}}`, failed: true, code: "invalid_request"},
		{name: "numeric error", wire: `{"type":"response.error","code":429,"message":"retry"}`, failed: true, code: "429"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observation, err := InspectResponseEvent([]byte(tc.wire), time.Now())
			if err != nil || !observation.Terminal || (observation.Error != nil) != tc.failed {
				t.Fatalf("observation = %+v, err = %v", observation, err)
			}
			events, err := (&ResponseOutbound{}).TransformSourceEvent(context.Background(), model.SourceEvent{Data: []byte(tc.wire)})
			if err != nil {
				t.Fatal(err)
			}
			var stop model.FinishReason
			var failure *model.ResponseError
			for _, event := range events {
				if event.Kind == model.StreamEventKindMessageStop {
					stop = event.StopReason
				}
				if event.Kind == model.StreamEventKindError {
					failure = event.Error
				}
			}
			if (failure != nil) != tc.failed || (!tc.failed && stop != tc.reason) {
				t.Fatalf("canonical = %+v, stop = %s, error = %v", events, stop, failure)
			}
			if tc.code != "" && (failure.Detail.Code != tc.code || observation.Error.Code != tc.code) {
				t.Fatalf("error codes disagree: %v / %v", failure, observation.Error)
			}
		})
	}
}

func TestResponseObservationSharesImmutableDTOAndRawOutput(t *testing.T) {
	const output = `[ {"type":"computer_call", "id":"computer-1", "action":{"x":0,"type":"click"}, "future":null} ]`
	wire := []byte(`{"type":"response.completed","event_id":"evt-1","sequence_number":0,"response":{"id":"resp-1","model":"upstream","status":"completed","output":` + output + `,"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`)
	observation, err := InspectResponseEvent(wire, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if observation.Source.Decoded == nil || observation.Source.ID != "evt-1" || observation.Source.Type != "response.completed" || observation.ResponseID != "resp-1" || observation.Usage.TotalTokens != 5 || string(observation.RawOutput) != output {
		t.Fatalf("observation lost metadata or raw output: %+v", observation)
	}
	again, err := InspectResponseSourceEvent(observation.Source, time.Now())
	if err != nil || again.Source.Decoded != observation.Source.Decoded {
		t.Fatalf("native observation reparsed its cached source: %v", err)
	}
	got, err := (&ResponseOutbound{}).TransformSourceEvent(context.Background(), observation.Source)
	if err != nil {
		t.Fatal(err)
	}
	uncached := observation.Source
	uncached.Decoded = nil
	want, err := (&ResponseOutbound{}).TransformSourceEvent(context.Background(), uncached)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("observed conversion differs: %+v / %+v, %v", got, want, err)
	}
	var recovered json.RawMessage
	for _, event := range got {
		if ext := event.ProviderExtensions; ext != nil && ext.OpenAI != nil {
			recovered = ext.OpenAI.RawResponseItems
		}
	}
	if string(recovered) != output {
		t.Fatalf("canonical conversion rewrote original output: %s", recovered)
	}
	rewritten := observation.RewriteModel("upstream", "client-model")
	if !bytes.Contains(rewritten, []byte(`"model":"client-model"`)) || !bytes.Contains(rewritten, []byte(`"future":null`)) || !bytes.Equal(observation.Source.Data, wire) {
		t.Fatalf("model rewrite lost native fields or mutated source: %s", rewritten)
	}
}

func TestResponseObservationPreservesErrorAndRetryPrecedence(t *testing.T) {
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, wire string
		seconds    int
	}{
		{"top level", `{"type":"response.error","status":429,"code":429,"message":"busy","retry_after":2}`, 2},
		{"response", `{"type":"response.failed","status":429,"retry_after":2,"response":{"status":"failed","retry_after":4,"error":{"code":429,"message":"busy"}}}`, 4},
		{"detail", `{"type":"response.failed","status":429,"retry_after":2,"response":{"status":"failed","retry_after":4,"error":{"code":429,"message":"busy","retry_at":"2026-09-11T00:00:07Z","retry_after":6}}}`, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observation, err := InspectResponseEvent([]byte(tc.wire), now)
			if err != nil || observation.Error == nil {
				t.Fatalf("missing error: %+v, %v", observation, err)
			}
			if !observation.Terminal || observation.Error.Status != 429 || observation.Error.Code != "429" || observation.Error.Message != "busy" || !observation.Error.RetryAt.Equal(now.Add(time.Duration(tc.seconds)*time.Second)) {
				t.Fatalf("error or retry precedence changed: %+v", observation.Error)
			}
		})
	}
}

func TestResponseObservationSkipsUnneededModelRewrite(t *testing.T) {
	for _, wire := range []string{
		`{ "type": "response.output_text.delta", "delta": "x" }`,
		`{ "type": "response.created", "model": "different", "response": {"model":"different"} }`,
	} {
		observation, err := InspectResponseEvent([]byte(wire), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		out := observation.RewriteModel("upstream", "client")
		if &out[0] != &observation.Source.Data[0] || string(out) != wire {
			t.Fatalf("unrelated frame was rewritten: %s", out)
		}
	}
}
