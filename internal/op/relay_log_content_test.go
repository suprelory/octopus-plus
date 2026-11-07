package op

import (
	"strings"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/relay/capture"
)

func captureTestMessage(text string) *model.RelayMessage {
	body := capture.NewBody(model.RelayMessage{}, capture.NewBudget(1024), 1024)
	body.Observe([]byte(text))
	body.Finish(true)
	return body.Snapshot()
}

func TestRelayContentRoundTripExpirationAndClear(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatal(err)
	}
	resetRelayLogStateForTest()
	t.Cleanup(resetRelayLogStateForTest)
	if err := SettingSetString(model.SettingKeyRelayLogContentKeepPeriod, "1"); err != nil {
		t.Fatal(err)
	}
	trace := &model.RelayTrace{ID: "req_test", Client: model.RelayExchange{Transport: "http", Request: captureTestMessage("original request"), Response: captureTestMessage("client response")}, Attempts: []model.RelayExchange{{AttemptID: "1", Request: captureTestMessage("rewritten request"), Response: captureTestMessage("provider response")}}}
	sub := RelayLogSubscribe()
	trace.ServingAttemptID = "1"
	trace.Attempts[0].Timings = []model.RelayTiming{{Phase: "request_sent", ElapsedMS: 12}}
	defer RelayLogUnsubscribe(sub)
	if err := RelayLogAdd(model.RelayLog{Time: time.Now().Add(-48 * time.Hour).Unix(), Trace: trace}); err != nil {
		t.Fatal(err)
	}
	live := <-sub
	if live.Trace != nil || live.RequestContent != "" || live.ResponseContent != "" {
		t.Fatal("live stream retained bodies")
	}
	for _, flush := range []bool{false, true} {
		if flush {
			if err := RelayLogFlushPending(ctx); err != nil {
				t.Fatal(err)
			}
		}
		message, err := RelayLogContentGet(ctx, live.ID, "1", "request")
		if err != nil || message.Body != "rewritten request" || message.State != "captured" {
			t.Fatalf("read (flushed %v): %+v %v", flush, message, err)
		}
		detail, err := RelayLogGet(ctx, live.ID)
		if err != nil || detail.Trace == nil || detail.Trace.ID != "req_test" || len(detail.Trace.Client.Request.Data) != 0 || detail.Trace.Client.Request.Body != "" {
			t.Fatalf("detail should contain metadata: %+v %v", detail, err)
		}
		if detail.Trace.ServingAttemptID != "1" || len(detail.Trace.Attempts[0].Timings) != 1 {
			t.Fatal("delivery and timing metadata lost")
		}
		start, end := int(time.Now().Add(-72*time.Hour).Unix()), int(time.Now().Unix())
		matches, err := RelayLogListWithFilter(ctx, RelayLogListFilter{Keyword: "rewritten", KeywordScope: RelayLogKeywordScopeContent, StartTime: &start, EndTime: &end, Page: 1, PageSize: 10, WithTotal: true})
		if err != nil || matches.Total != 1 || len(matches.Logs) != 1 || matches.Logs[0].Trace != nil {
			t.Fatalf("body search (flushed %v): %+v %v", flush, matches, err)
		}
	}
	dump, err := DBExportAll(ctx, true, false)
	if err != nil || len(dump.RelayRequestContents) != 1 || len(dump.RelayAttemptContents) != 1 {
		t.Fatalf("body backup missing: %v", err)
	}
	if err := relayLogCleanup(ctx); err != nil {
		t.Fatal(err)
	}
	message, err := RelayLogContentGet(ctx, live.ID, "1", "response")
	if err != nil || message.State != "expired" || message.Body != "" {
		t.Fatalf("expired: %+v %v", message, err)
	}
	if _, err := RelayLogGet(ctx, live.ID); err != nil {
		t.Fatalf("body expiration removed summary: %v", err)
	}
	if err := RelayLogClear(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []any{&model.RelayLog{}, &model.RelayRequestContent{}, &model.RelayAttemptContent{}} {
		var count int64
		if err := db.GetDB().Model(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("clear left %T rows: %d %v", table, count, err)
		}
	}
	if _, err := DBImportIncremental(ctx, dump); err != nil {
		t.Fatal(err)
	}
	restored, err := RelayLogContentGet(ctx, live.ID, "1", "response")
	if err != nil || restored.Body != "provider response" {
		t.Fatalf("restored body: %+v %v", restored, err)
	}
}

func TestRelayContentBudgetKeepsSummary(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatal(err)
	}
	resetRelayLogStateForTest()
	t.Cleanup(resetRelayLogStateForTest)
	relayLogBuffer.pendingContentBytes = relayLogContentQueueBytes
	trace := &model.RelayTrace{Client: model.RelayExchange{Request: captureTestMessage(strings.Repeat("x", 100))}}
	if err := RelayLogAdd(model.RelayLog{Time: time.Now().Unix(), RequestModelName: "retained", Trace: trace}); err != nil {
		t.Fatal(err)
	}
	if RelayLogPendingLen() != 1 {
		t.Fatal("body budget dropped the entire log")
	}
	got := relayLogBuffer.pending[0]
	if got.RequestModelName != "retained" || got.Trace.Client.Request.Reason != "queue_full" || len(got.Trace.Client.Request.Data) != 0 {
		t.Fatalf("unexpected queue row: %+v", got)
	}
	if len(trace.Client.Request.Data) == 0 {
		t.Fatal("queue changed the caller's snapshot")
	}
}
