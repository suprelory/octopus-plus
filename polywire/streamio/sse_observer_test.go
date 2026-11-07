package streamio

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	openai "github.com/suprelory/octopus-plus/polywire/outbound/openai"
)

func TestIncrementalSSEObserverHandlesSplitAndMultilineEvents(t *testing.T) {
	type observed struct {
		typ  string
		data string
	}
	var events []observed
	observer := NewIncrementalSSEObserver(1024, map[string]struct{}{"response.completed": {}}, func(_ context.Context, eventType string, data []byte) error {
		events = append(events, observed{typ: eventType, data: string(data)})
		return nil
	})
	for _, chunk := range [][]byte{
		[]byte("event: response.output_text.delta\r\nda"),
		[]byte("ta: {\"type\":\"response.output_text.delta\",\r\n"),
		[]byte("data: \"delta\":\"hello\"}\r\n\r\nevent: response.completed\n"),
		[]byte("data: {\"type\":\"response.completed\"}\n\n"),
	} {
		if err := observer.Observe(context.Background(), chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := observer.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []observed{
		{typ: "response.output_text.delta", data: "{\"type\":\"response.output_text.delta\",\n\"delta\":\"hello\"}"},
		{typ: "response.completed", data: "{\"type\":\"response.completed\"}"},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	if !observer.ReachedTerminal() {
		t.Fatal("terminal event was not detected")
	}
}

func BenchmarkIncrementalSSEObserver(b *testing.B) {
	for _, size := range []int{256, 1 << 20} {
		name := "Small"
		if size > 256 {
			name = "LargePayload"
		}
		payload := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("x", size) + "\"}\n\n")
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			ctx := context.Background()
			for range b.N {
				observer := NewIncrementalSSEObserver(len(payload)+1, nil, nil)
				for offset := 0; offset < len(payload); offset += 4096 {
					end := min(offset+4096, len(payload))
					if err := observer.Observe(ctx, payload[offset:end]); err != nil {
						b.Fatal(err)
					}
				}
				if err := observer.Finalize(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestIncrementalSSEObserverInfersJSONType(t *testing.T) {
	var gotType string
	observer := NewIncrementalSSEObserver(1024, nil, func(_ context.Context, eventType string, data []byte) error {
		gotType = eventType
		return nil
	})
	if err := observer.Observe(context.Background(), []byte("data: {\"type\":\"message_stop\"}\n\n")); err != nil {
		t.Fatal(err)
	}
	if gotType != "message_stop" {
		t.Fatalf("event type = %q", gotType)
	}
}

func TestIncrementalSSEObserverEmitsSourceEventMetadata(t *testing.T) {
	var got SourceEvent
	observer := NewIncrementalSourceEventObserver(1024, nil, func(_ context.Context, event SourceEvent) error {
		got = event
		return nil
	})
	if err := observer.Observe(context.Background(), []byte("id: event-9\nevent: message_stop\ndata: {}\n\n")); err != nil {
		t.Fatal(err)
	}
	if got.Type != "message_stop" || string(got.Data) != "{}" || got.ID != "event-9" || got.Sequence != 1 || got.Transport != SourceTransportSSE {
		t.Fatalf("source event = %#v, want complete SSE metadata", got)
	}
}

func TestIncrementalSSEObserverLimitsIndividualEventNotWholeChunk(t *testing.T) {
	observed := 0
	observer := NewIncrementalSSEObserver(40, nil, func(_ context.Context, _ string, _ []byte) error {
		observed++
		return nil
	})
	chunk := []byte("data: {\"type\":\"one\"}\n\ndata: {\"type\":\"two\"}\n\n")
	if len(chunk) <= 40 {
		t.Fatalf("test chunk must exceed per-event limit, got %d bytes", len(chunk))
	}
	if err := observer.Observe(context.Background(), chunk); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if observed != 2 {
		t.Fatalf("observed events = %d, want 2", observed)
	}
}

func TestIncrementalSSEObserverPreviewsCompleteJSONBeforeBlankLine(t *testing.T) {
	observed := 0
	observer := NewIncrementalSSEObserver(1024, nil, func(_ context.Context, eventType string, data []byte) error {
		observed++
		if eventType != "response.output_text.delta" || string(data) != `{"type":"response.output_text.delta","delta":"ok"}` {
			t.Fatalf("unexpected event: type=%q data=%s", eventType, data)
		}
		return nil
	})
	observer.SetSourceInspector(&openai.ResponseOutbound{})
	if err := observer.Observe(context.Background(), []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n")); err != nil {
		t.Fatal(err)
	}
	if observed != 0 || !observer.HasSemanticPreview() {
		t.Fatalf("preview should release precommit without consuming the frame: observed=%d semantic=%t", observed, observer.HasSemanticPreview())
	}
	if err := observer.Observe(context.Background(), []byte("\n")); err != nil {
		t.Fatal(err)
	}
	if observed != 1 {
		t.Fatalf("blank line redispatched event: observed=%d", observed)
	}
}

func TestIncrementalSSEObserverTerminalRequiresAcceptedCompleteFrame(t *testing.T) {
	ctx := context.Background()
	observer := NewIncrementalSourceEventObserver(1024, nil, nil)
	observer.SetSourceInspector(&openai.ResponseOutbound{})
	if err := observer.Observe(ctx, []byte("data: {\"type\":\"response.completed\"}\n")); err != nil {
		t.Fatal(err)
	}
	if observer.ReachedTerminal() {
		t.Fatal("unfinished frame terminated the stream")
	}
	// Later envelope fields still override a JSON type seen by the preview.
	if err := observer.Observe(ctx, []byte("event: response.output_text.delta\n\n")); err != nil {
		t.Fatal(err)
	}
	if observer.ReachedTerminal() {
		t.Fatal("superseded terminal preview terminated the stream")
	}
	if err := observer.Observe(ctx, []byte("data: {\"type\":\"response.completed\"}\n\n")); err != nil {
		t.Fatal(err)
	}
	if !observer.ReachedTerminal() {
		t.Fatal("complete terminal frame was missed")
	}

	failure := errors.New("invalid terminal")
	rejected := NewIncrementalSourceEventObserver(1024, map[string]struct{}{"done": {}}, func(context.Context, SourceEvent) error { return failure })
	if err := rejected.Observe(ctx, []byte("event: done\ndata: {}\n\n")); !errors.Is(err, failure) {
		t.Fatalf("observer error = %v", err)
	}
	if rejected.ReachedTerminal() {
		t.Fatal("rejected terminal was accepted")
	}
}
