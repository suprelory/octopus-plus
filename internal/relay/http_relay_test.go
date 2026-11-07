package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestHandlerSameChannelRetriesBeforeFailover(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	var primaryHits, fallbackHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits.Add(1)
		w.Header().Set("Retry-After", "0")
		http.Error(w, `{"error":{"message":"retry this channel","type":"server_error"}}`, http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits.Add(1)
		if primaryHits.Load() != 2 {
			t.Errorf("fallback started before same-channel retries completed: hits=%d", primaryHits.Load())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, relayTestResponseJSON("resp_fallback", "fallback answer"))
	}))
	defer fallback.Close()
	group := &dbmodel.Group{Name: "same-channel-retry", Mode: dbmodel.GroupModeFailover, RetryEnabled: true, MaxRetries: 2}
	channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, primary.URL, fallback.URL)
	c, recorder := newHTTPRelayTestContext(ctx, `{"model":"same-channel-retry","input":"hello"}`)

	Handler(inbound.InboundTypeOpenAIResponse, c)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "fallback answer") {
		t.Fatalf("fallback response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if primaryHits.Load() != 2 || fallbackHits.Load() != 1 {
		t.Fatalf("upstream hits: primary=%d fallback=%d", primaryHits.Load(), fallbackHits.Load())
	}
	assertHTTPRelaySettlement(t, ctx, true, channels[0].ID, channels[0].ID, channels[1].ID)
	assertHTTPRelayKeysReleased(t, channels)
	if stats := op.StatsChannelGet(channels[0].ID); stats.RequestFailed != 2 || stats.RequestSuccess != 0 {
		t.Fatalf("primary attempt statistics: %+v", stats)
	}
}

func TestHandlerCancellationDuringRetryBackoffReleasesKey(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	var primaryHits, fallbackHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits.Add(1)
		w.Header().Set("Retry-After", "60")
		http.Error(w, `{"error":{"message":"try later","type":"server_error"}}`, http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits.Add(1)
		http.Error(w, "unexpected fallback", http.StatusInternalServerError)
	}))
	defer fallback.Close()
	group := &dbmodel.Group{Name: "cancel-backoff", Mode: dbmodel.GroupModeFailover, RetryEnabled: true, MaxRetries: 3}
	channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, primary.URL, fallback.URL)
	requestCtx, cancel := context.WithCancel(ctx)
	c, recorder := newHTTPRelayTestContext(requestCtx, `{"model":"cancel-backoff","input":"hello"}`)
	done := make(chan struct{})
	go func() {
		defer close(done)
		Handler(inbound.InboundTypeOpenAIResponse, c)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("handler did not stop after cancellation")
		}
	}()

	// Wait for the failed attempt to be settled, while Retry-After keeps the
	// next attempt pending. Cancellation must interrupt that wait.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for op.StatsChannelGet(channels[0].ID).RequestFailed == 0 {
		select {
		case <-done:
			t.Fatal("handler returned before the retry backoff could be canceled")
		case <-deadline.C:
			t.Fatal("first attempt was not settled")
		case <-ticker.C:
		}
	}
	if got := balancer.InFlightKeyCount(channels[0].Keys[0].ID); got != 1 {
		t.Fatalf("key reservation during backoff = %d, want 1", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not interrupt Retry-After")
	}

	if primaryHits.Load() != 1 || fallbackHits.Load() != 0 {
		t.Fatalf("canceled request was retried: primary=%d fallback=%d", primaryHits.Load(), fallbackHits.Load())
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("canceled request wrote a response: %s", recorder.Body.String())
	}
	entry := assertHTTPRelaySettlement(t, ctx, false, channels[0].ID)
	if !strings.Contains(entry.Error, context.Canceled.Error()) {
		t.Fatalf("final error = %q, want cancellation", entry.Error)
	}
	assertHTTPRelayKeysReleased(t, channels)
}

