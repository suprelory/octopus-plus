package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/transformer"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	transformerModel "github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestHandlerRecordsDegradedCapabilityTrace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := setupRelayTestDB(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
	}))
	defer server.Close()

	channel := &dbmodel.Channel{
		Name:     "relay-p1-degraded-anthropic",
		Type:     outbound.OutboundTypeAnthropic,
		Enabled:  true,
		BaseUrls: []dbmodel.BaseUrl{{URL: server.URL + "/v1"}},
		Model:    "claude-test",
		Keys:     []dbmodel.ChannelKey{{Enabled: true, ChannelKey: "test-key"}},
	}
	if err := op.ChannelCreate(channel, ctx); err != nil {
		t.Fatal(err)
	}
	group := &dbmodel.Group{Name: "relay-p1-degraded-group", Mode: dbmodel.GroupModeFailover}
	if err := op.GroupCreate(group, ctx); err != nil {
		t.Fatal(err)
	}
	if err := op.GroupItemAdd(&dbmodel.GroupItem{GroupID: group.ID, ChannelID: channel.ID, ModelName: "claude-test", Priority: 1, Weight: 1}, ctx); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"relay-p1-degraded-group","messages":[{"role":"user","content":"hello"}],"response_format":{"type":"json_object"}}`))
	c.Request.Header.Set("Content-Type", "application/json")
	Handler(inbound.InboundTypeOpenAIChat, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("request failed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	logs, err := op.RelayLogList(ctx, nil, nil, nil, 1, 10)
	if err != nil || len(logs) == 0 || len(logs[0].Attempts) != 1 {
		t.Fatalf("expected one logged attempt, logs=%#v err=%v", logs, err)
	}
	attempt := logs[0].Attempts[0]
	if attempt.CapabilityStatus != "degraded" || attempt.Lossiness != "known" {
		t.Fatalf("unexpected capability decision: %#v", attempt)
	}
	if !slices.Contains(attempt.RequiredFeatures, "structured_output") || !slices.Contains(attempt.DegradedFields, "response_format") {
		t.Fatalf("missing degraded feature trace: %#v", attempt)
	}
	wantPath := []string{string(transformerModel.APIFormatOpenAIChatCompletion), "canonical", string(transformerModel.APIFormatAnthropicMessage)}
	if !slices.Equal(attempt.ConversionPath, wantPath) || len(attempt.CapabilityReasons) == 0 {
		t.Fatalf("incomplete conversion trace: %#v", attempt)
	}
}

func TestPlanRelayCapabilityRejectsRemovedOutboundTypeWithoutMutatingRequest(t *testing.T) {
	request := &transformerModel.InternalLLMRequest{
		Model:           "group-alias",
		RequestType:     transformerModel.RequestTypeResponses,
		ReasoningEffort: "medium",
		RawAPIFormat:    transformerModel.APIFormatOpenAIResponse,
	}
	relayRequest := &relayRequest{internalRequest: request}
	channel := &dbmodel.Channel{Type: outbound.OutboundType(-1)}
	decision := planRelayCapability(relayRequest, channel, transformer.Outbound(channel.Type), "legacy-model")
	if !decision.Rejected() {
		t.Fatalf("removed outbound type should be rejected: %#v", decision)
	}
	if request.Model != "group-alias" {
		t.Fatalf("planner mutated base request model to %q", request.Model)
	}

	if decision = planRelayCapability(relayRequest, nil, nil, "model"); !decision.Rejected() {
		t.Fatalf("nil channel should be rejected: %#v", decision)
	}
}

func TestStandardStreamFinalizationWritesCanonicalTailAndMetrics(t *testing.T) {
	writer := &notifyStreamWriter{header: http.Header{}}
	request := &transformerModel.InternalLLMRequest{Model: "model_1", Stream: p1Bool(true), RawAPIFormat: transformerModel.APIFormatOpenAIChatCompletion}
	metrics := NewRelayMetrics(1, request.Model, "chat", "", nil, request)
	ra := &relayAttempt{
		relayRequest: &relayRequest{
			ctx:             context.Background(),
			inAdapter:       transformer.Inbound(inbound.InboundTypeOpenAIChat),
			internalRequest: request,
			metrics:         metrics,
			streamWriter:    writer,
		},
		outAdapter: transformer.Outbound(outbound.OutboundTypeOpenAIChat),
	}
	rawSSE := "data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"model\":\"model_1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\n"
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(rawSSE)),
	}

	if err := ra.handleStreamResponseV2(context.Background(), response); err != nil {
		t.Fatal(err)
	}
	stream := writer.buf.String()
	if !strings.Contains(stream, `"finish_reason":"stop"`) || !strings.Contains(stream, `"usage"`) || !strings.HasSuffix(stream, "data: [DONE]\n\n") {
		t.Fatalf("canonical finalization tail is incomplete: %s", stream)
	}
	if metrics.InternalResponse == nil || metrics.InternalResponse.Usage == nil || len(metrics.InternalResponse.Choices) != 1 {
		t.Fatalf("canonical aggregate was not stored in metrics: %#v", metrics.InternalResponse)
	}
	if !ra.responseCollected.Load() {
		t.Fatal("stream finalization did not mark the canonical response collected")
	}
}

func TestPassthroughPrecommitAcceptsLargeSingleEventAcrossReadChunks(t *testing.T) {
	largeDelta := strings.Repeat("x", 300*1024)
	rawSSE := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_large","object":"response","model":"gpt-4o","output":[],"status":"in_progress"}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"` + largeDelta + `"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_large","object":"response","model":"gpt-4o","output":[],"status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		"",
	}, "\n")
	writer := &notifyStreamWriter{header: http.Header{}}
	ra, _ := newOpenAIResponsesPassthroughAttempt(writer)
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(rawSSE)),
	}
	cfg := ra.outAdapter.(transformerModel.PassthroughCapable).PassthroughConfig()
	if err := ra.handleStreamResponsePassthroughV2(context.Background(), response, cfg); err != nil {
		t.Fatal(err)
	}
	if writer.buf.String() != rawSSE {
		t.Fatalf("large passthrough event was not byte-stable: got=%d want=%d bytes", writer.buf.Len(), len(rawSSE))
	}
}

func TestHandlerFailsOverOnProviderStreamErrorBeforeFirstByte(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := setupRelayTestDB(t)
	var firstHits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"error\":{\"message\":\"semantic failure\",\"type\":\"server_error\",\"code\":\"bad_stream\"}}\n\n")
	}))
	defer first.Close()
	var secondHits atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chat_ok\",\"object\":\"chat.completion.chunk\",\"model\":\"model_1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer second.Close()

	channels := []*dbmodel.Channel{
		{Name: "relay-p1-stream-error", Type: outbound.OutboundTypeOpenAIChat, Enabled: true, BaseUrls: []dbmodel.BaseUrl{{URL: first.URL + "/v1"}}, Model: "model_1", Keys: []dbmodel.ChannelKey{{Enabled: true, ChannelKey: "first"}}},
		{Name: "relay-p1-stream-fallback", Type: outbound.OutboundTypeOpenAIChat, Enabled: true, BaseUrls: []dbmodel.BaseUrl{{URL: second.URL + "/v1"}}, Model: "model_1", Keys: []dbmodel.ChannelKey{{Enabled: true, ChannelKey: "second"}}},
	}
	for _, channel := range channels {
		if err := op.ChannelCreate(channel, ctx); err != nil {
			t.Fatal(err)
		}
	}
	group := &dbmodel.Group{Name: "relay-p1-stream-failover-group", Mode: dbmodel.GroupModeFailover}
	if err := op.GroupCreate(group, ctx); err != nil {
		t.Fatal(err)
	}
	for index, channel := range channels {
		if err := op.GroupItemAdd(&dbmodel.GroupItem{GroupID: group.ID, ChannelID: channel.ID, ModelName: "model_1", Priority: index + 1, Weight: 1}, ctx); err != nil {
			t.Fatal(err)
		}
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"relay-p1-stream-failover-group","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	Handler(inbound.InboundTypeOpenAIChat, c)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"content":"ok"`) || strings.Contains(recorder.Body.String(), "semantic failure") {
		t.Fatalf("fallback stream failed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if firstHits.Load() != 1 || secondHits.Load() != 1 {
		t.Fatalf("unexpected upstream hits: first=%d second=%d", firstHits.Load(), secondHits.Load())
	}
	logs, err := op.RelayLogList(ctx, nil, nil, nil, 1, 10)
	if err != nil || len(logs) == 0 || len(logs[0].Attempts) != 2 {
		t.Fatalf("expected two logged attempts, logs=%#v err=%v", logs, err)
	}
	if logs[0].Attempts[0].Status != dbmodel.AttemptFailed || logs[0].Attempts[1].Status != dbmodel.AttemptSuccess {
		t.Fatalf("unexpected failover trace: %#v", logs[0].Attempts)
	}
}

func p1Bool(value bool) *bool { return &value }
