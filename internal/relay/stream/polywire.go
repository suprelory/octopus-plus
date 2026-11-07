package stream

import (
	"io"

	"github.com/suprelory/octopus-plus/polywire/streamio"
)

// Protocol framing is provided by Polywire; relay retains connection ownership,
// downstream commitment, retries, timeouts and heartbeat policy.
type SourceEvent = streamio.SourceEvent
type SourceEventSource = streamio.SourceEventSource
type TypedStreamSource = streamio.TypedStreamSource
type SSESource = streamio.SSESource
type SSEDecoder = streamio.SSEDecoder
type SSEEventObserver = streamio.SSEEventObserver
type SourceEventObserver = streamio.SourceEventObserver
type IncrementalSSEObserver = streamio.IncrementalSSEObserver

const (
	SourceTransportSSE       = streamio.SourceTransportSSE
	SourceTransportWebSocket = streamio.SourceTransportWebSocket
	SourceTransportRaw       = streamio.SourceTransportRaw
)

func NewSSESource(reader io.ReadCloser, maxEventSize int) *SSESource {
	return streamio.NewSSESource(reader, maxEventSize)
}

func NewSSEDecoder(maxEventSize int) *SSEDecoder {
	return streamio.NewSSEDecoder(maxEventSize)
}

func NewIncrementalSSEObserver(maxEventSize int, terminal map[string]struct{}, observe SSEEventObserver) *IncrementalSSEObserver {
	return streamio.NewIncrementalSSEObserver(maxEventSize, terminal, observe)
}

func NewIncrementalSourceEventObserver(maxEventSize int, terminal map[string]struct{}, observe SourceEventObserver) *IncrementalSSEObserver {
	return streamio.NewIncrementalSourceEventObserver(maxEventSize, terminal, observe)
}

func NormalizeEventData(eventType string, data []byte) []byte {
	return streamio.NormalizeEventData(eventType, data)
}
