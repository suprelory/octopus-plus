package relay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/stream"
	"github.com/suprelory/octopus-plus/polywire/inbound"
)

type manualKeepAliveTimer struct {
	ticks   chan time.Time
	resets  chan time.Duration
	initial time.Duration
	stopped atomic.Bool
}

func installKeepAliveClock(t *testing.T) <-chan *manualKeepAliveTimer {
	t.Helper()
	created := make(chan *manualKeepAliveTimer, 16)
	previous := newHeartbeatTimer
	newHeartbeatTimer = func(delay time.Duration) *heartbeatTimer {
		manual := &manualKeepAliveTimer{
			ticks: make(chan time.Time, 1), resets: make(chan time.Duration, 32), initial: delay,
		}
		created <- manual
		return &heartbeatTimer{
			ch: manual.ticks,
			reset: func(next time.Duration) {
				if next <= 0 {
					select {
					case manual.ticks <- time.Now():
					default:
					}
				}
				manual.resets <- next
			},
			stop: func() { manual.stopped.Store(true) },
		}
	}
	t.Cleanup(func() { newHeartbeatTimer = previous })
	return created
}

func waitKeepAlive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func (m *manualKeepAliveTimer) fire(t *testing.T) {
	t.Helper()
	select {
	case m.ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat tick was not consumed")
	}
}

func (m *manualKeepAliveTimer) awaitWrite(t *testing.T) {
	t.Helper()
	for {
		if waitKeepAlive(t, m.resets, "heartbeat write") > 0 {
			return
		}
	}
}

func releaseKeepAlive(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func TestNonStreamKeepAliveJSONAndErrors(t *testing.T) {
	for _, endpoint := range []string{"responses", "passthrough", "images", "compact"} {
		for _, outcome := range []string{"success", "error_after_heartbeat", "error_before_heartbeat"} {
			t.Run(endpoint+"/"+outcome, func(t *testing.T) {
				ctx := setupHTTPRelayTestDB(t)
				created := installKeepAliveClock(t)
				if err := op.SettingSetString(dbmodel.SettingKeyNonStreamKeepAliveInterval, "30"); err != nil {
					t.Fatal(err)
				}
				started, release := make(chan struct{}), make(chan struct{})
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					close(started)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if outcome != "success" {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, "{\"error\":{\"message\":\"delayed failure\",\"type\":\"invalid_request_error\"}}")
						return
					}
					body := relayTestResponseJSON("resp_keepalive", "final answer")
					if endpoint == "images" {
						body = "{\"data\":[{\"b64_json\":\"final answer\"}]}"
					}
					_, _ = io.WriteString(w, body)
				}))
				defer func() { releaseKeepAlive(release); upstream.Close() }()
				group := &dbmodel.Group{Name: "json-keepalive", Mode: dbmodel.GroupModeFailover}
				mode := dbmodel.ChannelPassthroughModeOff
				if endpoint == "passthrough" {
					mode = dbmodel.ChannelPassthroughModeAuto
				}
				channels := addHTTPRelayTestChannels(t, ctx, group, mode, upstream.URL)
				requestCtx, cancel := context.WithCancel(ctx)
				c, recorder := newHTTPRelayTestContext(requestCtx, "{\"model\":\"json-keepalive\",\"input\":\"hello\",\"prompt\":\"hello\"}")
				done := make(chan struct{})
				go func() {
					defer close(done)
					switch endpoint {
					case "images":
						ImagesHandler("/images/generations", c)
					case "compact":
						HandleResponsesCompact(c)
					default:
						Handler(inbound.InboundTypeOpenAIResponse, c)
					}
				}()
				defer func() { cancel(); waitKeepAlive(t, done, "handler cleanup") }()
				timer := waitKeepAlive(t, created, "JSON timer")
				waitKeepAlive(t, started, "upstream request")
				if outcome != "error_before_heartbeat" {
					timer.fire(t)
					timer.awaitWrite(t)
				}
				close(release)
				waitKeepAlive(t, done, "JSON response")
				body := recorder.Body.String()
				wantStatus := http.StatusOK
				if outcome == "error_before_heartbeat" {
					wantStatus = http.StatusBadRequest
				} else if !strings.HasPrefix(body, "\n") {
					t.Fatalf("missing leading keep-alive: %q", body)
				}
				if recorder.Code != wantStatus || !json.Valid([]byte(body)) {
					t.Fatalf("invalid final JSON response: status=%d body=%q", recorder.Code, body)
				}
				if got := recorder.Result().Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
					t.Fatalf("committed content type = %q", got)
				}
				wantText := "delayed failure"
				if outcome == "success" {
					wantText = "final answer"
				}
				if !strings.Contains(body, wantText) || !timer.stopped.Load() {
					t.Fatalf("response or timer cleanup failed: %q stopped=%v", body, timer.stopped.Load())
				}
				assertHTTPRelaySettlement(t, ctx, outcome == "success", channels[0].ID)
				assertHTTPRelayKeysReleased(t, channels)
			})
		}
	}
}

