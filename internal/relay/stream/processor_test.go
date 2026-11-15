package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// mockStreamWriter implements StreamWriter for testing.
type mockStreamWriter struct {
	buffer  bytes.Buffer
	written bool
	headers http.Header
}

func newMockStreamWriter() *mockStreamWriter {
	return &mockStreamWriter{
		headers: make(http.Header),
	}
}

func (m *mockStreamWriter) Write(data []byte) (int, error) {
	m.written = true
	return m.buffer.Write(data)
}

func (m *mockStreamWriter) Flush() {}

func (m *mockStreamWriter) Written() bool {
	return m.written
}

func (m *mockStreamWriter) Header() http.Header {
	return m.headers
}

func (m *mockStreamWriter) WriteHeader(code int) {}

// mockStreamSource implements StreamSource for testing.
type mockStreamSource struct {
	events [][]byte
	index  int
	closed bool
}

func newMockStreamSource(events [][]byte) *mockStreamSource {
	return &mockStreamSource{events: events}
}

func (m *mockStreamSource) ReadSourceEvent(ctx context.Context) (SourceEvent, error) {
	if m.index >= len(m.events) {
		return SourceEvent{}, io.EOF
	}
	data := m.events[m.index]
	m.index++
	return SourceEvent{Data: data}, nil
}

func (m *mockStreamSource) Close() error {
	m.closed = true
	return nil
}

func TestStreamProcessor_BasicFlow(t *testing.T) {
	events := [][]byte{
		[]byte(`{"data":"chunk1"}`),
		[]byte(`{"data":"chunk2"}`),
		[]byte(`{"data":"chunk3"}`),
	}

	source := newMockStreamSource(events)
	writer := newMockStreamWriter()
	ctx := context.Background()

	processor := NewStreamProcessor(StreamConfig{
		Source:  source,
		Writer:  writer,
		Context: ctx,
	})

	err := processor.Run()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !source.closed {
		t.Error("source not closed")
	}

	output := writer.buffer.String()
	for _, event := range events {
		if !strings.Contains(output, string(event)) {
			t.Errorf("output missing event: %s", event)
		}
	}
}

func TestStreamProcessor_WithTransform(t *testing.T) {
	events := [][]byte{
		[]byte(`chunk1`),
		[]byte(`chunk2`),
	}

	source := newMockStreamSource(events)
	writer := newMockStreamWriter()
	ctx := context.Background()

	transform := func(ctx context.Context, event SourceEvent) ([]byte, error) {
		// Wrap in SSE format
		return []byte("data: " + string(event.Data) + "\n\n"), nil
	}

	processor := NewStreamProcessor(StreamConfig{
		Source:         source,
		Writer:         writer,
		Context:        ctx,
		TransformEvent: transform,
	})

	err := processor.Run()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := writer.buffer.String()
	expected := "data: chunk1\n\ndata: chunk2\n\n"
	if output != expected {
		t.Errorf("unexpected output:\ngot:  %q\nwant: %q", output, expected)
	}
}

func TestStreamProcessor_FirstTokenCallback(t *testing.T) {
	events := [][]byte{
		[]byte(`chunk1`),
		[]byte(`chunk2`),
	}

	source := newMockStreamSource(events)
	writer := newMockStreamWriter()
	ctx := context.Background()

	firstTokenCalled := false
	processor := NewStreamProcessor(StreamConfig{
		Source:  source,
		Writer:  writer,
		Context: ctx,
		OnFirstToken: func() {
			firstTokenCalled = true
		},
	})

	err := processor.Run()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !firstTokenCalled {
		t.Error("OnFirstToken not called")
	}
}

func TestStreamProcessor_EmptyStream(t *testing.T) {
	source := newMockStreamSource([][]byte{})
	writer := newMockStreamWriter()
	ctx := context.Background()

	processor := NewStreamProcessor(StreamConfig{
		Source:  source,
		Writer:  writer,
		Context: ctx,
	})

	err := processor.Run()
	if err == nil {
		t.Fatal("expected error for empty stream")
	}

	if !errors.Is(err, ErrEmptyUpstreamStream) {
		t.Errorf("expected ErrEmptyUpstreamStream, got: %v", err)
	}
}

