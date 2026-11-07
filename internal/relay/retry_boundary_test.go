package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/internal/transformer"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	transformerModel "github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestHTTPAndWSShareRetryBoundaries(t *testing.T) {
	tests := []struct {
		name         string
		primaryHits  int32
		fallbackHits int32
		success      bool
	}{
		{"transient", 2, 1, true},
		{"first_token_timeout", 1, 1, true},
		{"rate_limit", 1, 1, true},
		{"rate_limit_at_channel_limit", 2, 0, true},
		{"rate_limit_without_usable_fallback", 2, 0, true},
		{"total_attempt_limit", 1, 0, false},
		{"deadline", 1, 0, false},
		{"metadata_only", 2, 1, true},
		{"committed_failure", 1, 0, false},
		{"downstream_write_failure", 1, 0, false},
	}
	for _, ingress := range []string{"http", "ws"} {
		for _, test := range tests {
			t.Run(ingress+"/"+test.name, func(t *testing.T) {
				ctx := setupHTTPRelayTestDB(t)
				if test.name == "committed_failure" || test.name == "downstream_write_failure" {
					if err := op.SettingSetString(dbmodel.SettingKeyCircuitBreakerThreshold, "1"); err != nil {
						t.Fatal(err)
					}
				}
				var primaryHits, fallbackHits atomic.Int32
				primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					hit := primaryHits.Add(1)
					switch test.name {
					case "first_token_timeout", "deadline":
						select {
						case <-r.Context().Done():
						case <-time.After(3 * time.Second):
							t.Error("upstream request was not canceled")
						}
					case "rate_limit", "rate_limit_at_channel_limit", "rate_limit_without_usable_fallback":
						if test.name != "rate_limit" && hit == 2 {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, relayTestResponseSSE("resp_primary", "answer"))
							return
						}
						w.Header().Set("Retry-After", "0")
						http.Error(w, `{"error":{"message":"rate limited"}}`, http.StatusTooManyRequests)
					case "metadata_only":
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, relayTestResponseStart("resp_discarded"))
					case "committed_failure":
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, relayTestResponseStart("resp_partial")+"data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
					case "downstream_write_failure":
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, relayTestResponseSSE("resp_success", "answer"))
					default:
						w.Header().Set("Retry-After", "0")
						http.Error(w, `{"error":{"message":"temporary failure"}}`, http.StatusServiceUnavailable)
					}
				}))
				defer primary.Close()
				fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					fallbackHits.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, relayTestResponseSSE("resp_fallback", "answer"))
				}))
				defer fallback.Close()
				group := &dbmodel.Group{Name: "shared-retry", Mode: dbmodel.GroupModeFailover, RetryEnabled: true, MaxRetries: 2, FirstTokenTimeOut: 1}
				channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, primary.URL, fallback.URL)
				if test.name == "rate_limit_without_usable_fallback" {
					disabled := false
					if _, err := op.ChannelUpdate(&dbmodel.ChannelUpdateRequest{ID: channels[1].ID, Enabled: &disabled}, ctx); err != nil {
						t.Fatal(err)
					}
				}
				requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				body := `{"model":"shared-retry","input":"hello","stream":true}`
				c, recorder := newHTTPRelayTestContext(requestCtx, body)
				if test.name == "downstream_write_failure" {
					c.Writer = &failingRelayWriter{ResponseWriter: c.Writer}
				}
				var req *relayRequest
				var run func()
				if ingress == "http" {
					relay := prepareHTTPRelay(inbound.InboundTypeOpenAIResponse, c)
					if relay == nil {
						t.Fatalf("prepare HTTP: %s", recorder.Body.String())
					}
					req = relay.request
					defer req.heartbeat.Stop()
					run = relay.run
				} else {
					client, server := newTestWSConnPair(t)
					defer client.CloseNow()
					defer server.CloseNow()
					adapter := transformer.Inbound(inbound.InboundTypeOpenAIResponse)
					parsed, err := adapter.TransformRequest(requestCtx, []byte(body))
					if err != nil {
						t.Fatal(err)
					}
					var selectedGroup *dbmodel.Group
					req, selectedGroup, err = newWSRelayRequest(requestCtx, server, adapter, 7, group.Name, "", parsed, parsed, nil, []byte(body))
					if err != nil {
						t.Fatal(err)
					}
					req.streamWriter = c.Writer
					run = func() {
						result := runWSRelay(requestCtx, req, selectedGroup, true)
						finalizeWSRelay(requestCtx, server, req, result)
					}
				}
				req.execution.emptyResponseDetection = true
				if test.name == "total_attempt_limit" {
					req.execution.budget.maxTotalAttempts = 1
				}
				if test.name == "rate_limit_at_channel_limit" {
					req.execution.budget.maxChannelAttempts = 1
				}
				if test.name == "deadline" {
					req.execution.budget.deadline = time.Now().Add(50 * time.Millisecond)
				}
				run()
				if primaryHits.Load() != test.primaryHits || fallbackHits.Load() != test.fallbackHits {
					t.Fatalf("hits primary=%d fallback=%d, want %d/%d", primaryHits.Load(), fallbackHits.Load(), test.primaryHits, test.fallbackHits)
				}
				if got := req.execution.budget.totalAttempts; got != int(test.primaryHits+test.fallbackHits) {
					t.Fatalf("counted %d attempts, actual sends %d", got, test.primaryHits+test.fallbackHits)
				}
				var ids []int
				for i := int32(0); i < test.primaryHits; i++ {
					ids = append(ids, channels[0].ID)
				}
				if test.fallbackHits > 0 {
					ids = append(ids, channels[1].ID)
				}
				assertHTTPRelaySettlement(t, ctx, test.success, ids...)
				assertHTTPRelayKeysReleased(t, channels)
				if test.name == "committed_failure" || test.name == "downstream_write_failure" {
					tripped, _ := balancer.IsTripped(channels[0].ID, channels[0].Keys[0].ID, "model_1")
					if tripped != (test.name == "committed_failure") {
						t.Fatalf("breaker must distinguish upstream failure from downstream write failure: tripped=%t", tripped)
					}
				}
				if strings.Contains(recorder.Body.String(), "resp_discarded") {
					t.Fatalf("failed attempt metadata leaked: %s", recorder.Body.String())
				}
			})
		}
	}
}

