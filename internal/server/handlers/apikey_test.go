package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/auth"
)

func TestCreateAPIKeyEnabledContract(t *testing.T) {
	ctx := setupSiteHandlerTestDB(t)
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, body string
		enabled    bool
	}{
		{"disabled", `{"name":"disabled","enabled":false}`, false},
		{"enabled", `{"name":"enabled","enabled":true}`, true},
		{"default", `{"name":"default"}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/apikey/create", strings.NewReader(tt.body)).WithContext(ctx)
			c.Request.Header.Set("Content-Type", "application/json")
			createAPIKey(c)
			if w.Code != http.StatusOK {
				t.Fatalf("create returned %d", w.Code)
			}
			var response struct {
				Data model.APIKey `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			var stored model.APIKey
			if err := db.GetDB().First(&stored, response.Data.ID).Error; err != nil {
				t.Fatal(err)
			}
			cached, err := op.APIKeyGetByAPIKey(response.Data.APIKey, ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []model.APIKey{response.Data, stored, cached} {
				if key.Enabled != tt.enabled {
					t.Fatalf("enabled = %v, want %v", key.Enabled, tt.enabled)
				}
			}
			authErr, _ := auth.ValidateAPIKey(cached, false)
			if (authErr == nil) != tt.enabled {
				t.Fatal("authentication disagrees with requested state")
			}
		})
	}
}
