package webdav

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/backup"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestBackupV2WebDAVRestore(t *testing.T) {
	if db.GetDB() != nil {
		_ = db.Close()
	}
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "webdav.db"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	document := backup.Document{DBDump: model.DBDump{Version: 2, ExportedAt: time.Now().UTC()}, CheckinSites: []backup.Site{{CheckinSite: model.CheckinSite{
		ID: 10, Name: "WebDAV site", Platform: model.SitePlatformNewAPI, BaseURL: "https://webdav-fixture.example", Enabled: true,
		CheckinNetwork: model.CheckinNetwork{ProxyMode: model.ProxyUsageModeDirect}, CheckinTimezone: "UTC", CheckinWindowStart: "00:00", CheckinWindowEnd: "23:59"}}}}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/backups/octopus-backup-fixture.json" {
			t.Errorf("unexpected WebDAV request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	defer server.Close()
	for key, value := range map[model.SettingKey]string{model.SettingKeyWebDAVURL: server.URL, model.SettingKeyWebDAVBackupPath: "/backups", model.SettingKeyProxyURL: ""} {
		if err := op.SettingSetString(key, value); err != nil {
			t.Fatal(err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := RestoreFromBackupVersion(t.Context(), "octopus-backup-fixture.json", 2)
		if err != nil {
			t.Fatal(err)
		}
		if result.AlreadyImported != (attempt == 1) {
			t.Fatal("WebDAV restore repeated writes")
		}
	}
	var site model.CheckinSite
	if err := db.GetDB().First(&site).Error; err != nil || site.Name != "WebDAV site" {
		t.Fatal("WebDAV restore did not use v2 importer")
	}
}