func TestKeepAliveAcrossStreamFailureAndReconnection(t *testing.T) {
	for _, endpoint := range []string{"responses", "passthrough", "images", "empty_json_image"} {
		t.Run(endpoint, func(t *testing.T) {
			ctx := setupHTTPRelayTestDB(t)
			created := installKeepAliveClock(t)
			setHeartbeatSettings(t, "30", "30")
			if err := op.SettingSetString(dbmodel.SettingKeyNonStreamKeepAliveInterval, "30"); err != nil {
				t.Fatal(err)
			}
			started, release := make(chan struct{}), make(chan struct{})
			fallbackStarted, fallbackRelease := make(chan struct{}), make(chan struct{})
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if endpoint != "empty_json_image" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, ": upstream metadata\n\n")
					w.(http.Flusher).Flush()
				}
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				if endpoint == "images" {
					_, _ = io.WriteString(w, "event: error\ndata: {\"message\":\"retry stream\"}\n\n")
				} else if endpoint != "empty_json_image" {
					_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"code\":\"server_error\",\"message\":\"retry stream\"}\n\n")
				}
			}))
			defer func() { releaseKeepAlive(release); primary.Close() }()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(fallbackStarted)
				select {
				case <-fallbackRelease:
				case <-r.Context().Done():
					return
				}
				body := relayTestResponseSSE("resp_recovered", "recovered answer")
				w.Header().Set("Content-Type", "text/event-stream")
				if endpoint == "images" {
					body = "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"recovered answer\"}\n\n"
				} else if endpoint == "empty_json_image" {
					w.Header().Set("Content-Type", "application/json")
					body = "{\"data\":[{\"b64_json\":\"recovered answer\"}]}"
				}
				_, _ = io.WriteString(w, body)
			}))
			defer func() { releaseKeepAlive(fallbackRelease); fallback.Close() }()
			group := &dbmodel.Group{Name: "heartbeat-recovery", Mode: dbmodel.GroupModeFailover}
			mode := dbmodel.ChannelPassthroughModeOff
			if endpoint == "passthrough" {
				mode = dbmodel.ChannelPassthroughModeAuto
			}
			channels := addHTTPRelayTestChannels(t, ctx, group, mode, primary.URL, fallback.URL)
			requestCtx, cancel := context.WithCancel(ctx)
			body := "{\"model\":\"heartbeat-recovery\",\"input\":\"hello\",\"prompt\":\"hello\",\"stream\":true}"
			if endpoint == "empty_json_image" {
				body = strings.ReplaceAll(body, "\"stream\":true", "\"stream\":false")
			}
			c, recorder := newHTTPRelayTestContext(requestCtx, body)
			done := make(chan struct{})
			go func() {
				defer close(done)
				if endpoint == "images" || endpoint == "empty_json_image" {
					ImagesHandler("/images/generations", c)
				} else {
					Handler(inbound.InboundTypeOpenAIResponse, c)
				}
			}()
			defer func() { cancel(); waitKeepAlive(t, done, "retry cleanup") }()
			timer := waitKeepAlive(t, created, "shared timer")
			waitKeepAlive(t, started, "first upstream")
			timer.fire(t)
			timer.awaitWrite(t)
			close(release)
			waitKeepAlive(t, fallbackStarted, "fallback connection")
			// The fallback has not sent headers: only the resumed waiting writer
			// can answer this tick after the first stream processor has stopped.
			timer.fire(t)
			timer.awaitWrite(t)
			close(fallbackRelease)
			waitKeepAlive(t, done, "fallback result")
			response := recorder.Body.String()
			if !strings.Contains(response, "recovered answer") || strings.Contains(response, "retry stream") {
				t.Fatalf("failed attempt leaked or fallback failed: %q", response)
			}
			if endpoint == "empty_json_image" {
				if !json.Valid([]byte(response)) || !strings.HasPrefix(response, "\n\n") {
					t.Fatalf("invalid JSON image after heartbeats: %q", response)
				}
			} else if strings.Count(response, ":\n\n") != 2 {
				t.Fatalf("missing heartbeat across reconnect: %q", response)
			}
			select {
			case <-created:
				t.Fatal("stream handoff replaced the request heartbeat timer")
			default:
			}
			assertHTTPRelaySettlement(t, ctx, true, channels[0].ID, channels[1].ID)
			assertHTTPRelayKeysReleased(t, channels)
		})
	}
}

