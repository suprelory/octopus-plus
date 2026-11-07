package relay

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestHandlerAnthropicStreamCompletion(t *testing.T) {
	for _, mode := range []struct {
		name        string
		inboundType inbound.InboundType
		passthrough dbmodel.ChannelPassthroughMode
	}{
		{"openai conversion", inbound.InboundTypeOpenAIChat, dbmodel.ChannelPassthroughModeOff},
		{"anthropic conversion", inbound.InboundTypeAnthropic, dbmodel.ChannelPassthroughModeOff},
		{"anthropic passthrough", inbound.InboundTypeAnthropic, dbmodel.ChannelPassthroughModeAuto},
	} {
		for _, completion := range []struct {
			name          string
			last          string
			eventTypeOnly bool
			success       bool
		}{
			{"message stop without reason", `{"type":"message_stop"}`, false, true},
			{"message stop event type with empty data", "", true, true},
			{"EOF without terminal marker", "", false, false},
		} {
			t.Run(mode.name+"/"+completion.name, func(t *testing.T) {
				ctx := setupHTTPRelayTestDB(t)
				var hits atomic.Int32
				var raw strings.Builder
				for _, event := range []string{
					`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}`,
					`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
					`{"type":"content_block_stop","index":0}`,
					`{"type":"message_delta","usage":{"output_tokens":3}}`,
				} {
					fmt.Fprintf(&raw, "data: %s\n\n", event)
				}
				if completion.eventTypeOnly {
					raw.WriteString("event: message_stop\ndata:\n\n")
				} else if completion.last != "" {
					fmt.Fprintf(&raw, "data: %s\n\n", completion.last)
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					for _, event := range strings.Split(raw.String(), "\n\n") {
						if event != "" {
							fmt.Fprintf(w, "%s\n\n", event)
							w.(http.Flusher).Flush()
						}
					}
				}))
				defer upstream.Close()

				group := &dbmodel.Group{Name: "anthropic-completion", Mode: dbmodel.GroupModeFailover, RetryEnabled: true, MaxRetries: 2}
				if err := op.GroupCreate(group, ctx); err != nil {
					t.Fatal(err)
				}
				var channels []*dbmodel.Channel
				for index := 0; index < 2; index++ {
					channel := &dbmodel.Channel{
						Name: fmt.Sprintf("anthropic-%d", index), Type: outbound.OutboundTypeAnthropic,
						Enabled: true, BaseUrls: []dbmodel.BaseUrl{{URL: upstream.URL + "/v1"}}, Model: "claude-test",
						PassthroughMode: mode.passthrough, Keys: []dbmodel.ChannelKey{{Enabled: true, ChannelKey: "test-key"}},
					}
					if err := op.ChannelCreate(channel, ctx); err != nil {
						t.Fatal(err)
					}
					if err := op.GroupItemAdd(&dbmodel.GroupItem{GroupID: group.ID, ChannelID: channel.ID, ModelName: "claude-test", Priority: index}, ctx); err != nil {
						t.Fatal(err)
					}
					channels = append(channels, channel)
				}
				c, recorder := newHTTPRelayTestContext(ctx, `{"model":"anthropic-completion","messages":[{"role":"user","content":"hello"}],"max_tokens":64,"stream":true}`)

				Handler(mode.inboundType, c)

				if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "hello") {
					t.Fatalf("stream response: status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				if got := hits.Load(); got != 1 {
					t.Fatalf("committed response caused %d upstream attempts, want 1", got)
				}
				assertHTTPRelaySettlement(t, ctx, completion.success, channels[0].ID)
				assertHTTPRelayKeysReleased(t, channels)
				if completion.success {
					if mode.passthrough == dbmodel.ChannelPassthroughModeAuto {
						if recorder.Body.String() != raw.String() {
							t.Fatalf("passthrough response changed: %s", recorder.Body.String())
						}
					} else if mode.inboundType == inbound.InboundTypeOpenAIChat {
						if !strings.Contains(recorder.Body.String(), `"finish_reason":"stop"`) || strings.Count(recorder.Body.String(), "data: [DONE]") != 1 {
							t.Fatalf("missing or repeated completion: %s", recorder.Body.String())
						}
					} else if strings.Count(recorder.Body.String(), `"type":"message_stop"`) != 1 {
						t.Fatalf("missing or repeated message_stop: %s", recorder.Body.String())
					}
				}
			})
		}
	}
}
