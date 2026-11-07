package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/transformer"
	"github.com/suprelory/octopus-plus/polywire/inbound"
)

func TestReplayChannelBudgetCountsSubmissionsInsteadOfIteratorPositions(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, relayTestResponseSSE("resp_replay", "answer"))
	}))
	defer upstream.Close()
	group := &dbmodel.Group{Name: "replay-skips", Mode: dbmodel.GroupModeFailover}
	channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, upstream.URL, upstream.URL, upstream.URL, upstream.URL, upstream.URL)
	for _, channel := range channels[:4] {
		disabled := false
		if _, err := op.ChannelUpdate(&dbmodel.ChannelUpdateRequest{ID: channel.ID, Enabled: &disabled}, ctx); err != nil {
			t.Fatal(err)
		}
	}
	client, server := newTestWSConnPair(t)
	defer client.CloseNow()
	defer server.CloseNow()
	adapter := transformer.Inbound(inbound.InboundTypeOpenAIResponse)
	body := []byte(`{"model":"replay-skips","input":"next","stream":true}`)
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
	req.execution.budget.maxChannelAttempts = 1
	result := runWSRelay(ctx, req, selectedGroup, true)
	if !result.Success || hits.Load() != 1 {
		t.Fatalf("skipped candidates consumed recovery slots: result=%+v hits=%d", result, hits.Load())
	}
	if len(req.execution.budget.visitedChannels) != 1 || len(req.execution.replayBudget.visitedChannels) != 1 || req.execution.budget.totalAttempts != 1 {
		t.Fatalf("unexpected budget consumption: root=%+v replay=%+v", req.execution.budget, req.execution.replayBudget)
	}
	finalizeWSRelay(ctx, server, req, result)
	logs, err := op.RelayLogList(ctx, nil, nil, nil, 1, 10)
	if err != nil || len(logs) != 1 || !logs[0].Success {
		t.Fatalf("replay settlement: %+v err=%v", logs, err)
	}
	assertHTTPRelayKeysReleased(t, channels)
}
