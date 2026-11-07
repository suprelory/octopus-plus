package stream

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/polywire/model"
	openai "github.com/suprelory/octopus-plus/polywire/outbound/openai"
)

type testWSReader struct {
	events [][]byte
	index  int
}

func (r *testWSReader) ReadEvent(context.Context) ([]byte, error) {
	if r.index >= len(r.events) {
		return nil, io.EOF
	}
	event := r.events[r.index]
	r.index++
	return event, nil
}

func (*testWSReader) Close() error    { return nil }
func (*testWSReader) CloseWithError() {}
func (*testWSReader) StatusCode() int { return 200 }

func TestWSSourceExtractsEnvelopeMetadata(t *testing.T) {
	source := NewWSSource(&testWSReader{events: [][]byte{
		[]byte(`{"type":"response.created","event_id":"evt-1"}`),
		[]byte(`{"type":"response.completed","id":"evt-2"}`),
	}})

	first, err := source.ReadSourceEvent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.ReadSourceEvent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Type != "response.created" || first.ID != "evt-1" || first.Sequence != 1 || first.Transport != SourceTransportWebSocket {
		t.Fatalf("first event = %#v, want WebSocket metadata", first)
	}
	if second.Type != "response.completed" || second.ID != "evt-2" || second.Sequence != 2 || second.Transport != SourceTransportWebSocket {
		t.Fatalf("second event = %#v, want WebSocket metadata", second)
	}
}

type inspectedWSReader struct {
	testWSReader
	source SourceEvent
}

func (r *inspectedWSReader) ReadSourceEvent(context.Context) (SourceEvent, error) {
	return r.source, nil
}

func TestWSSourcePreservesAlreadyInspectedEvents(t *testing.T) {
	observation, err := openai.InspectResponseEvent([]byte(`{"type":"response.output_text.delta","event_id":"native-id","delta":"hello"}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	source := NewWSSource(&inspectedWSReader{source: observation.Source}, model.SourceEventInspectorFunc(func(context.Context, SourceEvent) (SourceEvent, model.StreamEventPreview, error) {
		t.Fatal("an already inspected reader was parsed again")
		return SourceEvent{}, model.StreamEventPreview{}, nil
	}))
	event, err := source.ReadSourceEvent(context.Background())
	if err != nil || event.Decoded != observation.Source.Decoded || event.ID != "native-id" || event.Type != observation.Type || event.Sequence != 1 || event.Transport != SourceTransportWebSocket {
		t.Fatalf("inspected source metadata or DTO lost: %+v, %v", event, err)
	}
}
