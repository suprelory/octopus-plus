package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/polywire/inbound"
)

func TestNativeContinuationPreservesProviderErrors(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		code         string
		publicCode   string
		message      string
		retryEnabled bool
		attempts     int32
		hasReplay    bool
		channels     int
		recover      bool
	}{
		{"rate limit without retry", 429, "rate_limit_exceeded", "upstream_rate_limited", "try again later", false, 1, false, 1, false},
		{"rate limit after retries", 429, "rate_limit_exceeded", "upstream_rate_limited", "try again later", true, 2, true, 2, false},
		{"context limit", 400, "context_length_exceeded", "context_length_exceeded", "maximum context length exceeded", true, 1, true, 2, false},
		{"rate limit then success", 429, "rate_limit_exceeded", "upstream_rate_limited", "try again later", true, 2, false, 2, true},
	}
	for _, ingress := range []string{"http", "ws transform", "ws passthrough"} {
		for _, test := range tests {
			t.Run(ingress+"/"+test.name, func(t *testing.T) {
				ctx := setupHTTPRelayTestDB(t)
				mode := "transform"
				if ingress == "ws passthrough" {
					mode = "passthrough"
				}
				for key, value := range map[dbmodel.SettingKey]string{
					dbmodel.SettingKeyResponsesWSEnabled:     "true",
					dbmodel.SettingKeyResponsesWSDefaultMode: mode,
				} {
					if err := op.SettingSetString(key, value); err != nil {
						t.Fatal(err)
					}
				}
				var wsConnections, wsHits, httpHits, fallbackHits atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
						httpHits.Add(1)
						http.Error(w, "unexpected HTTP replay", http.StatusBadRequest)
						return
					}
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						return
					}
					wsConnections.Add(1)
					defer conn.CloseNow()
					for {
						_, body, err := conn.Read(r.Context())
						if err != nil {
							return
						}
						var request struct {
							PreviousResponseID string `json:"previous_response_id"`
						}
						if err := json.Unmarshal(body, &request); err != nil || request.PreviousResponseID != "resp_prev" {
							t.Errorf("native continuation changed: %s, error=%v", body, err)
						}
						hit := wsHits.Add(1)
						if test.recover && hit == test.attempts {
							for _, event := range strings.Split(relayTestResponseSSE("resp_next", "answer"), "\n\n") {
								if event != "" {
									if err := conn.Write(r.Context(), websocket.MessageText, []byte(strings.TrimPrefix(event, "data: "))); err != nil {
										return
									}
								}
							}
							continue
						}
						payload := fmt.Sprintf(`{"type":"error","status":%d,"retry_after":0,"error":{"code":%q,"message":%q}}`, test.status, test.code, test.message)
						if err := conn.Write(r.Context(), websocket.MessageText, []byte(payload)); err != nil {
							return
						}
					}
				}))
				defer upstream.Close()
				fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					fallbackHits.Add(1)
					http.Error(w, "native continuation must not switch channels", http.StatusBadRequest)
				}))
				defer fallback.Close()
				group := &dbmodel.Group{Name: "native-provider-error", Mode: dbmodel.GroupModeFailover, RetryEnabled: test.retryEnabled, MaxRetries: 2}
				urls := []string{upstream.URL}
				if test.channels > 1 {
					urls = append(urls, fallback.URL)
				}
				channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, urls...)
				requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				body := `{"model":"native-provider-error","input":"next turn","previous_response_id":"resp_prev","stream":true}`
				if ingress == "http" {
					c, recorder := newHTTPRelayTestContext(requestCtx, body)
					Handler(inbound.InboundTypeOpenAIResponse, c)
					status := test.status
					if test.recover {
						status = http.StatusOK
					}
					if recorder.Code != status {
						t.Errorf("HTTP status=%d, want %d: %s", recorder.Code, status, recorder.Body.String())
					}
				} else {
					state := &wsConversationState{
						DownstreamSessionID: "test-session", RequestModel: group.Name, LastResponseID: "resp_prev",
						ChannelID: channels[0].ID, ChannelKeyID: channels[0].Keys[0].ID,
					}
					if test.hasReplay {
						state.ReplayWindowItems = json.RawMessage(`[{"role":"user","content":"past turn"}]`)
					}
					storeWSConversationState(7, group.Name, state, time.Minute)
					client, server := newTestWSConnPair(t)
					defer client.CloseNow()
					defer server.CloseNow()
					nextState := processWSResponseCreate(requestCtx, server, []byte(body), 7, "", "", state.DownstreamSessionID, state)
					if !test.recover {
						_, frame, err := client.Read(requestCtx)
						if err != nil {
							t.Fatal(err)
						}
						var response struct {
							Type   string `json:"type"`
							Status int    `json:"status"`
							Error  struct {
								Code string `json:"code"`
							} `json:"error"`
						}
						if err := json.Unmarshal(frame, &response); err != nil {
							t.Fatal(err)
						}
						if response.Type != "error" || response.Status != test.status || response.Error.Code != test.publicCode {
							t.Errorf("provider error was replaced: %s", frame)
						}
						storedState := loadWSConversationState(7, group.Name, state.DownstreamSessionID)
						if !reflect.DeepEqual(nextState, state) || !reflect.DeepEqual(storedState, state) {
							t.Errorf("provider error changed conversation state: next=%+v stored=%+v", nextState, storedState)
						}
					} else if nextState == nil || nextState.LastResponseID != "resp_next" {
						t.Errorf("same-channel retry failed: state=%+v", nextState)
					}
				}
				if wsHits.Load() != test.attempts || httpHits.Load() != 0 || fallbackHits.Load() != 0 {
					t.Errorf("unexpected attempts: WS=%d, want %d; HTTP replay=%d; fallback=%d", wsHits.Load(), test.attempts, httpHits.Load(), fallbackHits.Load())
				}
				if wsConnections.Load() != 1 || wsUpstreamPool.ShouldSkipWS(channels[0].ID) {
					t.Errorf("provider error invalidated WS transport: connections=%d, skipped=%t", wsConnections.Load(), wsUpstreamPool.ShouldSkipWS(channels[0].ID))
				}
				ids := make([]int, test.attempts)
				for i := range ids {
					ids[i] = channels[0].ID
				}
				assertHTTPRelaySettlement(t, ctx, test.recover, ids...)
				assertHTTPRelayKeysReleased(t, channels)
			})
		}
	}
}