func TestHandlerDoesNotRetryAfterStreamPayload(t *testing.T) {
	for _, mode := range []dbmodel.ChannelPassthroughMode{dbmodel.ChannelPassthroughModeOff, dbmodel.ChannelPassthroughModeAuto} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := setupHTTPRelayTestDB(t)
			var primaryHits, fallbackHits atomic.Int32
			committed := make(chan struct{})
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryHits.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, relayTestResponseStart("resp_partial")+"data: {\"type\":\"response.output_text.delta\",\"delta\":\"visible-once\"}\n\n")
				w.(http.Flusher).Flush()
				// Separate the provider error from the already committed payload,
				// including when the passthrough source reads raw network chunks.
				select {
				case <-committed:
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"code\":\"server_error\",\"message\":\"failed after payload\"}\n\n")
			}))
			defer primary.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackHits.Add(1)
				http.Error(w, "unexpected fallback", http.StatusInternalServerError)
			}))
			defer fallback.Close()
			group := &dbmodel.Group{Name: "committed-stream", Mode: dbmodel.GroupModeFailover, RetryEnabled: true, MaxRetries: 3}
			channels := addHTTPRelayTestChannels(t, ctx, group, mode, primary.URL, fallback.URL)
			requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			c, recorder := newHTTPRelayTestContext(requestCtx, `{"model":"committed-stream","input":"hello","stream":true}`)
			c.Writer = &relayPayloadSignalWriter{ResponseWriter: c.Writer, committed: committed}

			Handler(inbound.InboundTypeOpenAIResponse, c)

			body := recorder.Body.String()
			if strings.Count(body, `"delta":"visible-once"`) != 1 || !strings.Contains(body, "failed after payload") {
				t.Fatalf("expected one payload followed by the protocol error: %s", body)
			}
			if primaryHits.Load() != 1 || fallbackHits.Load() != 0 {
				t.Fatalf("committed stream was retried: primary=%d fallback=%d", primaryHits.Load(), fallbackHits.Load())
			}
			entry := assertHTTPRelaySettlement(t, ctx, false, channels[0].ID)
			if !strings.Contains(entry.ResponseContent, "visible-once") {
				t.Fatalf("partial response was not collected: %+v", entry)
			}
			if state := loadResponsesReplayState(7, group.ID, group.Name, "resp_partial"); state != nil {
				t.Fatalf("failed stream became a replay anchor: %+v", state)
			}
			assertHTTPRelayKeysReleased(t, channels)
		})
	}
}

func TestHandlerStreamPrecommitFailuresStayIsolated(t *testing.T) {
	for _, mode := range []dbmodel.ChannelPassthroughMode{dbmodel.ChannelPassthroughModeOff, dbmodel.ChannelPassthroughModeAuto} {
		for _, failure := range []string{"empty", "metadata_only", "first_token_timeout"} {
			t.Run(string(mode)+"/"+failure, func(t *testing.T) {
				ctx := setupHTTPRelayTestDB(t)
				if err := op.SettingSetString(dbmodel.SettingKeyEmptyResponseDetectionEnabled, "true"); err != nil {
					t.Fatal(err)
				}
				var primaryHits, fallbackHits atomic.Int32
				primaryDone := make(chan struct{}, 4)
				primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					primaryHits.Add(1)
					defer func() { primaryDone <- struct{}{} }()
					w.Header().Set("Content-Type", "text/event-stream")
					if failure == "empty" {
						return
					}
					_, _ = io.WriteString(w, relayTestResponseStart("resp_discarded"))
					if failure == "first_token_timeout" {
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						return
					}
					_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_discarded\",\"model\":\"discarded-model\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":999,\"output_tokens\":999,\"total_tokens\":1998}}}\n\n")
				}))
				defer primary.Close()
				successSSE := relayTestResponseSSE("resp_success", "fallback answer")
				fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					fallbackHits.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, successSSE)
				}))
				defer fallback.Close()
				group := &dbmodel.Group{Name: "stream-precommit", Mode: dbmodel.GroupModeFailover, FirstTokenTimeOut: 1}
				if failure == "first_token_timeout" {
					group.RetryEnabled, group.MaxRetries = true, 3
				}
				channels := addHTTPRelayTestChannels(t, ctx, group, mode, primary.URL, fallback.URL)
				requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				c, recorder := newHTTPRelayTestContext(requestCtx, `{"model":"stream-precommit","input":"hello","stream":true}`)

				Handler(inbound.InboundTypeOpenAIResponse, c)

				body := recorder.Body.String()
				if recorder.Code != http.StatusOK || !strings.Contains(body, "fallback answer") || strings.Contains(body, "resp_discarded") || strings.Contains(body, "999") {
					t.Fatalf("failed attempt leaked into fallback: status=%d body=%s", recorder.Code, body)
				}
				if mode == dbmodel.ChannelPassthroughModeAuto && body != successSSE {
					t.Fatalf("fallback passthrough bytes changed: %s", body)
				}
				if primaryHits.Load() != 1 || fallbackHits.Load() != 1 {
					t.Fatalf("upstream hits: primary=%d fallback=%d", primaryHits.Load(), fallbackHits.Load())
				}
				entry := assertHTTPRelaySettlement(t, ctx, true, channels[0].ID, channels[1].ID)
				if entry.InputTokens != 2 || entry.OutputTokens != 3 || strings.Contains(entry.ResponseContent, "resp_discarded") {
					t.Fatalf("failed attempt polluted final usage/response: %+v", entry)
				}
				if failure == "first_token_timeout" && !strings.Contains(entry.Attempts[0].Msg, "first token timeout") {
					t.Fatalf("metadata incorrectly satisfied the first-token deadline: %+v", entry.Attempts)
				}
				select {
				case <-primaryDone:
				case <-time.After(time.Second):
					t.Fatal("failed upstream request was not closed")
				}
				assertHTTPRelayKeysReleased(t, channels)
			})
		}
	}
}

