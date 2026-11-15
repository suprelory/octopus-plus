package op

import (
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestRelayLogCleanupPreservesRecentRowsAcrossBatches(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatal(err)
	}
	if err := SettingSetString(model.SettingKeyRelayLogKeepPeriod, "1"); err != nil {
		t.Fatal(err)
	}
	if err := SettingSetString(model.SettingKeyRelayLogContentKeepPeriod, "0"); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	rows := []model.RelayLog{{ID: 1, Time: now.Unix(), RequestModelName: "retained"}}
	for i := 0; i < relayLogCleanupBatchSize+1; i++ {
		rows = append(rows, model.RelayLog{ID: int64(i + 2), Time: now.Add(-48 * time.Hour).Unix()})
	}
	conn := db.GetDB().WithContext(ctx)
	if err := conn.CreateInBatches(&rows, 500).Error; err != nil {
		t.Fatal(err)
	}
	// Include bodies in the final expired batch and in the retained row. The
	// retention filter must apply on every batch, including the content deletes.
	for _, id := range []int64{rows[0].ID, rows[len(rows)-1].ID} {
		if err := conn.Create(&model.RelayRequestContent{LogID: id, RequestBody: []byte("request")}).Error; err != nil {
			t.Fatal(err)
		}
		if err := conn.Create(&model.RelayAttemptContent{LogID: id, AttemptID: "1", ResponseBody: []byte("response")}).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := relayLogCleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var remaining []model.RelayLog
	if err := conn.Find(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].ID != rows[0].ID {
		t.Fatalf("retention removed recent logs or left expired logs: %+v", remaining)
	}
	for _, table := range []any{&model.RelayRequestContent{}, &model.RelayAttemptContent{}} {
		var ids []int64
		if err := conn.Model(table).Pluck("log_id", &ids).Error; err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != rows[0].ID {
			t.Fatalf("retention left incorrect %T rows: %v", table, ids)
		}
	}
}
