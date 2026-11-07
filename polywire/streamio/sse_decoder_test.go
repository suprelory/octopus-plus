package streamio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
	anthropic "github.com/suprelory/octopus-plus/polywire/outbound/anthropic"
	openai "github.com/suprelory/octopus-plus/polywire/outbound/openai"
)

func decodeChunks(wire []byte, chunk, limit int) ([]SourceEvent, error) {
	decoder := NewSSEDecoder(limit)
	var events []SourceEvent
	emit := func(event SourceEvent) error { events = append(events, event); return nil }
	for offset := 0; offset < len(wire); offset += chunk {
		if err := decoder.Feed(wire[offset:min(offset+chunk, len(wire))], emit); err != nil {
			return events, err
		}
	}
	return events, decoder.Finish(emit)
}

func TestSSEDecoderWireContract(t *testing.T) {
	wire := []byte("\xef\xbb\xbf: comment\rid: first\r\nevent: delta\ndata: {}\ndata:\ndata: tail\nid: last\r\n\r\nid: bad\x00id\ndata: next\n\nid:\nevent: done\ndata:\n\n")
	want := []SourceEvent{
		{Type: "delta", Data: []byte("{}\n\ntail"), ID: "last", Sequence: 1, Transport: SourceTransportSSE},
		{Data: []byte("next"), ID: "last", Sequence: 2, Transport: SourceTransportSSE},
		{Type: "done", ID: "", Sequence: 3, Transport: SourceTransportSSE},
	}
	for _, size := range []int{1, 2, 3, 7, 64, len(wire)} {
		events, err := decodeChunks(wire, size, 1024)
		if err != nil || len(events) != len(want) {
			t.Fatalf("chunk=%d: %+v, %v", size, events, err)
		}
		for index := range events {
			if events[index].Type != want[index].Type || events[index].ID != want[index].ID || !bytes.Equal(events[index].Data, want[index].Data) || events[index].Sequence != want[index].Sequence {
				t.Fatalf("chunk=%d event=%+v, want %+v", size, events[index], want[index])
			}
		}
	}
}

func TestSSEDecoderBoundsAllFrameFields(t *testing.T) {
	for _, wire := range []string{"data: " + strings.Repeat("x", 128), "id: " + strings.Repeat("x", 128), "event: " + strings.Repeat("x", 128), ":" + strings.Repeat("x", 128), strings.Repeat("ignored: x\n", 32), strings.Repeat("data:\n", 32)} {
		for _, size := range []int{1, len(wire)} {
			if _, err := decodeChunks([]byte(wire), size, 64); err == nil {
				t.Fatalf("unbounded frame accepted: %q", wire)
			}
		}
	}
	if events, err := decodeChunks([]byte(strings.Repeat("data: x\n\n", 100)), 1024, 16); err != nil || len(events) != 100 {
		t.Fatalf("limited whole chunk instead of event: %d, %v", len(events), err)
	}
}

