// Package capture observes relay traffic without changing transport semantics.
package capture

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

// Budget bounds the uncompressed bytes retained across every attempt in a request.
type Budget struct {
	mu        sync.Mutex
	remaining int64
}

func NewBudget(size int64) *Budget { return &Budget{remaining: size} }

func (b *Budget) take(size int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if int64(size) > b.remaining {
		size = int(b.remaining)
	}
	b.remaining -= int64(size)
	return size
}

type Body struct {
	mu         sync.Mutex
	budget     *Budget
	limit      int64
	message    model.RelayMessage
	compressed bytes.Buffer
	writer     *gzip.Writer
	done       bool
	started    time.Time
	sse        bool
	eventStart int64
	position   int64
	lineBytes  int64
	pendingCR  bool
	boundary   bool
}

func NewBody(message model.RelayMessage, budget *Budget, limit int64) *Body {
	message.State = "partial"
	if limit == 0 {
		message.State = "not_captured"
		message.Reason = "disabled"
	}
	return &Body{message: message, budget: budget, limit: limit, started: time.Now(), sse: strings.HasPrefix(strings.ToLower(message.ContentType), "text/event-stream")}
}

// Observe never returns an error to the forwarding path. Compression happens
// incrementally; a long response never requires an uncompressed full-body copy.
func (b *Body) Observe(p []byte) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.observe(p)
}

// observe requires mu. Event parsing sees retained bytes only.
func (b *Body) observe(p []byte) {
	if b.done {
		return
	}
	b.message.Bytes += int64(len(p))
	if b.limit == 0 {
		return
	}
	n := min(len(p), int(b.limit-b.message.CapturedBytes))
	if b.budget != nil {
		n = b.budget.take(n)
	}
	if n < len(p) {
		b.message.State = "truncated"
		b.message.Reason = "size_limit"
		b.message.EventsTruncated = b.sse || len(b.message.Events) > 0
	}
	if n == 0 {
		return
	}
	if b.writer == nil {
		b.writer, _ = gzip.NewWriterLevel(&b.compressed, gzip.BestSpeed)
	}
	_, _ = b.writer.Write(p[:n])
	b.message.CapturedBytes += int64(n)
	if b.sse {
		b.observeSSE(p[:n])
	}
}

func (b *Body) Finish(complete bool) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return
	}
	b.done = true
	if b.sse {
		if b.pendingCR && b.boundary {
			b.appendEvent(b.eventStart, b.position-b.eventStart, "sse", true)
			b.eventStart = b.position
		}
		if b.position > b.eventStart {
			b.appendEvent(b.eventStart, b.position-b.eventStart, "sse", false)
		}
	}
	if b.writer != nil {
		_ = b.writer.Close()
	}
	if b.message.State == "partial" && complete {
		b.message.State = "captured"
	}
}

func (b *Body) Snapshot() *model.RelayMessage {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	result := b.message
	result.Data = b.compressed.Bytes()
	return &result
}

type reader struct {
	io.ReadCloser
	body           *Body
	expected, read int64
}

func Reader(source io.ReadCloser, body *Body, expected int64) io.ReadCloser {
	if source == nil || body == nil {
		return source
	}
	return &reader{ReadCloser: source, body: body, expected: expected}
}

func (r *reader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.body.Observe(p[:n])
	r.read += int64(n)
	if err == io.EOF {
		r.body.Finish(true)
	} else if err != nil {
		r.body.Finish(false)
	}
	return n, err
}

func (r *reader) Close() error {
	err := r.ReadCloser.Close()
	r.body.Finish(r.expected >= 0 && r.read == r.expected)
	return err
}
