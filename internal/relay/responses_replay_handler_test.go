package relay

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/polywire/inbound"
)

func TestHandlerResponsesReplaySurvivesFailoverAcrossTurns(t *testing.T) {
	for _, mode := range []dbmodel.ChannelPassthroughMode{dbmodel.ChannelPassthroughModeOff, dbmodel.ChannelPassthroughModeAuto} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", mode, streaming), func(t *testing.T) {
				ctx := setupHTTPRelayTestDB(t)
				var primaryHits, fallbackHits atomic.Int32
				requests := make(chan []byte, 8)
				captureRequest := func(r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read upstream request: %v", err)
					}
					requests <- body
				}
				writeSuccess := func(w http.ResponseWriter, turn int) {
					id, answer := fmt.Sprintf("resp_turn%d", turn), fmt.Sprintf("answer-%d", turn)
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, relayTestResponseSSE(id, answer))
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, relayTestResponseJSON(id, answer))
					}
				}
				primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					captureRequest(r)
					if primaryHits.Add(1) == 1 {
						writeSuccess(w, 1)
						return
					}
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, relayTestResponseStart("resp_failed")+"data: {\"type\":\"error\",\"code\":\"server_error\",\"message\":\"failed continuation\"}\n\n")
					} else {
						http.Error(w, `{"id":"resp_failed","error":{"message":"failed continuation","type":"server_error"}}`, http.StatusServiceUnavailable)
					}
				}))
				defer primary.Close()
				fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					captureRequest(r)
					writeSuccess(w, int(fallbackHits.Add(1))+1)
				}))
				defer fallback.Close()
				group := &dbmodel.Group{Name: "http-replay-turns", Mode: dbmodel.GroupModeFailover, SessionKeepTime: 300}
				channels := addHTTPRelayTestChannels(t, ctx, group, mode, primary.URL, fallback.URL)

				var wantHistory []string
				for turn := 1; turn <= 3; turn++ {
					previous := ""
					if turn > 1 {
						previous = fmt.Sprintf(`,"previous_response_id":"resp_turn%d"`, turn-1)
					}
					if turn == 3 {
						// Remove routing affinity and breaker state: the saved replay
						// state alone must prefer the channel that completed turn 2.
						balancer.Reset()
					}
					body := fmt.Sprintf(`{"model":%q,"input":"turn-%d","stream":%t%s}`, group.Name, turn, streaming, previous)
					c, recorder := newHTTPRelayTestContext(ctx, body)
					Handler(inbound.InboundTypeOpenAIResponse, c)
					if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), fmt.Sprintf("answer-%d", turn)) || strings.Contains(recorder.Body.String(), "resp_failed") {
						t.Fatalf("turn %d: status=%d body=%s", turn, recorder.Code, recorder.Body.String())
					}

					wantHistory = append(wantHistory, fmt.Sprintf("user:turn-%d", turn))
					attempts := 1
					if turn == 2 {
						attempts = 2
					}
					for index := 0; index < attempts; index++ {
						select {
						case request := <-requests:
							assertHTTPReplayInput(t, request, wantHistory)
						default:
							t.Fatalf("missing upstream request for turn %d attempt %d", turn, index)
						}
					}
					if len(requests) != 0 {
						t.Fatalf("unexpected additional attempts for turn %d", turn)
					}
					wantHistory = append(wantHistory, fmt.Sprintf("assistant:answer-%d", turn))
					state := loadResponsesReplayState(7, group.ID, group.Name, fmt.Sprintf("resp_turn%d", turn))
					channel := channels[0]
					if turn > 1 {
						channel = channels[1]
					}
					if state == nil || state.ChannelID != channel.ID || state.ChannelKeyID != channel.Keys[0].ID || state.LastResponseID != fmt.Sprintf("resp_turn%d", turn) {
						t.Fatalf("turn %d did not save the successful response/channel/key: %+v", turn, state)
					}
					assertHTTPReplayInput(t, []byte(fmt.Sprintf(`{"model":"model_1","input":%s}`, state.ReplayWindowItems)), wantHistory)
					if failed := loadResponsesReplayState(7, group.ID, group.Name, "resp_failed"); failed != nil {
						t.Fatalf("failed attempt became a replay anchor: %+v", failed)
					}
					assertHTTPRelayKeysReleased(t, channels)
				}

				if primaryHits.Load() != 2 || fallbackHits.Load() != 2 {
					t.Fatalf("replay routing: primary=%d fallback=%d", primaryHits.Load(), fallbackHits.Load())
				}
				firstState := loadResponsesReplayState(7, group.ID, group.Name, "resp_turn1")
				if firstState == nil || firstState.ChannelID != channels[0].ID {
					t.Fatalf("later turns mutated the original replay anchor: %+v", firstState)
				}
				assertHTTPReplayInput(t, []byte(fmt.Sprintf(`{"model":"model_1","input":%s}`, firstState.ReplayWindowItems)), []string{"user:turn-1", "assistant:answer-1"})
				logs, err := op.RelayLogList(ctx, nil, nil, nil, 1, 10)
				if err != nil || len(logs) != 3 {
					t.Fatalf("expected one log per turn, got %d: %v", len(logs), err)
				}
				for _, entry := range logs {
					if !entry.Success || entry.InputTokens != 2 || entry.OutputTokens != 3 {
						t.Fatalf("replay usage/result was not settled once: %+v", entry)
					}
					if strings.Contains(entry.RequestContent, "previous_response_id") && (entry.WSMode == nil || *entry.WSMode != dbmodel.RelayLogWSModeReplay || entry.WSRecovery == nil || *entry.WSRecovery != dbmodel.RelayLogWSRecoveryReplay) {
						t.Fatalf("continuation was not logged as replay: %+v", entry)
					}
					if strings.Contains(entry.RequestContent, `"input":"turn-2"`) && (len(entry.Attempts) != 2 || entry.Attempts[0].Status != dbmodel.AttemptFailed || entry.Attempts[1].Status != dbmodel.AttemptSuccess) {
						t.Fatalf("failover turn lost its attempt history: %+v", entry.Attempts)
					}
				}
				if stats := op.StatsTotalGet(); stats.RequestSuccess != 3 || stats.RequestFailed != 0 || stats.InputToken != 6 || stats.OutputToken != 9 {
					t.Fatalf("replay request totals: %+v", stats)
				}
			})
		}
	}
}

func assertHTTPReplayInput(t *testing.T, request []byte, want []string) {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(request, &payload); err != nil {
		t.Fatalf("invalid upstream request: %v", err)
	}
	if _, ok := payload["previous_response_id"]; ok {
		t.Fatalf("replay kept previous_response_id: %s", request)
	}
	if string(payload["model"]) != `"model_1"` {
		t.Fatalf("upstream model was not mapped: %s", request)
	}
	var inputText string
	var got []string
	if err := json.Unmarshal(payload["input"], &inputText); err == nil {
		got = []string{"user:" + inputText}
	} else {
		var items []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(payload["input"], &items); err != nil {
			t.Fatalf("invalid replay input: %v", err)
		}
		for _, item := range items {
			var content string
			if err := json.Unmarshal(item.Content, &content); err != nil {
				var parts []struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(item.Content, &parts); err != nil {
					t.Fatalf("invalid replay content: %v", err)
				}
				for _, part := range parts {
					content += part.Text
				}
			}
			got = append(got, item.Role+":"+content)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replay history = %v, want %v; request=%s", got, want, request)
	}
}
