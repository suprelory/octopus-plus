package relay

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/suprelory/octopus-plus/internal/utils/safe"
)

const wsClientPendingMessages = 8

type wsKeepAliveConnection interface {
	Ping(context.Context) error
	CloseNow() error
}

type wsClientMessage struct {
	kind websocket.MessageType
	data []byte
}

// coder/websocket processes Ping/Pong and close frames inside Read. Keep one
// reader active even while a turn waits for an upstream response, and bound the
// queued application messages by both count and total bytes.
type wsClientReader struct {
	conn     *websocket.Conn
	messages chan wsClientMessage
	done     chan struct{}
	cancel   context.CancelFunc
	pending  atomic.Int64
	once     sync.Once
}

func newWSClientReader(ctx context.Context, conn *websocket.Conn, cancelSession context.CancelCauseFunc) *wsClientReader {
	readCtx, cancel := context.WithCancel(ctx)
	r := &wsClientReader{
		conn: conn, messages: make(chan wsClientMessage, wsClientPendingMessages),
		done: make(chan struct{}), cancel: cancel,
	}
	safe.Go("relay-ws-client-read", func() {
		defer close(r.done)
		defer close(r.messages)
		fail := func(err error) {
			cancelSession(err)
			_ = conn.CloseNow()
		}
		for {
			kind, data, err := conn.Read(readCtx)
			if err != nil {
				fail(err)
				return
			}
			if kind != websocket.MessageText {
				continue
			}
			size := int64(len(data))
			if r.pending.Add(size) > wsClientReadLimit {
				r.pending.Add(-size)
				fail(fmt.Errorf("websocket pending request bytes exceed %d", wsClientReadLimit))
				return
			}
			select {
			case r.messages <- wsClientMessage{kind: kind, data: data}:
			case <-readCtx.Done():
				r.pending.Add(-size)
				return
			default:
				r.pending.Add(-size)
				fail(fmt.Errorf("websocket pending requests exceed %d", wsClientPendingMessages))
				return
			}
		}
	})
	return r
}

func (r *wsClientReader) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	if err := context.Cause(ctx); err != nil {
		return 0, nil, err
	}
	select {
	case <-ctx.Done():
		return 0, nil, context.Cause(ctx)
	case msg, ok := <-r.messages:
		if !ok {
			if err := context.Cause(ctx); err != nil {
				return 0, nil, err
			}
			return 0, nil, io.EOF
		}
		r.pending.Add(-int64(len(msg.data)))
		if err := context.Cause(ctx); err != nil {
			return 0, nil, err
		}
		return msg.kind, msg.data, nil
	}
}

func (r *wsClientReader) Close() {
	r.once.Do(func() {
		r.cancel()
		_ = r.conn.CloseNow()
		<-r.done
	})
}

// Session-wide control frames keep both active turns and idle connections alive.
// A missing Pong cancels the session and its active upstream request.
func startWSKeepAlive(ctx context.Context, conn wsKeepAliveConnection, interval time.Duration, cancelSession context.CancelCauseFunc) func() {
	if interval <= 0 {
		return func() {}
	}
	keepAliveCtx, cancel := context.WithCancel(ctx)
	timer := newHeartbeatTimer(interval)
	done := make(chan struct{})
	safe.Go("relay-ws-keepalive", func() {
		defer close(done)
		defer timer.stop()
		for {
			select {
			case <-keepAliveCtx.Done():
				return
			case <-timer.ch:
				if keepAliveCtx.Err() != nil {
					return
				}
				pingCtx, pingCancel := context.WithTimeout(keepAliveCtx, wsWriteTimeout)
				err := conn.Ping(pingCtx)
				pingCancel()
				if err != nil {
					if keepAliveCtx.Err() == nil {
						cancelSession(fmt.Errorf("websocket keep-alive failed: %w", err))
						_ = conn.CloseNow()
					}
					return
				}
				timer.reset(interval)
			}
		}
	})
	return func() {
		cancel()
		<-done
	}
}
