package op

import (
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestRelayLogRecentByteBudget(t *testing.T) {
	resetRelayLogStateForTest()
	t.Cleanup(resetRelayLogStateForTest)
	for i := 0; i < 120; i++ {
		size := (i%12 + 1) << 20
		appendRelayLogRecent(model.RelayLog{ID: int64(i + 1), RequestContent: strings.Repeat("x", size), RequestModelName: "model"})
		if got := relayLogBatchApproxBytes(relayLogBuffer.recent); got > relayLogRecentMaxBytes {
			t.Fatalf("retained %d bytes", got)
		}
		if len(relayLogBuffer.recent) > relayLogRecentMaxSize {
			t.Fatal("count budget exceeded")
		}
		latest := relayLogBuffer.recent[len(relayLogBuffer.recent)-1]
		if latest.ID != int64(i+1) || latest.RequestModelName != "model" {
			t.Fatal("latest metadata lost")
		}
	}
}

func TestRelayLogDisabledRetentionKeepsMetadataOnly(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatal(err)
	}
	if err := SettingSetString(model.SettingKeyRelayLogKeepEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	resetRelayLogStateForTest()
	t.Cleanup(resetRelayLogStateForTest)
	if err := RelayLogAdd(model.RelayLog{RequestModelName: "model", RequestContent: "private request", ResponseContent: "private response"}); err != nil {
		t.Fatal(err)
	}
	if RelayLogPendingLen() != 0 {
		t.Fatal("disabled log queued")
	}
	recent := relayLogBuffer.recent[0]
	got, err := RelayLogGet(ctx, recent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestContent != "" || got.ResponseContent != "" || got.RequestModelName != "model" {
		t.Fatal("unexpected retained content")
	}
}

func TestRelayLogDetailsLoadPersistedBody(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	resetRelayLogStateForTest()
	t.Cleanup(resetRelayLogStateForTest)
	row := model.RelayLog{ID: 42, RequestContent: "full body"}
	if err := db.GetDB().Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	appendRelayLogRecent(model.RelayLog{ID: row.ID})
	got, err := RelayLogGet(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestContent != row.RequestContent {
		t.Fatal("metadata cache hid persisted body")
	}
}
