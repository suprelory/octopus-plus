package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestConfigurationImportPreservesPendingUsage(t *testing.T) {
	ctx := setupSiteHandlerTestDB(t)
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	key := model.APIKey{APIKey: "sk-octopus-import-test", Enabled: true}
	if err := op.APIKeyCreate(&key, ctx); err != nil {
		t.Fatal(err)
	}
	metrics := model.StatsMetrics{InputCost: 7, RequestSuccess: 1}
	for _, err := range []error{op.StatsAPIKeyUpdate(key.ID, metrics), op.StatsTotalUpdate(metrics), op.StatsDailyUpdate(ctx, metrics), op.StatsHourlyUpdate(metrics)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/setting/import", strings.NewReader(`{"version":1,"include_stats":false,"settings":[{"key":"empty_response_detection_enabled","value":"false"}]}`)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	importDB(c)
	if w.Code != http.StatusOK {
		t.Fatalf("import failed: %d %s", w.Code, w.Body.String())
	}
	if value, err := op.SettingGetBool(model.SettingKeyEmptyResponseDetectionEnabled); err != nil || value {
		t.Fatalf("configuration cache not refreshed: %v %v", value, err)
	}
	if op.StatsAPIKeyGet(key.ID).InputCost != 7 || op.StatsTotalGet().InputCost != 7 || op.StatsTodayGet().InputCost != 7 {
		t.Fatal("configuration import discarded pending usage")
	}
	foundHourly := false
	for _, row := range op.StatsHourlyGet() {
		if row.Hour == time.Now().Hour() && row.InputCost == 7 {
			foundHourly = true
		}
	}
	if !foundHourly {
		t.Fatal("hourly usage discarded")
	}
	if err := op.StatsSaveDB(ctx); err != nil {
		t.Fatal(err)
	}
	var stored model.StatsAPIKey
	if err := db.GetDB().First(&stored, "api_key_id = ?", key.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.InputCost != 7 {
		t.Fatalf("pending write marker lost: %+v", stored)
	}
}