func setupHTTPRelayTestDB(t *testing.T) context.Context {
	t.Helper()
	ctx := setupRelayTestDB(t)
	// The log writer's in-memory buffer outlives the temporary database.
	if err := op.RelayLogClear(ctx); err != nil {
		t.Fatal(err)
	}
	resetResponsesReplayStore()
	t.Cleanup(resetResponsesReplayStore)
	return ctx
}

func addHTTPRelayTestChannels(t *testing.T, ctx context.Context, group *dbmodel.Group, mode dbmodel.ChannelPassthroughMode, urls ...string) []*dbmodel.Channel {
	t.Helper()
	if err := op.GroupCreate(group, ctx); err != nil {
		t.Fatal(err)
	}
	channels := make([]*dbmodel.Channel, 0, len(urls))
	for index, url := range urls {
		channel := &dbmodel.Channel{
			Name: fmt.Sprintf("%s-%d", group.Name, index), Type: outbound.OutboundTypeOpenAIResponse,
			Enabled: true, BaseUrls: []dbmodel.BaseUrl{{URL: url + "/v1"}}, Model: "model_1",
			PassthroughMode: mode, Keys: []dbmodel.ChannelKey{{Enabled: true, ChannelKey: fmt.Sprintf("key-%d", index)}},
		}
		if err := op.ChannelCreate(channel, ctx); err != nil {
			t.Fatal(err)
		}
		if err := op.GroupItemAdd(&dbmodel.GroupItem{GroupID: group.ID, ChannelID: channel.ID, ModelName: "model_1", Priority: index + 1, Weight: 1}, ctx); err != nil {
			t.Fatal(err)
		}
		channels = append(channels, channel)
	}
	return channels
}

func newHTTPRelayTestContext(ctx context.Context, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("api_key_id", 7)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	return c, recorder
}

func assertHTTPRelaySettlement(t *testing.T, ctx context.Context, success bool, channelIDs ...int) dbmodel.RelayLog {
	t.Helper()
	logs, err := op.RelayLogList(ctx, nil, nil, nil, 1, 10)
	if err != nil || len(logs) != 1 {
		t.Fatalf("expected one relay log, got %d: %v", len(logs), err)
	}
	entry := logs[0]
	if entry.Success != success || len(entry.Attempts) != len(channelIDs) {
		t.Fatalf("unexpected final result: %+v", entry)
	}
	for index, channelID := range channelIDs {
		status := dbmodel.AttemptFailed
		if success && index == len(channelIDs)-1 {
			status = dbmodel.AttemptSuccess
		}
		if attempt := entry.Attempts[index]; attempt.ChannelID != channelID || attempt.Status != status {
			t.Fatalf("attempt %d: %+v, want channel %d status %s", index, attempt, channelID, status)
		}
	}
	stats := op.StatsTotalGet()
	wantSuccess := int64(0)
	if success {
		wantSuccess = 1
	}
	if stats.RequestSuccess != wantSuccess || stats.RequestFailed != 1-wantSuccess {
		t.Fatalf("request was not settled exactly once: %+v", stats)
	}
	return entry
}

func assertHTTPRelayKeysReleased(t *testing.T, channels []*dbmodel.Channel) {
	t.Helper()
	for _, channel := range channels {
		for _, key := range channel.Keys {
			if got := balancer.InFlightKeyCount(key.ID); got != 0 {
				t.Errorf("channel %d key %d still reserved: %d", channel.ID, key.ID, got)
			}
		}
	}
}

type relayPayloadSignalWriter struct {
	gin.ResponseWriter
	committed chan struct{}
	once      sync.Once
}

func (w *relayPayloadSignalWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if strings.Contains(string(data[:n]), `"delta":"visible-once"`) {
		w.once.Do(func() { close(w.committed) })
	}
	return n, err
}

func relayTestResponseStart(id string) string {
	return fmt.Sprintf("data: {\"type\":\"response.created\",\"response\":{\"id\":%q,\"object\":\"response\",\"model\":\"model_1\",\"created_at\":1,\"output\":[],\"status\":\"in_progress\"}}\n\n", id)
}

func relayTestResponseJSON(id, text string) string {
	return fmt.Sprintf(`{"id":%q,"object":"response","model":"model_1","created_at":1,"status":"completed","output":[{"id":%q,"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q,"annotations":[]}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`, id, "msg_"+id, text)
}

func relayTestResponseSSE(id, text string) string {
	return relayTestResponseStart(id) + fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", text, relayTestResponseJSON(id, text))
}
