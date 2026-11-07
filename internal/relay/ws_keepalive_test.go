package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
)

func TestWebSocketKeepAliveDuringTurnsAndIdle(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	created := installKeepAliveClock(t)
	setHeartbeatSettings(t, "30", "0")
	key := dbmodel.APIKey{APIKey: "sk-octopus-ws-keepalive-test", Enabled: true}
	if err := op.APIKeyCreate(&key, ctx); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var turns atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		turn := turns.Add(1)
		if turn == 1 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, relayTestResponseSSE(fmt.Sprintf("resp_%d", turn), "answer"))
	}))
	defer func() { releaseKeepAlive(release); upstream.Close() }()
	group := &dbmodel.Group{Name: "ws-keepalive", Mode: dbmodel.GroupModeFailover}
	addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, upstream.URL)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlerDone := make(chan struct{})
	router.GET("/v1/responses", middleware.APIKeyWSAuth(), func(c *gin.Context) {
		defer close(handlerDone)
		HandleWSResponse(c)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	wsCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pings := make(chan struct{}, 8)
	conn, _, err := websocket.Dial(wsCtx, server.URL+"/v1/responses", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + key.APIKey}},
		OnPingReceived: func(context.Context, []byte) bool {
			pings <- struct{}{}
			return true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan []byte, 128)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer close(events)
		for {
			_, data, err := conn.Read(wsCtx)
			if err != nil {
				return
			}
			select {
			case events <- data:
			case <-wsCtx.Done():
				return
			}
		}
	}()
	defer func() {
		_ = conn.CloseNow()
		cancel()
		waitKeepAlive(t, handlerDone, "WebSocket handler cleanup")
		waitKeepAlive(t, readDone, "WebSocket client cleanup")
	}()
	timer := waitKeepAlive(t, created, "WebSocket timer")
	pulse := func() {
		t.Helper()
		timer.fire(t)
		waitKeepAlive(t, pings, "Ping control frame")
		timer.awaitWrite(t) // A reset proves that the server also consumed the Pong.
	}
	pulse() // Idle before the first request.
	select {
	case data := <-events:
		t.Fatalf("keep-alive produced an application frame: %s", data)
	default:
	}
	request := []byte("{\"type\":\"response.create\",\"model\":\"ws-keepalive\",\"input\":\"hello\"}")
	if err := conn.Write(wsCtx, websocket.MessageText, request); err != nil {
		t.Fatal(err)
	}
	waitKeepAlive(t, started, "active upstream turn")
	pulse()
	// Queue a second turn while the first is blocked; control frames must still
	// be read, and application requests must remain ordered.
	if err := conn.Write(wsCtx, websocket.MessageText, request); err != nil {
		t.Fatal(err)
	}
	pulse()
	close(release)
	for completed := 1; completed <= 2; {
		data := waitKeepAlive(t, events, "response event")
		var event map[string]any
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("invalid WebSocket JSON: %q: %v", data, err)
		}
		if event["type"] == "error" || event["type"] == "response.failed" {
			t.Fatalf("unexpected response error: %s", data)
		}
		if event["type"] != "response.completed" {
			continue
		}
		response, _ := event["response"].(map[string]any)
		if response["id"] != fmt.Sprintf("resp_%d", completed) {
			t.Fatalf("queued turns lost ordering: %s", data)
		}
		completed++
	}
	pulse() // Idle after both turns.
	if turns.Load() != 2 {
		t.Fatalf("upstream turns = %d", turns.Load())
	}
}

type testKeepAliveConn struct {
	ping   func(context.Context) error
	closed atomic.Bool
}

func (c *testKeepAliveConn) Ping(ctx context.Context) error { return c.ping(ctx) }
func (c *testKeepAliveConn) CloseNow() error {
	c.closed.Store(true)
	return nil
}

func TestWebSocketKeepAliveFailureCancelsSession(t *testing.T) {
	created := installKeepAliveClock(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	deadlineSet := make(chan bool, 1)
	conn := &testKeepAliveConn{ping: func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		deadlineSet <- ok && time.Until(deadline) <= wsWriteTimeout
		return context.DeadlineExceeded
	}}
	stop := startWSKeepAlive(ctx, conn, 30*time.Second, cancel)
	defer stop()
	timer := waitKeepAlive(t, created, "WebSocket timer")
	timer.fire(t)
	waitKeepAlive(t, ctx.Done(), "failed Ping cancellation")
	stop()
	if !waitKeepAlive(t, deadlineSet, "Ping deadline") || !conn.closed.Load() ||
		!errors.Is(context.Cause(ctx), context.DeadlineExceeded) || !timer.stopped.Load() {
		t.Fatalf("failed Ping did not close/cancel the session: %v", context.Cause(ctx))
	}
}

func TestWebSocketKeepAliveStopInterruptsPing(t *testing.T) {
	created := installKeepAliveClock(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	pinging := make(chan struct{})
	conn := &testKeepAliveConn{ping: func(ctx context.Context) error {
		close(pinging)
		<-ctx.Done()
		return ctx.Err()
	}}
	stop := startWSKeepAlive(ctx, conn, time.Second, cancel)
	defer stop()
	waitKeepAlive(t, created, "WebSocket timer").fire(t)
	waitKeepAlive(t, pinging, "pending Ping")
	stop()
	if ctx.Err() != nil || conn.closed.Load() {
		t.Fatal("normal keep-alive cleanup was reported as a failed session")
	}
}

func TestWebSocketKeepAliveDisabled(t *testing.T) {
	created := installKeepAliveClock(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	startWSKeepAlive(ctx, nil, 0, cancel)()
	select {
	case <-created:
		t.Fatal("disabled WebSocket keep-alive created a timer")
	default:
	}
}
