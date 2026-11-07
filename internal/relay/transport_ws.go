package relay

import (
	"context"
	"fmt"
	"github.com/suprelory/octopus-plus/internal/relay/capture"
	"io"
	"net/http"
	"time"

	"github.com/suprelory/octopus-plus/polywire/model"
	openaiOutbound "github.com/suprelory/octopus-plus/polywire/outbound/openai"

	"github.com/coder/websocket"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

// wsUpstreamReader reads events from an upstream WebSocket connection.
type wsUpstreamReader struct {
	conn       *websocket.Conn
	pc         *pooledConn
	channelID  int
	keyID      int
	closed     bool
	done       bool // true after a terminal event has been returned
	statusCode int
	retryAt    time.Time
	capture    *capture.Body
	firstByte  func()
}

func newWSUpstreamReader(pc *pooledConn, channelID, keyID int) *wsUpstreamReader {
	return &wsUpstreamReader{
		conn:       pc.conn,
		pc:         pc,
		channelID:  channelID,
		keyID:      keyID,
		statusCode: 200,
	}
}

func (r *wsUpstreamReader) ReadEvent(ctx context.Context) ([]byte, error) {
	event, err := r.ReadSourceEvent(ctx)
	return event.Data, err
}

func (r *wsUpstreamReader) ReadSourceEvent(ctx context.Context) (model.SourceEvent, error) {
	if r.closed || r.done {
		return model.SourceEvent{}, io.EOF
	}

	msgType, data, err := r.conn.Read(ctx)
	if err != nil {
		// Check if it's a normal close
		closeStatus := websocket.CloseStatus(err)
		if closeStatus == websocket.StatusNormalClosure || closeStatus == websocket.StatusGoingAway {
			return model.SourceEvent{}, io.EOF
		}
		switch closeStatus {
		case websocket.StatusPolicyViolation:
			r.statusCode = http.StatusConflict
		case websocket.StatusTryAgainLater:
			r.statusCode = http.StatusServiceUnavailable
		default:
			if r.statusCode < 400 {
				r.statusCode = http.StatusBadGateway
			}
		}
		return model.SourceEvent{}, fmt.Errorf("ws read error: %w", err)
	}

	if r.firstByte != nil {
		r.firstByte()
		r.firstByte = nil
	}
	r.capture.ObserveFrame(data, msgType == websocket.MessageBinary)
	if msgType != websocket.MessageText {
		return model.SourceEvent{}, fmt.Errorf("unexpected ws message type: %d", msgType)
	}

	observation, parseErr := openaiOutbound.InspectResponseEvent(data, time.Now())
	if parseErr != nil {
		return model.SourceEvent{}, fmt.Errorf("invalid upstream Responses event: %w", parseErr)
	}
	r.done = observation.Terminal
	if r.done {
		r.capture.Finish(true)
	}
	if observation.Error != nil {
		r.statusCode, r.retryAt = observation.Error.Status, observation.Error.RetryAt
		return model.SourceEvent{}, observation.Error
	}
	return observation.Source, nil
}

func (r *wsUpstreamReader) StatusCode() int {
	return r.statusCode
}

func (r *wsUpstreamReader) RetryAt() time.Time {
	return r.retryAt
}

func (r *wsUpstreamReader) Headers() http.Header {
	return http.Header{
		"Content-Type": []string{"text/event-stream"},
	}
}

func (r *wsUpstreamReader) Body() io.ReadCloser {
	return nil // WS doesn't have a body
}

func (r *wsUpstreamReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	// Return connection to pool (don't close it)
	wsUpstreamPool.Put(r.pc)
	log.Debugf("upstream WS connection returned to pool (channel=%d, key=%d)", r.channelID, r.keyID)
	return nil
}

// CloseWithError closes the reader and removes the connection from pool.
func (r *wsUpstreamReader) CloseWithError() {
	if r.closed {
		return
	}
	r.closed = true
	wsUpstreamPool.RemoveConn(r.pc)
}
