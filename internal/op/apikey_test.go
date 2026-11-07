package op

import (
	"fmt"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestAPIKeyCreateFailureDoesNotPublish(t *testing.T) {
	ctx := setupBackupTestDB(t)
	apiKeyCache.Clear()
	apiKeyIDMap.Clear()
	t.Cleanup(func() { apiKeyCache.Clear(); apiKeyIDMap.Clear() })
	if err := db.GetDB().Exec("CREATE TRIGGER reject_key_create BEFORE INSERT ON api_keys BEGIN SELECT RAISE(ABORT, 'create failed'); END").Error; err != nil {
		t.Fatal(err)
	}
	key := model.APIKey{Name: "disabled", APIKey: "test-only-disabled", Enabled: false}
	if err := APIKeyCreate(&key, ctx); err == nil {
		t.Fatal("expected creation failure")
	}
	var count int64
	if err := db.GetDB().Model(&model.APIKey{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 || key.ID != 0 || apiKeyCache.Len() != 0 || apiKeyIDMap.Len() != 0 {
		t.Fatal("failed creation published a key")
	}
}

func TestAPIKeyImportPreservesDisabledState(t *testing.T) {
	ctx := setupBackupTestDB(t)
	dump := model.DBDump{Version: dbDumpVersion, APIKeys: []model.APIKey{
		{ID: 21, Name: "disabled backup", APIKey: "test-only-import", Enabled: false},
	}}
	if _, err := DBImportIncremental(ctx, &dump); err != nil {
		t.Fatal(err)
	}
	var stored model.APIKey
	if err := db.GetDB().First(&stored, "api_key = ?", dump.APIKeys[0].APIKey).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Enabled {
		t.Fatal("restore enabled a disabled key")
	}
}

func TestAPIKeyDeleteKeepsStateOnFailure(t *testing.T) {
	for _, table := range []string{"stats_api_keys", "api_keys"} {
		t.Run(table, func(t *testing.T) {
			ctx := setupBackupTestDB(t)
			resetStatsTestState(t)
			apiKeyCache.Clear()
			apiKeyIDMap.Clear()
			t.Cleanup(func() { apiKeyCache.Clear(); apiKeyIDMap.Clear() })
			key := model.APIKey{Name: "limited", APIKey: "test-only-limited", Enabled: true, MaxCost: 1}
			if err := APIKeyCreate(&key, ctx); err != nil {
				t.Fatal(err)
			}
			if err := StatsAPIKeyUpdate(key.ID, model.StatsMetrics{InputCost: 2}); err != nil {
				t.Fatal(err)
			}
			stored := StatsAPIKeyGet(key.ID)
			if err := db.GetDB().Create(&stored).Error; err != nil {
				t.Fatal(err)
			}
			trigger := fmt.Sprintf("CREATE TRIGGER reject_delete BEFORE DELETE ON %s BEGIN SELECT RAISE(ABORT, 'delete failed'); END", table)
			if err := db.GetDB().Exec(trigger).Error; err != nil {
				t.Fatal(err)
			}
			if err := APIKeyDelete(key.ID, ctx); err == nil || !strings.Contains(err.Error(), "delete failed") {
				t.Fatalf("expected delete failure, got %v", err)
			}
			if err := db.GetDB().First(&stored, "api_key_id = ?", key.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.InputCost != 2 || StatsAPIKeyGet(key.ID).InputCost != 2 {
				t.Fatal("delete failure reset consumption")
			}
			if _, err := APIKeyGetByAPIKey(key.APIKey, ctx); err != nil {
				t.Fatal("delete failure invalidated key")
			}
			statsAPIKeyCacheNeedUpdateLock.Lock()
			_, dirty := statsAPIKeyCacheNeedUpdate[key.ID]
			statsAPIKeyCacheNeedUpdateLock.Unlock()
			if !dirty {
				t.Fatal("delete failure discarded pending write")
			}
			var persisted model.APIKey
			if err := db.GetDB().First(&persisted, key.ID).Error; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAPIKeyDeletePreventsLateSettlement(t *testing.T) {
	ctx := setupBackupTestDB(t)
	resetStatsTestState(t)
	apiKeyCache.Clear()
	apiKeyIDMap.Clear()
	t.Cleanup(func() { apiKeyCache.Clear(); apiKeyIDMap.Clear() })
	if err := InitCache(); err != nil {
		t.Fatal(err)
	}
	key := model.APIKey{Name: "deleted", APIKey: "test-only-deleted", Enabled: true}
	if err := APIKeyCreate(&key, ctx); err != nil {
		t.Fatal(err)
	}
	if err := StatsAPIKeyUpdate(key.ID, model.StatsMetrics{InputCost: 2}); err != nil {
		t.Fatal(err)
	}
	if err := StatsSaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	if err := APIKeyDelete(key.ID, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKeyGetByAPIKey(key.APIKey, ctx); err == nil {
		t.Fatal("deleted key still resolves")
	}
	if apiKeyIDMap.Exists(key.APIKey) {
		t.Fatal("deleted key left reverse index")
	}
	if err := StatsAPIKeyUpdate(key.ID, model.StatsMetrics{InputCost: 3}); err != nil {
		t.Fatal(err)
	}
	if err := StatsSaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.GetDB().Model(&model.StatsAPIKey{}).Where("api_key_id = ?", key.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 || statsAPIKeyCache.Exists(key.ID) {
		t.Fatal("late settlement recreated deleted statistics")
	}
}