func TestNativeContinuationTransportFailureAllowsReplay(t *testing.T) {
	for _, mode := range []string{"transform", "passthrough"} {
		for _, retryEnabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retry=%t", mode, retryEnabled), func(t *testing.T) {
				ctx := setupHTTPRelayTestDB(t)
				for key, value := range map[dbmodel.SettingKey]string{
					dbmodel.SettingKeyResponsesWSEnabled:     "true",
					dbmodel.SettingKeyResponsesWSDefaultMode: mode,
				} {
					if err := op.SettingSetString(key, value); err != nil {
						t.Fatal(err)
					}
				}
				var wsHits, httpHits atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
						conn, err := websocket.Accept(w, r, nil)
						if err != nil {
							return
						}
						defer conn.CloseNow()
						if _, _, err := conn.Read(r.Context()); err == nil {
							wsHits.Add(1)
						}
						return
					}
					httpHits.Add(1)
					var request map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if _, exists := request["previous_response_id"]; exists || !strings.Contains(string(request["input"]), "historical-input") {
						t.Errorf("replay must be self-contained: %v", request)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, relayTestResponseSSE("resp_replayed", "replayed answer"))
				}))
				defer upstream.Close()
				group := &dbmodel.Group{Name: "native-transport-error", Mode: dbmodel.GroupModeFailover, RetryEnabled: retryEnabled, MaxRetries: 2}
				channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, upstream.URL, upstream.URL)
				// Native continuation uses affinity; replay may choose the higher-priority channel.
				original, fallback := channels[1], channels[0]
				state := &wsConversationState{
					DownstreamSessionID: "test-session", RequestModel: group.Name, LastResponseID: "resp_prev",
					ChannelID: original.ID, ChannelKeyID: original.Keys[0].ID,
					ReplayWindowItems: json.RawMessage(`[{"role":"user","content":"historical-input"}]`),
				}
				client, server := newTestWSConnPair(t)
				defer client.CloseNow()
				defer server.CloseNow()
				requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				body := []byte(`{"model":"native-transport-error","input":"next turn","previous_response_id":"resp_prev","stream":true}`)
				nextState := processWSResponseCreate(requestCtx, server, body, 7, "", "", state.DownstreamSessionID, state)
				if nextState == nil || nextState.LastResponseID != "resp_replayed" || nextState.ChannelID != fallback.ID {
					t.Errorf("transport failure did not recover through replay: state=%+v", nextState)
				}
				ids := []int{original.ID}
				if retryEnabled {
					ids = append(ids, original.ID)
				}
				if wsHits.Load() != int32(len(ids)) || httpHits.Load() != 1 {
					t.Errorf("unexpected attempts: WS=%d, want %d; HTTP replay=%d, want 1", wsHits.Load(), len(ids), httpHits.Load())
				}
				ids = append(ids, fallback.ID)
				entry := assertHTTPRelaySettlement(t, ctx, true, ids...)
				if trace := entry.Attempts[len(ids)-1]; trace.Transport != "http" || trace.WSMode != dbmodel.RelayLogWSModeReplay {
					t.Errorf("missing replay attempt trace: %+v", trace)
				}
				assertHTTPRelayKeysReleased(t, channels)
			})
		}
	}
}
