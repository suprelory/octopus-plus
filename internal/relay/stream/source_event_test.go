package stream

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestSSESourcePreservesEventType(t *testing.T) {
	source := NewSSESource(io.NopCloser(strings.NewReader("id: evt-7\nevent: message_stop\ndata:\n\n")), 0)
	defer source.Close()

	event, err := source.ReadSourceEvent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != "message_stop" || len(event.Data) != 0 {
		t.Fatalf("event = %#v, want message_stop with empty data", event)
	}
	if event.ID != "evt-7" || event.Sequence != 1 || event.Transport != SourceTransportSSE {
		t.Fatalf("event metadata = %#v, want id/sequence/transport", event)
	}
}

func TestStreamProcessorPassesSourceEventToTransform(t *testing.T) {
	source := NewSSESource(io.NopCloser(strings.NewReader("id: evt-3\nevent: response.completed\ndata:\n\n")), 0)
	defer source.Close()
	writer := newMockStreamWriter()
	var got SourceEvent
	processor := NewStreamProcessor(StreamConfig{
		Source:  source,
		Writer:  writer,
		Context: context.Background(),
		TransformEvent: func(_ context.Context, event SourceEvent) ([]byte, error) {
			got = event
			return []byte("event"), nil
		},
	})

	if err := processor.Run(); err != nil {
		t.Fatal(err)
	}
	if got.Type != "response.completed" || got.ID != "evt-3" || got.Sequence != 1 || got.Transport != SourceTransportSSE {
		t.Fatalf("source event = %#v, want complete metadata", got)
	}
}

func TestStreamProcessorDoesNotRewriteEventDataForEventTransform(t *testing.T) {
	const payload = `{"type":"payload-type"}`
	source := NewSSESource(io.NopCloser(strings.NewReader("event: envelope-type\ndata: "+payload+"\n\n")), 0)
	defer source.Close()
	writer := newMockStreamWriter()
	var got SourceEvent
	processor := NewStreamProcessor(StreamConfig{
		Source:  source,
		Writer:  writer,
		Context: context.Background(),
		TransformEvent: func(_ context.Context, event SourceEvent) ([]byte, error) {
			got = event
			return []byte("event"), nil
		},
	})

	if err := processor.Run(); err != nil {
		t.Fatal(err)
	}
	if got.Type != "envelope-type" || string(got.Data) != payload {
		t.Fatalf("source event = %#v, want separate unchanged type and data", got)
	}
}