func TestStreamProcessor_PrecommitWaitsForMeaningfulPayload(t *testing.T) {
	events := [][]byte{
		[]byte(`{"type":"response.created"}`),
		[]byte(`{"type":"response.output_text.delta","delta":"hello"}`),
	}
	writer := newMockStreamWriter()
	firstTokenCalls := 0
	processor := NewStreamProcessor(StreamConfig{
		Source:  newMockStreamSource(events),
		Writer:  writer,
		Context: context.Background(),
		TransformEvent: func(_ context.Context, event SourceEvent) ([]byte, error) {
			return append(append([]byte("data: "), event.Data...), []byte("\n\n")...), nil
		},
		PrecommitPredicate: isMeaningfulPayload,
		OnFirstToken:       func() { firstTokenCalls++ },
	})

	if err := processor.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(writer.buffer.String(), "response.created") || !strings.Contains(writer.buffer.String(), "hello") {
		t.Fatalf("expected buffered metadata and payload to commit together, got %q", writer.buffer.String())
	}
	if firstTokenCalls != 1 {
		t.Fatalf("expected one first token callback, got %d", firstTokenCalls)
	}
}

func TestStreamProcessor_PrecommitRejectsMetadataOnlyStream(t *testing.T) {
	writer := newMockStreamWriter()
	processor := NewStreamProcessor(StreamConfig{
		Source:  newMockStreamSource([][]byte{[]byte(`{"type":"response.created"}`), []byte(`{"type":"response.completed"}`)}),
		Writer:  writer,
		Context: context.Background(),
		TransformEvent: func(_ context.Context, event SourceEvent) ([]byte, error) {
			return append(append([]byte("data: "), event.Data...), []byte("\n\n")...), nil
		},
		PrecommitPredicate: isMeaningfulPayload,
	})

	err := processor.Run()
	if !errors.Is(err, ErrEmptyUpstreamStream) {
		t.Fatalf("expected retryable empty stream error, got %v", err)
	}
	if writer.Written() {
		t.Fatalf("metadata-only stream must not commit bytes, got %q", writer.buffer.String())
	}
}

func TestStreamProcessor_PrecommitLimitFailsWithoutWriting(t *testing.T) {
	writer := newMockStreamWriter()
	processor := NewStreamProcessor(StreamConfig{
		Source:  newMockStreamSource([][]byte{[]byte(`{"type":"response.created"}`), []byte(`{"type":"response.in_progress"}`)}),
		Writer:  writer,
		Context: context.Background(),
		TransformEvent: func(_ context.Context, event SourceEvent) ([]byte, error) {
			return append(append([]byte("data: "), event.Data...), []byte("\n\n")...), nil
		},
		PrecommitPredicate: func(_, _ []byte) bool { return false },
		PrecommitMaxEvents: 2,
		PrecommitMaxBytes:  1024,
	})

	err := processor.Run()
	if !errors.Is(err, ErrPrecommitLimitExceeded) {
		t.Fatalf("Run() error = %v, want ErrPrecommitLimitExceeded", err)
	}
	if writer.Written() {
		t.Fatalf("precommit limit failure must not write downstream bytes, got %q", writer.buffer.String())
	}
}

// AllowEmptyPayload suppresses only the empty-response verdict: the buffered
// metadata is handed to the client and the stream completes normally.
func TestStreamProcessor_AllowEmptyPayloadFlushesMetadataOnlyStream(t *testing.T) {
	writer := newMockStreamWriter()
	processor := NewStreamProcessor(StreamConfig{
		Source:  newMockStreamSource([][]byte{[]byte(`{"type":"response.created"}`), []byte(`{"type":"response.completed"}`)}),
		Writer:  writer,
		Context: context.Background(),
		TransformEvent: func(_ context.Context, event SourceEvent) ([]byte, error) {
			return append(append([]byte("data: "), event.Data...), []byte("\n\n")...), nil
		},
		PrecommitPredicate: isMeaningfulPayload,
		AllowEmptyPayload:  true,
	})

	if err := processor.Run(); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	body := writer.buffer.String()
	if !strings.Contains(body, "response.created") || !strings.Contains(body, "response.completed") {
		t.Fatalf("expected buffered metadata to be flushed, got %q", body)
	}
}

