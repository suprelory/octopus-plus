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
	"github.com/suprelory/octopus-plus/polywire/inbound"
)

func TestPassthroughEmptyResponseDetectionAndFailover(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			ctx := setupHTTPRelayTestDB(t)
			if err := op.SettingSetString(model.SettingKeyEmptyResponseDetectionEnabled, fmt.Sprint(enabled)); err != nil {
				t.Fatal(err)
			}
			var fallbackHits atomic.Int32
			emptyBody := strings.TrimSuffix(relayTestResponseJSON("resp_empty", ""), "}") + `,"passthrough_probe":true}`
			fallbackBody := strings.TrimSuffix(relayTestResponseJSON("resp_fallback", "fallback answer"), "}") + `,"passthrough_probe":true}`
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, emptyBody)
			}))
			defer primary.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackHits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, fallbackBody)
			}))
			defer fallback.Close()
			group := &model.Group{Name: "empty-passthrough", Mode: model.GroupModeFailover}
			channels := addHTTPRelayTestChannels(t, ctx, group, model.ChannelPassthroughModeAuto, primary.URL, fallback.URL)
			c, recorder := newHTTPRelayTestContext(ctx, `{"model":"empty-passthrough","input":"hello"}`)
			Handler(inbound.InboundTypeOpenAIResponse, c)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if enabled {
				if recorder.Body.String() != fallbackBody {
					t.Fatal("successful passthrough was not byte-stable")
				}
				if fallbackHits.Load() != 1 || !strings.Contains(recorder.Body.String(), "fallback answer") || strings.Contains(recorder.Body.String(), "resp_empty") {
					t.Fatalf("empty reply was committed: %s", recorder.Body.String())
				}
				assertHTTPRelaySettlement(t, ctx, true, channels[0].ID, channels[1].ID)
			} else {
				if recorder.Body.String() != emptyBody {
					t.Fatal("disabled detection did not preserve raw passthrough body")
				}
				if fallbackHits.Load() != 0 || !strings.Contains(recorder.Body.String(), "resp_empty") {
					t.Fatalf("disabled detection changed response: %s", recorder.Body.String())
				}
				assertHTTPRelaySettlement(t, ctx, true, channels[0].ID)
			}
		})
	}
}
