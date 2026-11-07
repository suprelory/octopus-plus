package relay

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/stream"
	"github.com/suprelory/octopus-plus/polywire/inbound"
)

func TestHandlerEmptyResponsesAreLoggedAsFailures(t *testing.T) {
	for _, mode := range []model.ChannelPassthroughMode{model.ChannelPassthroughModeOff, model.ChannelPassthroughModeAuto} {
		for _, streaming := range []bool{false, true} {
			for _, content := range []struct {
				name string
				text string
			}{
				{"empty", ""},
				{"whitespace", " \n\t "},
			} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", mode, streaming, content.name), func(t *testing.T) {
					ctx := setupHTTPRelayTestDB(t)
					if err := op.SettingSetString(model.SettingKeyEmptyResponseDetectionEnabled, "true"); err != nil {
						t.Fatal(err)
					}
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body := relayTestResponseJSON("resp_empty", content.text)
						w.Header().Set("Content-Type", "application/json")
						if streaming {
							body = relayTestResponseSSE("resp_empty", content.text)
							w.Header().Set("Content-Type", "text/event-stream")
						}
						_, _ = io.WriteString(w, body)
					}))
					defer upstream.Close()
					group := &model.Group{Name: "empty-log", Mode: model.GroupModeFailover}
					channels := addHTTPRelayTestChannels(t, ctx, group, mode, upstream.URL)
					c, recorder := newHTTPRelayTestContext(ctx, fmt.Sprintf(`{"model":"empty-log","input":"hello","stream":%t}`, streaming))
					Handler(inbound.InboundTypeOpenAIResponse, c)

					entry := assertHTTPRelaySettlement(t, ctx, false, channels[0].ID)
					if !strings.Contains(entry.Error, stream.ErrEmptyUpstreamStream.Error()) {
						t.Fatalf("empty response failure reason was lost: %+v", entry)
					}
					if recorder.Code != http.StatusBadGateway {
						t.Fatalf("empty response returned status=%d body=%s", recorder.Code, recorder.Body.String())
					}
					for _, status := range []op.RelayLogStatusFilter{op.RelayLogStatusError, op.RelayLogStatusSuccess} {
						result, err := op.RelayLogListWithFilter(ctx, op.RelayLogListFilter{Status: status, Page: 1, PageSize: 10})
						if err != nil {
							t.Fatal(err)
						}
						wantCount := 0
						if status == op.RelayLogStatusError {
							wantCount = 1
						}
						if len(result.Logs) != wantCount {
							t.Fatalf("status filter %q returned %d logs, want %d", status, len(result.Logs), wantCount)
						}
					}
				})
			}
		}
	}
}

func TestHandlerWhitespaceStreamFailover(t *testing.T) {
	for _, mode := range []model.ChannelPassthroughMode{model.ChannelPassthroughModeOff, model.ChannelPassthroughModeAuto} {
		for _, enabled := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/detection=%t", mode, enabled), func(t *testing.T) {
				ctx := setupHTTPRelayTestDB(t)
				if err := op.SettingSetString(model.SettingKeyEmptyResponseDetectionEnabled, fmt.Sprint(enabled)); err != nil {
					t.Fatal(err)
				}
				primaryBody := relayTestResponseSSE("resp_empty", " \n\t")
				fallbackBody := relayTestResponseStart("resp_fallback") + fmt.Sprintf(
					"data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n",
					" \n\t", relayTestResponseJSON("resp_fallback", " \n\tanswer"))
				var fallbackHits atomic.Int32
				primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, primaryBody)
				}))
				defer primary.Close()
				fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					fallbackHits.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, fallbackBody)
				}))
				defer fallback.Close()
				group := &model.Group{Name: "empty-failover-log", Mode: model.GroupModeFailover}
				channels := addHTTPRelayTestChannels(t, ctx, group, mode, primary.URL, fallback.URL)
				c, recorder := newHTTPRelayTestContext(ctx, `{"model":"empty-failover-log","input":"hello","stream":true}`)
				Handler(inbound.InboundTypeOpenAIResponse, c)
				if recorder.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				var entry model.RelayLog
				if enabled {
					entry = assertHTTPRelaySettlement(t, ctx, true, channels[0].ID, channels[1].ID)
					if fallbackHits.Load() != 1 || strings.Contains(recorder.Body.String(), "resp_empty") || !strings.Contains(recorder.Body.String(), "answer") {
						t.Fatalf("empty response was committed before failover: %s", recorder.Body.String())
					}
					if mode == model.ChannelPassthroughModeAuto && recorder.Body.String() != fallbackBody {
						t.Fatal("failover lost the leading whitespace of the successful passthrough")
					}
					if !strings.Contains(entry.Attempts[0].Msg, stream.ErrEmptyUpstreamStream.Error()) {
						t.Fatalf("empty attempt failure reason was lost: %+v", entry.Attempts[0])
					}
				} else {
					entry = assertHTTPRelaySettlement(t, ctx, true, channels[0].ID)
					if fallbackHits.Load() != 0 {
						t.Fatal("disabled empty response detection still triggered failover")
					}
				}
				if entry.Error != "" {
					t.Fatalf("successful request retained an attempt error: %s", entry.Error)
				}
			})
		}
	}
}
