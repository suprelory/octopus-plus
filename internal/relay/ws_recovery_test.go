package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/transformer"
	"github.com/suprelory/octopus-plus/polywire/inbound"
)

func TestWSReplaySharesBudgetAndSettlement(t *testing.T) {
	for _, mode := range []string{"transform", "passthrough"} {
		for _, scenario := range []string{"recover", "budget_exhausted", "committed"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				ctx := setupHTTPRelayTestDB(t)
				maxAttempts := 2
				if scenario == "budget_exhausted" {
					maxAttempts = 1
				}
				for key, value := range map[dbmodel.SettingKey]string{
					dbmodel.SettingKeyResponsesWSEnabled: "true", dbmodel.SettingKeyResponsesWSDefaultMode: mode,
					dbmodel.SettingKeyRelayMaxTotalAttempts: strconv.Itoa(maxAttempts),
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
						if _, _, err := conn.Read(r.Context()); err != nil {
							return
						}
						wsHits.Add(1)
						if mode == "transform" || scenario == "committed" {
							_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_discarded","model":"model_1"}}`))
						}
						if scenario == "committed" {
							_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.output_text.delta","delta":"visible-once"}`))
						}
						_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"error","status":400,"code":"previous_response_not_found","message":"previous response resp_prev not found"}`))
						return
					}
					httpHits.Add(1)
					body, _ := io.ReadAll(r.Body)
					var request map[string]json.RawMessage
					if err := json.Unmarshal(body, &request); err != nil {
						t.Error(err)
					}
					if _, exists := request["previous_response_id"]; exists || !strings.Contains(string(body), "historical-input") {
						t.Errorf("replay must be self-contained: %s", body)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, relayTestResponseSSE("resp_replayed", "replayed answer"))
				}))
				defer upstream.Close()
				group := &dbmodel.Group{Name: "replay-boundary", Mode: dbmodel.GroupModeFailover, RetryEnabled: true, MaxRetries: 3}
				channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, upstream.URL)
				state := &wsConversationState{
					DownstreamSessionID: "test-session", RequestModel: group.Name, LastResponseID: "resp_prev",
					ChannelID: channels[0].ID, ChannelKeyID: channels[0].Keys[0].ID,
					ReplayWindowItems: json.RawMessage(`[{"role":"user","content":[{"type":"input_text","text":"historical-input"}]}]`),
				}
				client, server := newTestWSConnPair(t)
				defer client.CloseNow()
				defer server.CloseNow()
				requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				frames := make(chan string, 32)
				go func() {
					defer close(frames)
					for {
						_, data, err := client.Read(requestCtx)
						if err != nil {
							return
						}
						frames <- string(data)
					}
				}()
				body := []byte(`{"type":"response.create","model":"replay-boundary","input":"next turn","previous_response_id":"resp_prev"}`)
				processWSResponseCreate(requestCtx, server, body, 7, "", "", "test-session", state)
				_ = server.Close(websocket.StatusNormalClosure, "")
				var output strings.Builder
				for frame := range frames {
					output.WriteString(frame)
				}
				success := scenario == "recover"
				wantHTTP := int32(0)
				ids := []int{channels[0].ID}
				if success {
					wantHTTP = 1
					ids = append(ids, channels[0].ID)
				}
				if wsHits.Load() != 1 || httpHits.Load() != wantHTTP {
					t.Fatalf("unexpected resend: ws=%d http=%d output=%s", wsHits.Load(), httpHits.Load(), output.String())
				}
				entry := assertHTTPRelaySettlement(t, ctx, success, ids...)
				if success {
					if strings.Contains(output.String(), "resp_discarded") || entry.InputTokens != 2 || entry.OutputTokens != 3 {
						t.Fatalf("failed phase polluted final response: output=%s log=%+v", output.String(), entry)
					}
					if trace := entry.Attempts[1]; trace.Transport != "http" || trace.WSMode != dbmodel.RelayLogWSModeReplay || trace.AttemptNum != 2 {
						t.Fatalf("missing replay attempt trace: %+v", trace)
					}
				}
				if scenario == "committed" && strings.Count(output.String(), "previous response resp_prev not found") != 1 {
					t.Fatalf("expected exactly one terminal error after committed payload: %s", output.String())
				}
				assertHTTPRelayKeysReleased(t, channels)
			})
		}
	}
}

func TestWSReplayDeadlineStopsAtCommit(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, relayTestResponseStart("resp_long")+"data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n")
		w.(http.Flusher).Flush()
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			t.Error("committed replay was truncated by its precommit deadline")
			return
		case <-timer.C:
		}
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", relayTestResponseJSON("resp_long", "answer"))
	}))
	defer upstream.Close()
	group := &dbmodel.Group{Name: "long-replay", Mode: dbmodel.GroupModeFailover}
	channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, upstream.URL)
	client, server := newTestWSConnPair(t)
	defer client.CloseNow()
	defer server.CloseNow()
	adapter := transformer.Inbound(inbound.InboundTypeOpenAIResponse)
	body := []byte(`{"model":"long-replay","input":"next","stream":true}`)
	parsed, err := adapter.TransformRequest(ctx, body)
	if err != nil {
		t.Fatal(err)
	}
	state := &wsConversationState{ReplayWindowItems: json.RawMessage(`[{"role":"user","content":"past"}]`)}
	replayed := state.BuildReplayRequest(parsed)
	req, selectedGroup, err := newWSRelayRequest(ctx, server, adapter, 7, group.Name, "", replayed, parsed, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := req.execution.beginReplay(time.Now()); err != nil {
		t.Fatal(err)
	}
	req.execution.budget.deadline = time.Now().Add(150 * time.Millisecond)
	req.execution.replayBudget.deadline = time.Now().Add(100 * time.Millisecond)
	result := runWSRelay(ctx, req, selectedGroup, true)
	if !result.Success || !req.responseCommitted() {
		t.Fatalf("long replay failed: %+v", result)
	}
	finalizeWSRelay(ctx, server, req, result)
	assertHTTPRelaySettlement(t, ctx, true, channels[0].ID)
	assertHTTPRelayKeysReleased(t, channels)
}

func TestWSStaleConnectionResendHonorsBudget(t *testing.T) {
	for _, mode := range []string{"transform", "passthrough"} {
		t.Run(mode, func(t *testing.T) {
			ctx := setupHTTPRelayTestDB(t)
			for key, value := range map[dbmodel.SettingKey]string{
				dbmodel.SettingKeyResponsesWSEnabled: "true", dbmodel.SettingKeyResponsesWSDefaultMode: mode,
			} {
				if err := op.SettingSetString(key, value); err != nil {
					t.Fatal(err)
				}
			}
			var accepted, httpHits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					httpHits.Add(1)
					return
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				accepted.Add(1)
				defer conn.CloseNow()
				_, _, _ = conn.Read(r.Context())
			}))
			defer upstream.Close()
			group := &dbmodel.Group{Name: "stale-budget", Mode: dbmodel.GroupModeFailover, RetryEnabled: true, MaxRetries: 3}
			channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, upstream.URL)
			channel := channels[0]
			stale := TryUpstreamWS(ctx, channel, channel.GetBaseUrl(), channel.Keys[0].ChannelKey, channel.Keys[0].ID, nil, true)
			if stale == nil {
				t.Fatal("failed to prepare stale connection")
			}
			_ = stale.conn.Close(websocket.StatusNormalClosure, "")
			wsUpstreamPool.Put(stale)
			client, server := newTestWSConnPair(t)
			defer client.CloseNow()
			defer server.CloseNow()
			adapter := transformer.Inbound(inbound.InboundTypeOpenAIResponse)
			body := []byte(`{"model":"stale-budget","input":"hello","stream":true}`)
			parsed, err := adapter.TransformRequest(ctx, body)
			if err != nil {
				t.Fatal(err)
			}
			req, selectedGroup, err := newWSRelayRequest(ctx, server, adapter, 7, group.Name, "", parsed, parsed, nil, body)
			if err != nil {
				t.Fatal(err)
			}
			req.execution.budget.maxTotalAttempts = 1
			result := runWSRelay(ctx, req, selectedGroup, true)
			if result.Success || result.Failure.Class != FailureBudgetExceeded || req.execution.budget.totalAttempts != 1 || accepted.Load() != 1 || httpHits.Load() != 0 {
				t.Fatalf("hidden resend escaped budget: result=%+v counted=%d accepted=%d http=%d", result, req.execution.budget.totalAttempts, accepted.Load(), httpHits.Load())
			}
			finalizeWSRelay(ctx, server, req, result)
			assertHTTPRelaySettlement(t, ctx, false, channel.ID)
			assertHTTPRelayKeysReleased(t, channels)
		})
	}
}
