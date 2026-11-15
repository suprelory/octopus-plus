package stream

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

type heartbeatFragmentSource struct {
	fragments chan []byte
}

func (s *heartbeatFragmentSource) ReadSourceEvent(ctx context.Context) (SourceEvent, error) {
	select {
	case <-ctx.Done():
		return SourceEvent{}, ctx.Err()
	case data, ok := <-s.fragments:
		if !ok {
			return SourceEvent{}, io.EOF
		}
		return SourceEvent{Data: data}, nil
	}
}

func (s *heartbeatFragmentSource) Close() error { return nil }

type heartbeatFragmentWriter struct {
	*mockStreamWriter
	writes chan []byte
}

func (w *heartbeatFragmentWriter) Write(data []byte) (int, error) {
	n, err := w.mockStreamWriter.Write(data)
	w.writes <- append([]byte(nil), data...)
	return n, err
}

func TestHeartbeatNeverSplitsPassthroughSSEFrame(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		frame := "event: delta" + newline + "data: {\"text\":\"hello\"}" + newline + newline
		for split := 1; split < len(frame); split++ {
			// A trailing CR already ends the event even if its LF arrives later.
			if newline == "\r\n" && split == len(frame)-1 {
				continue
			}
			source := &heartbeatFragmentSource{fragments: make(chan []byte)}
			writer := &heartbeatFragmentWriter{mockStreamWriter: newMockStreamWriter(), writes: make(chan []byte, 4)}
			ticks := make(chan time.Time)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			processor := NewStreamProcessor(StreamConfig{
				Source: source, Writer: writer, Context: ctx, HeartbeatC: ticks,
			})
			done := make(chan error, 1)
			go func() { done <- processor.Run() }()
			send := func(fragment string) {
				t.Helper()
				select {
				case source.fragments <- []byte(fragment):
				case <-ctx.Done():
					t.Fatal("processor did not read fragment")
				}
				select {
				case <-writer.writes:
				case <-ctx.Done():
					t.Fatal("processor did not write fragment")
				}
			}
			send(frame[:split])
			select {
			case ticks <- time.Now():
			case <-ctx.Done():
				t.Fatal("processor did not read heartbeat tick")
			}
			send(frame[split:])
			close(source.fragments)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("processor did not finish")
			}
			cancel()
			if got := writer.buffer.String(); got != frame+":\n\n" {
				t.Fatalf("heartbeat corrupted a fragmented frame at %d with %q newlines: %q", split, newline, got)
			}
			if strings.Count(writer.buffer.String(), "hello") != 1 {
				t.Fatal("payload duplicated")
			}
		}
	}
}
