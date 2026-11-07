package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestHandlerRejectsKnownDegradationInStrictMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := setupRelayTestDB(t)
	if err := op.SettingSetString(dbmodel.SettingKeyCapabilityDegradationPolicy, string(capabilityPolicyStrict)); err != nil {
		t.Fatal(err)
	}

	var upstreamHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		http.Error(w, "strict policy must reject before upstream", http.StatusInternalServerError)
	}))
	defer server.Close()
	group := addStrictDegradationRoute(t, ctx, "relay-strict-http", server.URL)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("api_key_id", 91)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+group.Name+`","messages":[{"role":"user","content":"hello"}],"response_format":{"type":"json_object"}}`))
	c.Request.Header.Set("Content-Type", "application/json")

	Handler(inbound.InboundTypeOpenAIChat, c)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), CodeRelayCapabilityRejected) {
		t.Fatalf("strict rejection = status %d body %s", recorder.Code, recorder.Body.String())
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("strict rejection reached upstream %d times", upstreamHits.Load())
	}
	assertStrictCapabilityAttempt(t, ctx)
}

func TestWSRejectsKnownDegradationInStrictMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := setupRelayTestDB(t)
	if err := op.SettingSetString(dbmodel.SettingKeyCapabilityDegradationPolicy, string(capabilityPolicyStrict)); err != nil {
		t.Fatal(err)
	}

	var upstreamHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		http.Error(w, "strict policy must reject before upstream", http.StatusInternalServerError)
	}))
	defer server.Close()
	group := addStrictDegradationRoute(t, ctx, "relay-strict-ws", server.URL)

	clientConn, serverConn := newTestWSConnPair(t)
	defer clientConn.Close(websocket.StatusNormalClosure, "")
	defer serverConn.Close(websocket.StatusNormalClosure, "")
	request := []byte(`{"type":"response.create","model":"` + group.Name + `","input":"hello","text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object"}}}}`)
	processWSResponseCreate(context.Background(), serverConn, request, 92, "", "127.0.0.1", "strict-session", nil)

	readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, payload, err := clientConn.Read(readCtx)
	if err != nil {
		t.Fatalf("read strict websocket rejection: %v", err)
	}
	var event struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
		Error  struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode strict websocket rejection: %v", err)
	}
	if event.Type != "error" || event.Status != http.StatusBadRequest || event.Error.Code != CodeRelayCapabilityRejected {
		t.Fatalf("strict websocket rejection = %s", payload)
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("strict websocket rejection reached upstream %d times", upstreamHits.Load())
	}
	assertStrictCapabilityAttempt(t, ctx)
}

func addStrictDegradationRoute(t *testing.T, ctx context.Context, name, baseURL string) *dbmodel.Group {
	t.Helper()
	channel := &dbmodel.Channel{
		Name:     name + "-anthropic",
		Type:     outbound.OutboundTypeAnthropic,
		Enabled:  true,
		BaseUrls: []dbmodel.BaseUrl{{URL: baseURL + "/v1"}},
		Model:    "claude-test",
		Keys:     []dbmodel.ChannelKey{{Enabled: true, ChannelKey: "test-key"}},
	}
	if err := op.ChannelCreate(channel, ctx); err != nil {
		t.Fatal(err)
	}
	group := &dbmodel.Group{Name: name + "-group", Mode: dbmodel.GroupModeFailover}
	if err := op.GroupCreate(group, ctx); err != nil {
		t.Fatal(err)
	}
	if err := op.GroupItemAdd(&dbmodel.GroupItem{GroupID: group.ID, ChannelID: channel.ID, ModelName: "claude-test", Priority: 1, Weight: 1}, ctx); err != nil {
		t.Fatal(err)
	}
	return group
}

func assertStrictCapabilityAttempt(t *testing.T, ctx context.Context) {
	t.Helper()
	logs, err := op.RelayLogList(ctx, nil, nil, nil, 1, 10)
	if err != nil || len(logs) == 0 || len(logs[0].Attempts) != 1 {
		t.Fatalf("strict capability log = %#v, err=%v", logs, err)
	}
	attempt := logs[0].Attempts[0]
	if attempt.Status != dbmodel.AttemptSkipped || attempt.CapabilityStatus != string(outbound.CapabilityDegraded) || attempt.CapabilityPolicy != string(capabilityPolicyStrict) {
		t.Fatalf("strict capability attempt = %#v", attempt)
	}
	if len(attempt.DegradedFields) == 0 || len(attempt.CapabilityLosses) == 0 || attempt.FallbackReason == "" {
		t.Fatalf("incomplete strict capability trace = %#v", attempt)
	}
}
