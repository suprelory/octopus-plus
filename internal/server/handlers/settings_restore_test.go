package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/webdav"
)

func TestRestoreAppliesTrustedProxiesForJSONAndWebDAV(t *testing.T) {
	for _, source := range []string{"json", "webdav"} {
		for _, tc := range []struct {
			name, value string
			count       int
			invalid     bool
		}{
			{"empty", "", 0, false},
			{"narrowed", "127.0.0.1/32", 1, false},
			{"invalid", "not-an-ip-or-cidr", 0, true},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				ctx := setupSiteHandlerTestDB(t)
				if err := op.InitCache(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = middleware.ConfigureTrustedProxies("") })
				if err := op.SettingSetString(model.SettingKeyTrustedProxies, "127.0.0.0/8,10.0.0.0/8"); err != nil {
					t.Fatal(err)
				}
				if middleware.TrustedProxyCount() != 2 {
					t.Fatal("initial trust setting was not applied")
				}
				dump := model.DBDump{Version: 1, Settings: []model.Setting{{Key: model.SettingKeyTrustedProxies, Value: tc.value}}}
				data, err := json.Marshal(dump)
				if err != nil {
					t.Fatal(err)
				}
				if source == "webdav" {
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Method != http.MethodGet || r.URL.Path != "/backups/octopus-backup-test.json" {
							http.NotFound(w, r)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write(data)
					}))
					defer upstream.Close()
					if err := op.SettingSetString(model.SettingKeyWebDAVURL, upstream.URL); err != nil {
						t.Fatal(err)
					}
					if err := op.SettingSetString(model.SettingKeyWebDAVBackupPath, "/backups"); err != nil {
						t.Fatal(err)
					}
					_, err = webdav.RestoreFromBackup(ctx, "octopus-backup-test.json")
					if (err != nil) != tc.invalid {
						t.Fatalf("restore error = %v, want failure=%t", err, tc.invalid)
					}
				} else {
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/import", bytes.NewReader(data))
					c.Request.Header.Set("Content-Type", "application/json")
					importDB(c)
					want := http.StatusOK
					if tc.invalid {
						want = http.StatusInternalServerError
					}
					if recorder.Code != want {
						t.Fatalf("restore status=%d want=%d: %s", recorder.Code, want, recorder.Body.String())
					}
				}
				value, _ := op.SettingGetString(model.SettingKeyTrustedProxies)
				var saved model.Setting
				if err := db.GetDB().First(&saved, "key = ?", model.SettingKeyTrustedProxies).Error; err != nil {
					t.Fatal(err)
				}
				if value != tc.value || saved.Value != tc.value || middleware.TrustedProxyCount() != tc.count {
					t.Fatalf("restored trust mismatch: cache=%q db=%q active=%d", value, saved.Value, middleware.TrustedProxyCount())
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
				c.Request.RemoteAddr = "10.0.0.1:1234"
				c.Request.Header.Set("X-Forwarded-For", "203.0.113.9")
				if ip := middleware.ClientIP(c); ip != "10.0.0.1" {
					t.Fatalf("restored resolver retained old trust: %s", ip)
				}
			})
		}
	}
}
