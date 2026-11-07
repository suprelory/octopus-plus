package relay

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestHandlerPrefersNativeSemanticsBeforeAvailabilityFallback(t *testing.T) {
	for _, protocol := range []struct {
		name     string
		inbound  inbound.InboundType
		outbound outbound.OutboundType
		path     string
		body     string
		response string
	}{
		{
			name: "Responses", inbound: inbound.InboundTypeOpenAIResponse, outbound: outbound.OutboundTypeOpenAIResponse,
			path:     "/v1/responses",
			body:     `{"model":"native-fallback","input":"retained prompt","tools":[{"type":"web_search"}]}`,
			response: `{"id":"resp_1","object":"response","model":"upstream","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"native works"}]}],"status":"completed"}`,
		},
		{
			name: "Anthropic", inbound: inbound.InboundTypeAnthropic, outbound: outbound.OutboundTypeAnthropic,
			path:     "/v1/messages",
			body:     `{"model":"native-fallback","max_tokens":16,"messages":[{"role":"user","content":"retained prompt"}],"tools":[{"type":"web_search_20250305","name":"web_search"}],"mcp_servers":[{"type":"url","name":"lookup","url":"https://example.invalid/mcp"}],"container":{"id":"container_1"}}`,
			response: `{"id":"msg_1","type":"message","role":"assistant","model":"upstream","content":[{"type":"text","text":"native works"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`,
		},
	} {
		t.Run(protocol.name, func(t *testing.T) {
			for _, scenario := range []struct {
				name         string
				withNative   bool
				nativeFails  bool
				wantNative   int32
				wantFallback int32
			}{
				{name: "only fallback", wantFallback: 1},
				{name: "native ahead of configured priority", withNative: true, wantNative: 1},
				{name: "native unavailable", withNative: true, nativeFails: true, wantNative: 1, wantFallback: 1},
			} {
				t.Run(scenario.name, func(t *testing.T) {
					gin.SetMode(gin.TestMode)
					ctx := setupRelayTestDB(t)
					if err := op.SettingSetString(model.SettingKeyCapabilityDegradationPolicy, "strict"); err != nil {
						t.Fatal(err)
					}
					var nativeHits, fallbackHits atomic.Int32
					fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						fallbackHits.Add(1)
						body, err := io.ReadAll(r.Body)
						if err != nil || !bytes.Contains(body, []byte("retained prompt")) {
							t.Errorf("fallback lost the prompt: %s, err=%v", body, err)
						}
						for _, field := range []string{"web_search", "mcp_servers", "container_1"} {
							if bytes.Contains(body, []byte(field)) {
								t.Errorf("native field %q leaked into fallback: %s", field, body)
							}
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"id":"chat_1","object":"chat.completion","model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"fallback works"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
					}))
					defer fallback.Close()
					native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						nativeHits.Add(1)
						if scenario.nativeFails {
							http.Error(w, "native channel unavailable", http.StatusServiceUnavailable)
							return
						}
						body, err := io.ReadAll(r.Body)
						if err != nil || !bytes.Contains(body, []byte("web_search")) {
							t.Errorf("native tool was not preserved: %s, err=%v", body, err)
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(protocol.response))
					}))
					defer native.Close()

					group := &model.Group{Name: "native-fallback", Mode: model.GroupModeFailover}
					if err := op.GroupCreate(group, ctx); err != nil {
						t.Fatal(err)
					}
					channels := []*model.Channel{{Name: "fallback", Type: outbound.OutboundTypeOpenAIChat, BaseUrls: []model.BaseUrl{{URL: fallback.URL + "/v1"}}}}
					if scenario.withNative {
						channels = append(channels, &model.Channel{Name: "native", Type: protocol.outbound, BaseUrls: []model.BaseUrl{{URL: native.URL + "/v1"}}})
					}
					for i, channel := range channels {
						channel.Enabled, channel.Model = true, "upstream"
						channel.Keys = []model.ChannelKey{{Enabled: true, ChannelKey: "test-key"}}
						if err := op.ChannelCreate(channel, ctx); err != nil {
							t.Fatal(err)
						}
						if err := op.GroupItemAdd(&model.GroupItem{GroupID: group.ID, ChannelID: channel.ID, ModelName: "upstream", Priority: i + 1, Weight: 1}, ctx); err != nil {
							t.Fatal(err)
						}
					}
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Set("api_key_id", 8)
					c.Request = httptest.NewRequest(http.MethodPost, protocol.path, strings.NewReader(protocol.body))
					c.Request.Header.Set("Content-Type", "application/json")
					Handler(protocol.inbound, c)
					if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "works") {
						t.Fatalf("request failed: status=%d body=%s", recorder.Code, recorder.Body.String())
					}
					if nativeHits.Load() != scenario.wantNative || fallbackHits.Load() != scenario.wantFallback {
						t.Fatalf("upstream hits: native=%d fallback=%d, want native=%d fallback=%d", nativeHits.Load(), fallbackHits.Load(), scenario.wantNative, scenario.wantFallback)
					}
				})
			}
		})
	}
}
