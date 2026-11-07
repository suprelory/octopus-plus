package capture

import (
	"github.com/suprelory/octopus-plus/internal/model"
	"time"
)

const maxEvents = 256

// EnableSSE is called when downstream headers are committed, before observing
// their first body bytes. Parsing recognizes LF, CRLF and CR across reads.
func (b *Body) EnableSSE() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sse = true
}

func (b *Body) appendEvent(offset, size int64, kind string, complete bool) {
	if len(b.message.Events) >= maxEvents {
		b.message.EventsTruncated = true
		return
	}
	b.message.Events = append(b.message.Events, model.RelayMessageEvent{
		Sequence: len(b.message.Events) + 1, Offset: offset, Bytes: size,
		ElapsedMS: time.Since(b.started).Milliseconds(), Type: kind, Complete: complete,
	})
}

func (b *Body) observeSSE(p []byte) {
	for _, v := range p {
		if b.pendingCR {
			b.pendingCR = false
			if v == '\n' {
				b.position++
				if b.boundary {
					b.appendEvent(b.eventStart, b.position-b.eventStart, "sse", true)
					b.eventStart = b.position
				}
				continue
			}
			if b.boundary {
				b.appendEvent(b.eventStart, b.position-b.eventStart, "sse", true)
				b.eventStart = b.position
			}
		}
		b.position++
		switch v {
		case '\r', '\n':
			b.boundary = b.lineBytes == 0
			b.lineBytes = 0
			if v == '\r' {
				b.pendingCR = true
			} else if b.boundary {
				b.appendEvent(b.eventStart, b.position-b.eventStart, "sse", true)
				b.eventStart = b.position
			}
		default:
			b.lineBytes++
		}
	}
}

// ObserveFrame records only messages accepted by the WS transport. Binary bytes
// stay intact; the content endpoint can return base64 when UTF-8 is invalid.
func (b *Body) ObserveFrame(p []byte, binary bool) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return
	}
	offset := b.message.CapturedBytes
	b.observe(p)
	if b.limit == 0 {
		return
	}
	kind := "ws_text"
	if binary {
		kind = "ws_binary"
	}
	n := b.message.CapturedBytes - offset
	b.appendEvent(offset, n, kind, n == int64(len(p)))
	if n != int64(len(p)) {
		b.message.EventsTruncated = true
	}
}
