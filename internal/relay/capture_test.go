package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/polywire/inbound"
)

func TestHTTPLogCapturesEachAttemptAndDeliveredResponse(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("X-Request-Id", "provider-failed")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer primary.Close()
	var sentBody string
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sentBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "provider-success")
		_, _ = io.WriteString(w, relayTestResponseJSON("resp_captured", "record this"))
	}))
	defer fallback.Close()
	group := &dbmodel.Group{Name: "capture-retry", Mode: dbmodel.GroupModeFailover}
	channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, primary.URL, fallback.URL)
	override := `{"temperature":0.4}`
	if _, err := op.ChannelUpdate(&dbmodel.ChannelUpdateRequest{ID: channels[1].ID, ParamOverride: &override}, ctx); err != nil {
		t.Fatal(err)
	}
	original := `{"model":"capture-retry","input":"hello","stream":false}`
	c, recorder := newHTTPRelayTestContext(ctx, original)
	c.Request.Header.Set("Authorization", "Bearer private-client-key")
	Handler(inbound.InboundTypeOpenAIResponse, c)
	entry := assertHTTPRelaySettlement(t, ctx, true, channels[0].ID, channels[1].ID)
	detail, err := op.RelayLogGet(ctx, entry.ID)
	if err != nil || detail.Trace == nil || len(detail.Trace.Attempts) != 2 {
		t.Fatalf("missing trace: %+v %v", detail, err)
	}
	trace := detail.Trace
	if trace.ServingAttemptID != "2" {
		t.Fatalf("serving attempt = %q", trace.ServingAttemptID)
	}
	phases := map[string]bool{}
	var last int64
	for _, timing := range trace.Attempts[1].Timings {
		if timing.ElapsedMS < last {
			t.Fatal("timeline is not monotonic")
		}
		last = timing.ElapsedMS
		phases[timing.Phase] = true
	}
	for _, phase := range []string{"attempt_start", "send_start", "connection_ready", "request_sent", "response_first_byte", "response_headers", "downstream_delivery", "finished"} {
		if !phases[phase] {
			t.Fatalf("missing timing %s: %+v", phase, trace.Attempts[1].Timings)
		}
	}
	if trace.ID != recorder.Header().Get("X-Octopus-Request-Id") || trace.Client.Request.Headers["Authorization"][0] != "[REDACTED]" {
		t.Fatal("request identity or redaction missing")
	}
	if trace.Attempts[0].Response.StatusCode == nil || *trace.Attempts[0].Response.StatusCode != 429 || entry.Attempts[0].HTTPStatus == nil || *entry.Attempts[0].HTTPStatus != 429 {
		t.Fatal("upstream status was lost")
	}
	for _, test := range []struct{ attempt, direction, want string }{
		{"", "request", original}, {"2", "request", sentBody}, {"1", "response", `{"error":{"message":"rate limited"}}`}, {"", "response", recorder.Body.String()},
	} {
		got, err := op.RelayLogContentGet(ctx, entry.ID, test.attempt, test.direction)
		if err != nil || got.Body != test.want || got.State != "captured" {
			t.Fatalf("%s/%s: %+v %v; want %q", test.attempt, test.direction, got, err, test.want)
		}
	}
	if !strings.Contains(sentBody, `"temperature":0.4`) || !strings.Contains(sentBody, `"model":"model_1"`) {
		t.Fatalf("final payload not captured: %s", sentBody)
	}
	usageMessage, err := op.RelayLogContentGet(ctx, entry.ID, "2", "response")
	if err != nil || len(usageMessage.RawUsage) != 1 || !strings.Contains(string(usageMessage.RawUsage[0].Value), `"total_tokens":5`) {
		t.Fatalf("raw usage: %+v %v", usageMessage, err)
	}
}

func TestHTTPLogCapturesFinalLocalError(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid upstream parameter","type":"invalid_request_error"}}`)
	}))
	defer upstream.Close()
	group := &dbmodel.Group{Name: "capture-error", Mode: dbmodel.GroupModeFailover}
	channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, upstream.URL)
	c, recorder := newHTTPRelayTestContext(ctx, `{"model":"capture-error","input":"hello"}`)
	Handler(inbound.InboundTypeOpenAIResponse, c)
	entry := assertHTTPRelaySettlement(t, ctx, false, channels[0].ID)
	got, err := op.RelayLogContentGet(ctx, entry.ID, "", "response")
	if err != nil || got.StatusCode == nil || *got.StatusCode != recorder.Code || got.Body == "" || got.Body != recorder.Body.String() {
		t.Fatalf("local response not captured: %+v %v", got, err)
	}
}
