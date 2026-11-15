package stream

import (
	"context"
	"io"
	"sync"
)

// RawSource reads raw bytes in fixed-size chunks (for passthrough).
type RawSource struct {
	reader     io.ReadCloser
	bufSize    int
	sequence   int64
	readMu     sync.Mutex
	closeOnce  sync.Once
	closeErr   error
	pendingErr error
}

// NewRawSource creates a source that reads raw chunks.
func NewRawSource(reader io.ReadCloser, bufSize int) *RawSource {
	if bufSize <= 0 {
		bufSize = 32 * 1024 // 32KB default
	}
	return &RawSource{
		reader:  reader,
		bufSize: bufSize,
	}
}

// ReadSourceEvent returns raw chunks with a stable source identity and sequence
// number. Raw chunks intentionally have no event type.
func (s *RawSource) ReadSourceEvent(ctx context.Context) (SourceEvent, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if err := ctx.Err(); err != nil {
		return SourceEvent{}, err
	}
	if s.pendingErr != nil {
		return SourceEvent{}, s.pendingErr
	}
	buf := make([]byte, s.bufSize)
	n, err := s.reader.Read(buf)
	if n > 0 {
		s.pendingErr = err
		s.sequence++
		return SourceEvent{Data: buf[:n], Sequence: s.sequence, Transport: SourceTransportRaw}, nil
	}
	if err != nil {
		return SourceEvent{}, err
	}
	return SourceEvent{}, nil
}

// Close releases the underlying reader.
func (s *RawSource) Close() error {
	s.closeOnce.Do(func() {
		if s.reader != nil {
			s.closeErr = s.reader.Close()
		}
	})
	return s.closeErr
}
