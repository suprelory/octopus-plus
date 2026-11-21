package handlers

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/backup"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestBackupV2HTTPArchiveRestore(t *testing.T) {
	ctx := setupSiteHandlerTestDB(t)
	service := backup.NewService(db.GetDB())
	if err := service.PrepareSchema(ctx); err != nil {
		t.Fatal(err)
	}
	site := model.CheckinSite{ID: 4, Name: "HTTP backup site", Platform: model.SitePlatformNewAPI, BaseURL: "https://http-backup.example.test", Enabled: true,
		CheckinNetwork: model.CheckinNetwork{ProxyMode: model.ProxyUsageModeDirect}, CheckinMode: model.SiteCheckinModeEnabled,
		CheckinTimezone: "UTC", CheckinWindowStart: "00:00", CheckinWindowEnd: "23:59"}
	if err := db.GetDB().Create(&site).Error; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/setting/export?version=2&format=zip&include_logs=true", nil).WithContext(ctx)
	exportDB(c)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" {
		t.Fatal("v2 export failed")
	}
	archive := append([]byte(nil), w.Body.Bytes()...)
	ctx = setupSiteHandlerTestDB(t)
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		file, err := writer.CreateFormFile("file", "backup.zip")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(archive); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		w = httptest.NewRecorder()
		c, _ = gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/setting/import?version=2", &body).WithContext(ctx)
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		importDB(c)
		if w.Code != http.StatusOK {
			t.Fatalf("v2 restore failed: %s", w.Body.String())
		}
		var response struct {
			Data model.DBImportResult `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Data.AlreadyImported != (attempt == 1) {
			t.Fatal("HTTP import lost duplicate result")
		}
	}
	var saved model.CheckinSite
	if err := db.GetDB().First(&saved).Error; err != nil || saved.Name != site.Name {
		t.Fatal("site not restored")
	}
	var legacyCount int64
	if err := db.GetDB().Model(&model.Site{}).Count(&legacyCount).Error; err != nil || legacyCount != 0 {
		t.Fatal("restore created legacy site")
	}
}

func TestBackupV2HTTPReportsSafeInvalidInput(t *testing.T) {
	ctx := setupSiteHandlerTestDB(t)
	document := backup.Document{DBDump: model.DBDump{Version: 2, ExportedAt: time.Now().UTC()}, CheckinCredentials: []model.CheckinCredentialBackup{{ID: 1, CheckinCredentialInput: model.CheckinCredentialInput{Password: "must-not-leak"}}}}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/setting/import?version=2", bytes.NewReader(data)).WithContext(ctx)
	importDB(c)
	if w.Code != http.StatusBadRequest || bytes.Contains(w.Body.Bytes(), []byte("must-not-leak")) {
		t.Fatal("unsafe validation response")
	}
}