func TestInStreamHeartbeatPreservesTerminalSSEError(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	created := installKeepAliveClock(t)
	setHeartbeatSettings(t, "30", "0")
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"code\":\"server_error\",\"message\":\"failure after heartbeat\"}\n\n")
	}))
	defer func() { releaseKeepAlive(release); upstream.Close() }()
	group := &dbmodel.Group{Name: "heartbeat-error", Mode: dbmodel.GroupModeFailover}
	addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, upstream.URL)
	requestCtx, cancel := context.WithCancel(ctx)
	c, recorder := newHTTPRelayTestContext(requestCtx, "{\"model\":\"heartbeat-error\",\"input\":\"hello\",\"stream\":true}")
	done := make(chan struct{})
	go func() { defer close(done); Handler(inbound.InboundTypeOpenAIResponse, c) }()
	defer func() { cancel(); waitKeepAlive(t, done, "error cleanup") }()
	timer := waitKeepAlive(t, created, "in-stream timer")
	timer.fire(t)
	timer.awaitWrite(t)
	close(release)
	waitKeepAlive(t, done, "terminal SSE error")
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.HasPrefix(body, ":\n\n") ||
		!strings.Contains(body, "event: error\ndata: ") || !strings.Contains(body, "failure after heartbeat") {
		t.Fatalf("error did not preserve SSE after in-stream heartbeat: status=%d body=%q", recorder.Code, body)
	}
}

func TestWaitingHeartbeatWriteFailureCancelsRequest(t *testing.T) {
	setupRelayTestDB(t)
	created := installKeepAliveClock(t)
	setHeartbeatSettings(t, "30", "30")
	c, _ := newTestGinContext(t)
	c.Writer = disconnectedCaptureWriter{ResponseWriter: c.Writer}
	hb := startEarlyHeartbeat(c, true)
	defer hb.Stop()
	timer := waitKeepAlive(t, created, "timer")
	timer.fire(t)
	waitKeepAlive(t, c.Request.Context().Done(), "downstream cancellation")
	if !errors.Is(context.Cause(c.Request.Context()), stream.ErrDownstreamWrite) {
		t.Fatalf("unexpected cancellation cause: %v", context.Cause(c.Request.Context()))
	}
}

func TestNonStreamKeepAliveDisabledByDefault(t *testing.T) {
	setupRelayTestDB(t)
	created := installKeepAliveClock(t)
	c, recorder := newTestGinContext(t)
	hb := startEarlyHeartbeat(c, false)
	hb.Stop()
	select {
	case <-created:
		t.Fatal("default JSON request created a keep-alive timer")
	default:
	}
	if recorder.Body.Len() != 0 || c.Writer.Written() {
		t.Fatal("disabled JSON keep-alive committed a response")
	}
}