// Hitting the precommit limit with detection disabled commits the buffer
// instead of failing the attempt.
func TestStreamProcessor_AllowEmptyPayloadCommitsAtPrecommitLimit(t *testing.T) {
	writer := newMockStreamWriter()
	processor := NewStreamProcessor(StreamConfig{
		Source:  newMockStreamSource([][]byte{[]byte(`{"type":"response.created"}`), []byte(`{"type":"response.in_progress"}`)}),
		Writer:  writer,
		Context: context.Background(),
		TransformEvent: func(_ context.Context, event SourceEvent) ([]byte, error) {
			return append(append([]byte("data: "), event.Data...), []byte("\n\n")...), nil
		},
		PrecommitPredicate: func(_, _ []byte) bool { return false },
		PrecommitMaxEvents: 2,
		PrecommitMaxBytes:  1024,
		AllowEmptyPayload:  true,
	})

	if err := processor.Run(); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if !writer.Written() {
		t.Fatal("expected buffered events to be committed at the precommit limit")
	}
}

// A stream that forwards nothing at all is broken regardless of the switch.
func TestStreamProcessor_AllowEmptyPayloadStillRejectsSilentStream(t *testing.T) {
	writer := newMockStreamWriter()
	processor := NewStreamProcessor(StreamConfig{
		Source:             newMockStreamSource(nil),
		Writer:             writer,
		Context:            context.Background(),
		TransformEvent:     func(context.Context, SourceEvent) ([]byte, error) { return nil, nil },
		PrecommitPredicate: isMeaningfulPayload,
		AllowEmptyPayload:  true,
	})

	if err := processor.Run(); !errors.Is(err, ErrEmptyUpstreamStream) {
		t.Fatalf("Run() error = %v, want ErrEmptyUpstreamStream", err)
	}
}

