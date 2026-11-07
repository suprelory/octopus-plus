package openai

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestResponseTransformSourceEventPrefersEnvelopeType(t *testing.T) {
	outbound := &ResponseOutbound{}
	events, err := outbound.TransformSourceEvent(context.Background(), model.SourceEvent{
		Type:      "response.created",
		Data:      []byte(`{"type":"response.output_text.delta","delta":"must-not-leak","response":{"id":"resp_1","model":"gpt-test"}}`),
		ID:        "wire-1",
		Sequence:  3,
		Transport: model.SourceTransportSSE,
	})
	if err != nil {
		t.Fatalf("TransformSourceEvent: %v", err)
	}
	if len(events) != 3 || events[0].Kind != model.StreamEventKindResponseStart || events[1].Kind != model.StreamEventKindMessageMetadata || events[2].Kind != model.StreamEventKindMessageStart {
		t.Fatalf("events = %+v, want response lifecycle without text", events)
	}
}

func TestResponseTransformSourceEventAcceptsTypedLifecycleWithoutJSON(t *testing.T) {
	outbound := &ResponseOutbound{}
	events, err := outbound.TransformSourceEvent(context.Background(), model.SourceEvent{
		Type: "response.completed",
		Data: []byte("not-json"),
	})
	if err != nil {
		t.Fatalf("TransformSourceEvent: %v", err)
	}
	if len(events) != 2 || events[0].Kind != model.StreamEventKindResponseStop || events[1].Kind != model.StreamEventKindDone {
		t.Fatalf("events = %+v, want done", events)
	}
}

func eventsOfKind(events []model.StreamEvent, kind model.StreamEventKind) []model.StreamEvent {
	var found []model.StreamEvent
	for _, event := range events {
		if event.Kind == kind {
			found = append(found, event)
		}
	}
	return found
}

func TestChatTransformSourceEventAcceptsTypedDoneWithoutJSON(t *testing.T) {
	outbound := &ChatOutbound{}
	events, err := outbound.TransformSourceEvent(context.Background(), model.SourceEvent{
		Type:      "done",
		Data:      []byte("not-json"),
		Transport: model.SourceTransportSSE,
	})
	if err != nil {
		t.Fatalf("TransformSourceEvent: %v", err)
	}
	if len(events) != 1 || events[0].Kind != model.StreamEventKindDone {
		t.Fatalf("events = %+v, want done", events)
	}
}

func TestChatByteStreamEventWrapsSourceEventConversion(t *testing.T) {
	outbound := &ChatOutbound{}
	data := []byte(`{"id":"chat_1","choices":[{"index":0,"delta":{"content":"hello"}}]}`)
	want, err := outbound.TransformSourceEvent(context.Background(), model.SourceEvent{Data: data})
	if err != nil {
		t.Fatalf("TransformSourceEvent: %v", err)
	}
	got, err := outbound.TransformStreamEvent(context.Background(), data)
	if err != nil {
		t.Fatalf("TransformStreamEvent: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("byte wrapper = %+v, want %+v", got, want)
	}
}

func TestChatInspectorPreservesProviderErrorWithInvalidChoices(t *testing.T) {
	adapter := &ChatOutbound{}
	source, preview, err := adapter.InspectSourceEvent(context.Background(), model.SourceEvent{Data: []byte(`{"choices":{},"error":{"code":"rate_limit","type":"requests","message":"busy"}}`)})
	if err != nil || !preview.Terminal || preview.Semantic {
		t.Fatalf("error preview = %+v, %v", preview, err)
	}
	_, err = adapter.TransformSourceEvent(context.Background(), source)
	var failure *model.ResponseError
	if !errors.As(err, &failure) || failure.Detail.Code != "rate_limit" || failure.Detail.Message != "busy" {
		t.Fatalf("provider error was replaced by a choice decoding error: %v", err)
	}
}
