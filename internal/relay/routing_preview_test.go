package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/polywire/inbound"
)

func TestRoutingPreviewExplainsDisabledCandidatesWithoutSending(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, relayTestResponseJSON("resp_ok", "answer"))
	}))
	defer server.Close()
	group := &dbmodel.Group{Name: "routing-preview", Mode: dbmodel.GroupModeRoundRobin}
	channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, server.URL, server.URL, server.URL)
	disabled := false
	if _, err := op.ChannelUpdate(&dbmodel.ChannelUpdateRequest{ID: channels[0].ID, Enabled: &disabled}, ctx); err != nil {
		t.Fatal(err)
	}
	input := RoutingPreviewRequest{GroupID: group.ID, APIKeyID: 7, Endpoint: "responses", Request: json.RawMessage(`{"model":"routing-preview","input":"hello"}`)}
	var selected int
	for i := 0; i < 3; i++ {
		result, err := PreviewRouting(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Candidates) != 3 || result.Candidates[2].Reason != "channel_disabled" {
			t.Fatalf("candidates=%+v", result.Candidates)
		}
		if i == 0 {
			selected = result.Candidates[0].Item.ChannelID
		} else if selected != result.Candidates[0].Item.ChannelID {
			t.Fatal("preview changed live ordering")
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "key-0") || strings.Contains(string(encoded), server.URL) {
			t.Fatal("preview exposed upstream credentials or URL")
		}
	}
	if hits.Load() != 0 {
		t.Fatal("preview sent an upstream request")
	}
	assertHTTPRelayKeysReleased(t, channels)
	c, recorder := newHTTPRelayTestContext(ctx, string(input.Request))
	Handler(inbound.InboundTypeOpenAIResponse, c)
	if recorder.Code != 200 || hits.Load() != 1 {
		t.Fatalf("status=%d hits=%d", recorder.Code, hits.Load())
	}
	entry := assertHTTPRelaySettlement(t, ctx, true, selected)
	summary := entry.Attempts[len(entry.Attempts)-1].Routing
	if summary == nil || summary.StopReason != "success" || summary.AttemptsUsed != 1 || summary.ChannelsUsed != 1 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestRoutingExplanationReportsBudgetStop(t *testing.T) {
	e := newRelayExecution(dbmodel.Group{}, true)
	e.budget.totalAttempts = 2
	attempts := []dbmodel.ChannelAttempt{{ChannelID: 1, Status: dbmodel.AttemptFailed}}
	explained := explainRouting(attempts, e, resolveRequestAffinity(nil, nil), false, newRelayBudgetError("send limit"))
	if attempts[0].Routing != nil {
		t.Fatal("explanation mutated iterator records")
	}
	if summary := explained[0].Routing; summary.StopReason != "budget_exceeded" || summary.AttemptsUsed != 2 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestRoutingPreviewRejectsInvalidRequests(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	group := &dbmodel.Group{Name: "routing-validation", Mode: dbmodel.GroupModeRoundRobin}
	if err := op.GroupCreate(group, ctx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, endpoint, body, want string
	}{
		{"null model", "responses", `{"model":null,"input":"hello"}`, "model must be a nonempty string"},
		{"model mismatch", "responses", `{"model":"another-group","input":"hello"}`, "model must match"},
		{"array", "responses", `[]`, "JSON object"},
		{"continuation", "responses", `{"input":"hello","previous_response_id":"resp_1"}`, "stateless requests only"},
		{"unknown endpoint", "unknown", `{"input":"hello"}`, "unsupported preview endpoint"},
		{"missing compact input", "compact", `{}`, "requires input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PreviewRouting(ctx, RoutingPreviewRequest{GroupID: group.ID, Endpoint: tc.endpoint, Request: json.RawMessage(tc.body)})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestRoutingPreviewStrictAffinityExplainsUnavailableKey(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	if err := op.SettingSetString(dbmodel.SettingKeyChannelAffinityMode, "strict"); err != nil {
		t.Fatal(err)
	}
	group := &dbmodel.Group{Name: "strict-preview", Mode: dbmodel.GroupModeFailover}
	channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, "http://127.0.0.1:1", "http://127.0.0.1:1")
	input := RoutingPreviewRequest{GroupID: group.ID, APIKeyID: 7, Endpoint: "responses", Request: json.RawMessage(`{"input":"hello","session_id":"preview-session"}`)}
	option := resolveRequestAffinity(nil, input.Request)
	channel := channels[0]
	balancer.SetRoutingAffinity(input.APIKeyID, group.ID, group.Name, channel.ID, channel.Keys[0].ID, option)
	bound := balancer.GetChannelAffinity(input.APIKeyID, group.ID, group.Name, option)
	retryAt := time.Now().Add(time.Hour)
	balancer.RecordScopedFailureAt(channel.ID, channel.Keys[0].ID, "model_1", balancer.FailureAuthentication, retryAt, "key")

	result, err := PreviewRouting(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 2 || result.Affinity.Mode != "strict" {
		t.Fatalf("unexpected result: %+v", result)
	}
	first, fallback := result.Candidates[0], result.Candidates[1]
	if first.Item.ChannelID != channel.ID || first.Eligible || first.Reason != "no_available_key" || first.BlockedKeys != 1 || first.RetryAt == nil || !first.RetryAt.Equal(retryAt) {
		t.Fatalf("unavailable key was not explained: %+v", first)
	}
	if fallback.Eligible || fallback.Reason != "affinity_filtered" || fallback.Order != 0 {
		t.Fatalf("strict affinity allowed fallback: %+v", fallback)
	}
	if after := balancer.GetChannelAffinity(input.APIKeyID, group.ID, group.Name, option); after == nil || *after != *bound {
		t.Fatal("preview changed strict affinity")
	}
	if result.Budget.AttemptsUsed != 0 || result.Budget.ChannelsUsed != 0 {
		t.Fatal("preview consumed a request budget")
	}
	assertHTTPRelayKeysReleased(t, channels)
}

func TestRoutingPreviewProtocolsAndOperationBudgets(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	for key, value := range map[dbmodel.SettingKey]string{
		dbmodel.SettingKeyRelayMaxChannelAttempts:     "2",
		dbmodel.SettingKeyRelayMaxTotalAttempts:       "5",
		dbmodel.SettingKeyRelayFailoverTimeoutSeconds: "15",
		"relay_images_max_channel_attempts":           "1",
		"relay_images_max_total_attempts":             "2",
		"relay_images_timeout_seconds":                "3",
		"relay_compact_max_channel_attempts":          "1",
		"relay_compact_max_total_attempts":            "3",
		"relay_compact_timeout_seconds":               "4",
	} {
		if err := op.SettingSetString(key, value); err != nil {
			t.Fatal(err)
		}
	}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	group := &dbmodel.Group{Name: "protocol-preview", Mode: dbmodel.GroupModeRoundRobin}
	channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, server.URL)
	for _, tc := range []struct {
		endpoint, body             string
		channelLimit, attemptLimit int
		timeoutMillis              int64
	}{
		{"responses", `{"input":"hello"}`, 2, 5, 15000},
		{"chat", `{"messages":[{"role":"user","content":"hello"}]}`, 2, 5, 15000},
		{"messages", `{"messages":[{"role":"user","content":"hello"}],"max_tokens":100}`, 2, 5, 15000},
		{"embeddings", `{"input":"hello"}`, 2, 5, 15000},
		{"websocket", `{"input":"hello"}`, 2, 5, 15000},
		{"images", `{"prompt":"hello"}`, 1, 2, 3000},
		{"compact", `{"input":[]}`, 1, 3, 4000},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			result, err := PreviewRouting(ctx, RoutingPreviewRequest{GroupID: group.ID, Endpoint: tc.endpoint, Request: json.RawMessage(tc.body)})
			if err != nil {
				t.Fatal(err)
			}
			if result.Model != group.Name || len(result.Candidates) != 1 || result.Candidates[0].CapabilityStatus == "" {
				t.Fatalf("missing protocol diagnostics: %+v", result)
			}
			budget := result.Budget
			if budget.ChannelLimit != tc.channelLimit || budget.AttemptLimit != tc.attemptLimit || budget.RemainingMillis <= 0 || budget.RemainingMillis > tc.timeoutMillis || budget.AttemptsUsed != 0 || budget.ChannelsUsed != 0 {
				t.Fatalf("unexpected operation budget: %+v", budget)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatal("preview sent an upstream request")
	}
	assertHTTPRelayKeysReleased(t, channels)
}