func TestStreamProcessor_ContextCancellation(t *testing.T) {
	// Source that emits one chunk then blocks until cancelled
	source := &cancelTestSource{first: []byte(`chunk1`)}
	writer := newMockStreamWriter()
	ctx, cancel := context.WithCancel(context.Background())

	firstTokenSeen := make(chan struct{})
	processor := NewStreamProcessor(StreamConfig{
		Source:  source,
		Writer:  writer,
		Context: ctx,
		OnFirstToken: func() {
			close(firstTokenSeen)
		},
	})

	errChan := make(chan error, 1)
	go func() {
		errChan <- processor.Run()
	}()

	// Wait until first token is processed, then cancel
	<-firstTokenSeen
	cancel()

	err := <-errChan
	if err == nil {
		t.Fatal("expected context cancellation error")
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

// cancelTestSource emits one chunk then blocks until context is cancelled.
type cancelTestSource struct {
	first  []byte
	sent   bool
	closed bool
}

func (s *cancelTestSource) ReadSourceEvent(ctx context.Context) (SourceEvent, error) {
	if !s.sent {
		s.sent = true
		return SourceEvent{Data: s.first}, nil
	}
	<-ctx.Done()
	return SourceEvent{}, ctx.Err()
}

func (s *cancelTestSource) Close() error {
	s.closed = true
	return nil
}

func TestStreamProcessor_OnFinish(t *testing.T) {
	events := [][]byte{
		[]byte(`chunk1`),
		[]byte(`chunk2`),
	}

	source := newMockStreamSource(events)
	writer := newMockStreamWriter()
	ctx := context.Background()

	finished := false
	processor := NewStreamProcessor(StreamConfig{
		Source:  source,
		Writer:  writer,
		Context: ctx,
		OnFinish: func(ctx context.Context) error {
			finished = true
			return nil
		},
	})

	err := processor.Run()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !finished {
		t.Error("OnFinish was not called")
	}

	expected := "chunk1chunk2"
	if got := writer.buffer.String(); got != expected {
		t.Errorf("unexpected forwarded data:\ngot:  %q\nwant: %q", got, expected)
	}
}

func TestStreamProcessor_FirstTokenTimeout(t *testing.T) {
	// Source that blocks forever
	blockingSource := &blockingStreamSource{}
	writer := newMockStreamWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	processor := NewStreamProcessor(StreamConfig{
		Source:            blockingSource,
		Writer:            writer,
		Context:           ctx,
		FirstTokenTimeout: 10 * time.Millisecond,
	})

	err := processor.Run()
	if err == nil {
		t.Fatal("expected timeout error")
	}

	if !strings.Contains(err.Error(), "first token timeout") {
		t.Errorf("unexpected error: %v", err)
	}
}

// blockingStreamSource blocks forever in ReadSourceEvent.
type blockingStreamSource struct {
	closed bool
}

func (s *blockingStreamSource) ReadSourceEvent(ctx context.Context) (SourceEvent, error) {
	<-ctx.Done()
	return SourceEvent{}, ctx.Err()
}

func (s *blockingStreamSource) Close() error {
	s.closed = true
	return nil
}

// stubTerminalObserver reports that a terminal event was seen. Terminal
// detection lives on the Observer since the raw-stream buffer was removed.
type stubTerminalObserver struct {
	terminal  bool
	finalized bool
}

func (o *stubTerminalObserver) Observe(ctx context.Context, data []byte) error { return nil }

func (o *stubTerminalObserver) Finalize(ctx context.Context) error {
	o.finalized = true
	return nil
}

func (o *stubTerminalObserver) ReachedTerminal() bool { return o.terminal }

func TestStreamProcessor_TerminalEventDetection(t *testing.T) {
	sseData := `data: {"type":"message_start"}

data: {"type":"content_block_delta"}

data: {"type":"message_stop"}

`
	source := newMockStreamSource([][]byte{[]byte(sseData)})
	writer := newMockStreamWriter()
	ctx, cancel := context.WithCancel(context.Background())

	observer := &stubTerminalObserver{terminal: true}

	firstTokenSeen := make(chan struct{})
	processor := NewStreamProcessor(StreamConfig{
		Source:   source,
		Writer:   writer,
		Context:  ctx,
		Observer: observer,
		OnFirstToken: func() {
			close(firstTokenSeen)
		},
	})

	// Start processor in goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- processor.Run()
	}()

	// Wait until first token is written, then cancel
	<-firstTokenSeen
	cancel()

	// Should treat as success because the observer reached a terminal event
	err := <-errChan
	if err != nil {
		t.Errorf("expected nil error due to terminal event detection, got: %v", err)
	}
	if !observer.finalized {
		t.Error("expected the observer to be finalized on the success path")
	}
}

func TestRawSource(t *testing.T) {
	data := []byte("raw chunk data")
	reader := io.NopCloser(bytes.NewReader(data))

	source := NewRawSource(reader, 10)
	defer source.Close()

	var chunks [][]byte
	for {
		event, err := source.ReadSourceEvent(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(event.Data) > 0 {
			chunks = append(chunks, event.Data)
		}
	}

	// Reconstruct data
	result := bytes.Join(chunks, nil)
	if !bytes.Equal(result, data) {
		t.Errorf("data mismatch:\ngot:  %q\nwant: %q", result, data)
	}
}

func TestSSESource(t *testing.T) {
	sseData := `data: event1

data: event2

data: event3

`
	reader := io.NopCloser(strings.NewReader(sseData))
	source := NewSSESource(reader, 0)
	defer source.Close()

	expected := []string{"event1", "event2", "event3"}
	var events []string

	for {
		event, err := source.ReadSourceEvent(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		events = append(events, string(event.Data))
	}

	if len(events) != len(expected) {
		t.Fatalf("event count mismatch: got %d, want %d", len(events), len(expected))
	}

	for i, ev := range events {
		if ev != expected[i] {
			t.Errorf("event[%d] mismatch: got %q, want %q", i, ev, expected[i])
		}
	}
}
