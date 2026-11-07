package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/stream"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/utils/safe"
)

// A request keeps the same timer when ownership moves between connection setup,
// stream processing and retry backoff. Only the current owner consumes ticks.
type heartbeatTimer struct {
	ch    <-chan time.Time
	reset func(time.Duration)
	stop  func()
}

var newHeartbeatTimer = func(delay time.Duration) *heartbeatTimer {
	timer := time.NewTimer(delay)
	return &heartbeatTimer{
		ch:    timer.C,
		reset: func(delay time.Duration) { timer.Reset(delay) },
		stop:  func() { timer.Stop() },
	}
}

type earlyHeartbeat struct {
	c             *gin.Context
	ctx           context.Context
	cancelRequest context.CancelCauseFunc
	interval      time.Duration
	isStream      bool
	timer         *heartbeatTimer
	nextAt        time.Time

	// control serializes handoff/resume/stop; mu protects response writes.
	control sync.Mutex
	mu      sync.Mutex
	handed  atomic.Bool
	stopped atomic.Bool
	cancel  context.CancelFunc
	done    chan struct{}

	headerSet atomic.Bool
}

func startEarlyHeartbeat(c *gin.Context, isStream bool) *earlyHeartbeat {
	h := &earlyHeartbeat{c: c, isStream: isStream, done: make(chan struct{})}
	close(h.done)
	h.handed.Store(true)
	if c == nil || c.Request == nil {
		return h
	}

	var interval, delay int
	if isStream {
		interval, _ = op.SettingGetInt(dbmodel.SettingKeySSEHeartbeatInterval)
		delay, _ = op.SettingGetInt(dbmodel.SettingKeySSEPreStreamHeartbeatDelay)
	} else {
		interval, _ = op.SettingGetInt(dbmodel.SettingKeyNonStreamKeepAliveInterval)
		delay = interval
	}
	if interval <= 0 {
		return h
	}
	h.interval = time.Duration(interval) * time.Second
	h.ctx, h.cancelRequest = context.WithCancelCause(c.Request.Context())
	c.Request = c.Request.WithContext(h.ctx)
	if delay > 0 {
		h.timer = newHeartbeatTimer(time.Duration(delay) * time.Second)
		h.nextAt = time.Now().Add(time.Duration(delay) * time.Second)
		h.Resume()
	}
	return h
}

// Resume returns timer ownership to the waiting phase after an uncommitted
// upstream stream fails. It preserves the next heartbeat deadline.
func (h *earlyHeartbeat) Resume() {
	if h == nil {
		return
	}
	h.control.Lock()
	defer h.control.Unlock()
	if h.stopped.Load() || !h.handed.Load() || h.timer == nil || h.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(h.ctx)
	h.cancel = cancel
	h.done = make(chan struct{})
	h.handed.Store(false)
	safe.Go("relay-waiting-heartbeat", func() {
		defer close(h.done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-h.timer.ch:
				if ctx.Err() != nil {
					// The next owner must still receive a tick consumed at handoff.
					h.resetTimer(0)
					return
				}
				h.mu.Lock()
				h.writeHeaderLocked()
				err := h.writeHeartbeatLocked()
				h.mu.Unlock()
				if err != nil {
					h.cancelRequest(fmt.Errorf("%w: %w", stream.ErrDownstreamWrite, err))
					return
				}
				h.resetTimer(h.interval)
			}
		}
	})
}

func (h *earlyHeartbeat) resetTimer(delay time.Duration) {
	h.nextAt = time.Now().Add(delay)
	h.timer.reset(delay)
}

func (h *earlyHeartbeat) writeHeaderLocked() {
	if !h.headerSet.CompareAndSwap(false, true) {
		return
	}
	w := h.c.Writer
	contentType := "application/json"
	if h.isStream {
		contentType = "text/event-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Del("Content-Length")
	w.WriteHeader(http.StatusOK)
}

func (h *earlyHeartbeat) writeHeartbeatLocked() error {
	payload := "\n"
	if h.isStream {
		payload = ":\n\n"
	}
	n, err := h.c.Writer.Write([]byte(payload))
	if err != nil {
		return err
	}
	if n != len(payload) {
		return io.ErrShortWrite
	}
	h.c.Writer.Flush()
	return nil
}

// streamConfig hands the shared timer to the synchronous stream writer, so
// heartbeat bytes never race with response frames or restart the interval.
func (h *earlyHeartbeat) streamConfig(config stream.StreamConfig) stream.StreamConfig {
	if _, isWS := config.Writer.(*WSStreamWriter); isWS {
		// The WebSocket session sends control Ping frames, including between turns.
		config.HeartbeatInterval = 0
		return config
	}
	if h == nil {
		return config
	}
	h.Hand()
	h.control.Lock()
	defer h.control.Unlock()
	config.HeartbeatInterval = 0
	if h.stopped.Load() || h.interval <= 0 || !h.isStream {
		return config
	}
	if h.timer == nil {
		h.timer = newHeartbeatTimer(h.interval)
		h.nextAt = time.Now().Add(h.interval)
	} else if time.Until(h.nextAt) > h.interval {
		h.resetTimer(h.interval)
	}
	config.HeartbeatC = h.timer.ch
	config.OnHeartbeat = func() {
		h.headerSet.Store(true)
		h.resetTimer(h.interval)
	}
	return config
}

// Hand pauses the background writer without destroying the shared schedule.
func (h *earlyHeartbeat) Hand() {
	if h == nil {
		return
	}
	h.control.Lock()
	defer h.control.Unlock()
	h.handLocked()
}

func (h *earlyHeartbeat) handLocked() {
	h.handed.Store(true)
	if h.cancel != nil {
		h.cancel()
	}
	<-h.done
}

func (h *earlyHeartbeat) Stop() {
	if h == nil {
		return
	}
	h.control.Lock()
	defer h.control.Unlock()
	h.stopped.Store(true)
	h.handLocked()
	if h.timer != nil {
		h.timer.stop()
	}
}

// Handoff stops the background writer and reports whether an SSE response has
// already been committed, including a heartbeat sent by the stream processor.
func (h *earlyHeartbeat) Handoff() bool {
	if h == nil {
		return false
	}
	h.Hand()
	return h.isStream && h.headerSet.Load()
}

func (h *earlyHeartbeat) HeaderWritten() bool {
	return h != nil && h.headerSet.Load()
}

func (h *earlyHeartbeat) WriteSSEError(statusCode int, message string) {
	if h == nil || h.c == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"code":    statusCode,
		"message": message,
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	_, _ = h.c.Writer.Write([]byte("event: error\ndata: "))
	_, _ = h.c.Writer.Write(payload)
	_, _ = h.c.Writer.Write([]byte("\n\n"))
	h.c.Writer.Flush()
}

func (h *earlyHeartbeat) FlushOrError(c *gin.Context, statusCode int, message string) {
	if h != nil && h.Handoff() {
		h.WriteSSEError(statusCode, message)
		return
	}
	resp.Error(c, statusCode, message)
}

func streamHeartbeatInterval() time.Duration {
	interval, err := op.SettingGetInt(dbmodel.SettingKeySSEHeartbeatInterval)
	if err != nil || interval <= 0 {
		return 0
	}
	return time.Duration(interval) * time.Second
}

func newStreamHeartbeatTicker() (*time.Ticker, <-chan time.Time) {
	interval := streamHeartbeatInterval()
	if interval <= 0 {
		return nil, nil
	}
	ticker := time.NewTicker(interval)
	return ticker, ticker.C
}