type failingRelayWriter struct{ gin.ResponseWriter }

func (w *failingRelayWriter) Write(data []byte) (int, error) {
	return len(data) / 2, fmt.Errorf("downstream connection lost during write")
}

// Exercise the shared retry owner in the existing stale-connection tests. A
// single forwardViaWS call now sends once and returns its recovery suggestion.
func runWSRetryTestAttempt(t *testing.T, attempt *relayAttempt) (int, error) {
	t.Helper()
	if err := op.SettingSetString(dbmodel.SettingKeyResponsesWSEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	req := attempt.relayRequest
	req.ctx, req.streamWriter, req.c = context.Background(), req.c.Writer, nil
	req.inboundType = inbound.InboundTypeOpenAIResponse
	req.internalRequest.RawAPIFormat = transformerModel.APIFormatOpenAIResponse
	attempt.channel.WSMode = dbmodel.ChannelWSModeTransform
	group := dbmodel.Group{RetryEnabled: true, MaxRetries: 3, Items: []dbmodel.GroupItem{{ChannelID: attempt.channel.ID, ModelName: req.internalRequest.Model}}}
	req.execution = newRelayExecution(group, true)
	req.iter = balancer.NewIterator(group, req.apiKeyID, req.requestModel)
	defer req.iter.Close()
	if !req.iter.Next() {
		t.Fatal("missing test candidate")
	}
	result, _ := (&relayExecutor{request: req}).runChannelAttempts(attempt.channel, attempt.usedKey, func() {}, req.internalRequest.Model, outbound.CapabilityDecision{})
	if !result.Success {
		return result.StatusCode, result.Err
	}
	if req.execution.budget.totalAttempts != 2 || len(req.attempts()) != 2 {
		t.Fatalf("reconnect submissions were not counted/logged independently: count=%d attempts=%+v", req.execution.budget.totalAttempts, req.attempts())
	}
	if trace := req.attempts()[1]; trace.Transport != "ws" || trace.Recovery != dbmodel.RelayLogWSRecoveryReconnect {
		t.Fatalf("missing reconnect trace: %+v", trace)
	}
	return http.StatusOK, nil
}

func TestHTTPAndWSCancelRetryBackoff(t *testing.T) {
	for _, ingress := range []string{"http", "ws"} {
		t.Run(ingress, func(t *testing.T) {
			ctx := setupHTTPRelayTestDB(t)
			var hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Retry-After", "60")
				http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			}))
			defer upstream.Close()
			group := &dbmodel.Group{Name: "cancel-shared", Mode: dbmodel.GroupModeFailover, RetryEnabled: true, MaxRetries: 3}
			channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, upstream.URL)
			requestCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			c, _ := newHTTPRelayTestContext(requestCtx, `{"model":"cancel-shared","input":"hello","stream":true}`)
			relay := prepareHTTPRelay(inbound.InboundTypeOpenAIResponse, c)
			defer relay.request.heartbeat.Stop()
			if ingress == "ws" {
				relay.request.ctx, relay.request.streamWriter, relay.request.c = requestCtx, c.Writer, nil
			}
			done := make(chan relayOutcome, 1)
			go func() { done <- (&relayExecutor{request: relay.request}).run() }()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			for op.StatsChannelGet(channels[0].ID).RequestFailed == 0 {
				select {
				case <-ticker.C:
				case <-deadline.C:
					cancel()
					t.Fatal("first attempt did not finish")
				}
			}
			cancel()
			select {
			case outcome := <-done:
				if !outcome.result.Canceled || hits.Load() != 1 {
					t.Fatalf("cancel result=%+v hits=%d", outcome.result, hits.Load())
				}
			case <-time.After(time.Second):
				t.Fatal(fmt.Sprintf("%s backoff ignored cancellation", ingress))
			}
			assertHTTPRelayKeysReleased(t, channels)
		})
	}
}