func TestSSEDecoderAndObserverRetainTrailingMetadataAndMultilineData(t *testing.T) {
	var events []SourceEvent
	observer := NewIncrementalSourceEventObserver(1024, nil, func(_ context.Context, event SourceEvent) error { events = append(events, event); return nil })
	for _, chunk := range []string{"data: {}\n", "data: {}\nid: trailing\nevent: multi\n\n"} {
		if err := observer.Observe(context.Background(), []byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if len(events) != 1 || events[0].Type != "multi" || events[0].ID != "trailing" || string(events[0].Data) != "{}\n{}" {
		t.Fatalf("framing split one native event: %+v", events)
	}
}

func TestSSEPreviewRetainsFinalEnvelopeWithoutConsumingState(t *testing.T) {
	var events []SourceEvent
	observer := NewIncrementalSourceEventObserver(1024, nil, func(_ context.Context, event SourceEvent) error { events = append(events, event); return nil })
	observer.SetSourceInspector(&openai.ResponseOutbound{})
	if err := observer.Observe(context.Background(), []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n")); err != nil {
		t.Fatal(err)
	}
	if !observer.HasSemanticPreview() || len(events) != 0 {
		t.Fatal("preview consumed a frame or failed to release precommit")
	}
	if err := observer.Observe(context.Background(), []byte("id: late\nevent: response.output_text.delta\n\n")); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "late" || events[0].Sequence != 1 || events[0].Decoded == nil {
		t.Fatalf("preview lost late metadata or parsed DTO: %+v", events)
	}
}

func TestSSEPreviewCacheRetainsPayloadTypeAfterEnvelopeReset(t *testing.T) {
	for _, tc := range []struct {
		name, initialType, data string
		adapter                 interface {
			model.SourceEventInspector
			TransformSourceEvent(context.Context, model.SourceEvent) ([]model.StreamEvent, error)
		}
	}{
		{"responses", "response.created", `{"type":"response.output_text.delta","delta":"hello"}`, &openai.ResponseOutbound{}},
		{"anthropic", "ping", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`, &anthropic.MessageOutbound{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []model.StreamEvent
			parses := 0
			observer := NewIncrementalSourceEventObserver(1024, nil, func(ctx context.Context, source SourceEvent) error {
				if source.Type != "" || source.ID != "late" {
					t.Fatalf("final wire envelope lost: %+v", source)
				}
				var err error
				got, err = tc.adapter.TransformSourceEvent(ctx, source)
				return err
			})
			observer.SetSourceInspector(model.SourceEventInspectorFunc(func(ctx context.Context, source SourceEvent) (SourceEvent, model.StreamEventPreview, error) {
				if source.Decoded == nil {
					parses++
				}
				return tc.adapter.InspectSourceEvent(ctx, source)
			}))
			for _, chunk := range []string{"event: " + tc.initialType + "\ndata: " + tc.data + "\n", "event:\nid: late\n", "\n"} {
				if err := observer.Observe(context.Background(), []byte(chunk)); err != nil {
					t.Fatal(err)
				}
			}
			if parses != 1 || len(got) != 1 || got[0].Kind != model.StreamEventKindTextDelta || got[0].Delta.Text != "hello" {
				t.Fatalf("reset retained stale type or reparsed: parses=%d events=%+v", parses, got)
			}
		})
	}
}

func TestSSEPreviewDoesNotCacheMalformedTerminalAsValidJSON(t *testing.T) {
	observer := NewIncrementalSourceEventObserver(1024, nil, nil)
	observer.SetSourceInspector(&openai.ResponseOutbound{})
	if err := observer.Observe(context.Background(), []byte("event: response.completed\ndata: not-json\n")); err != nil {
		t.Fatal(err)
	}
	if err := observer.Observe(context.Background(), []byte("event: response.output_text.delta\n\n")); err == nil {
		t.Fatal("late envelope type bypassed JSON validation through the preview cache")
	}
}

func TestSSESourceErrorDoesNotFinalizePendingTerminal(t *testing.T) {
	for _, delimiter := range []string{"\n", "\n\n"} {
		failure := errors.New("connection reset")
		source := NewSSESource(&decoderDataErrorReader{data: []byte("event: response.completed\ndata: {}" + delimiter), err: failure}, 1024)
		defer source.Close()
		event, err := source.ReadSourceEvent(context.Background())
		if delimiter == "\n\n" {
			if err != nil || event.Type != "response.completed" {
				t.Fatalf("complete frame lost: %+v, %v", event, err)
			}
			_, err = source.ReadSourceEvent(context.Background())
		}
		if !errors.Is(err, failure) {
			t.Fatalf("source error became EOF/completion: %v", err)
		}
	}
}

type decoderDataErrorReader struct {
	data []byte
	err  error
}

func (r *decoderDataErrorReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, r.err
}
func (*decoderDataErrorReader) Close() error { return nil }

func TestSSEDecoderFinalizesOnlyOnce(t *testing.T) {
	decoder := NewSSEDecoder(1024)
	count := 0
	emit := func(SourceEvent) error { count++; return nil }
	if err := decoder.Feed([]byte("data: tail"), emit); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Finish(emit); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Finish(emit); err != nil || count != 1 {
		t.Fatalf("duplicate finish: count=%d err=%v", count, err)
	}
	if err := decoder.Feed([]byte("data: extra\n\n"), emit); err == nil {
		t.Fatal("event after finalization accepted")
	}
}

type contractChunkReader struct {
	io.Reader
	size int
}

func (r contractChunkReader) Read(p []byte) (int, error) {
	return r.Reader.Read(p[:min(len(p), r.size)])
}

func TestSSESourceMatchesObserverAcrossSplitReads(t *testing.T) {
	wire := "data: {}\nid: tail\n\ndata:\ndata: next\n\nevent: done\ndata:\n"
	for _, size := range []int{1, 3, 64} {
		var observed, read []SourceEvent
		observer := NewIncrementalSourceEventObserver(1024, nil, func(_ context.Context, event SourceEvent) error { observed = append(observed, event); return nil })
		for offset := 0; offset < len(wire); offset += size {
			if err := observer.Observe(context.Background(), []byte(wire[offset:min(offset+size, len(wire))])); err != nil {
				t.Fatal(err)
			}
		}
		if err := observer.Finalize(context.Background()); err != nil {
			t.Fatal(err)
		}
		source := NewSSESource(io.NopCloser(contractChunkReader{strings.NewReader(wire), size}), 1024)
		for {
			event, err := source.ReadSourceEvent(context.Background())
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			read = append(read, event)
		}
		source.Close()
		if !reflect.DeepEqual(read, observed) {
			t.Fatalf("source/observer mismatch: %+v / %+v", read, observed)
		}
	}
}

func FuzzSSEDecoderChunkBoundaries(f *testing.F) {
	for _, seed := range []string{"data: x\n\n", "\xef\xbb\xbfevent: done\r\ndata:\r\n\r\n", "data: {}\ndata:\nid: tail\n\n", "id: bad\x00id\n\ndata: tail"} {
		f.Add([]byte(seed), uint8(3))
	}
	f.Fuzz(func(t *testing.T, wire []byte, split uint8) {
		if len(wire) > 4096 {
			t.Skip()
		}
		whole, wholeErr := decodeChunks(wire, max(1, len(wire)), 1024)
		chunks, chunkErr := decodeChunks(wire, int(split)+1, 1024)
		if (wholeErr == nil) != (chunkErr == nil) || !reflect.DeepEqual(whole, chunks) {
			t.Fatalf("chunk-dependent framing: %+v %v / %+v %v", whole, wholeErr, chunks, chunkErr)
		}
		for _, event := range chunks {
			if len(event.Data) > 1024 {
				t.Fatal("unbounded decoded payload")
			}
		}
	})
}
