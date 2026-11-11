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
	"github.com/suprelory/octopus-plus/internal/server/middleware"
)

func TestCheckinRewardExtractorPreview(t *testing.T) {
	setupSiteHandlerTestDB(t)
	for _, tc := range []struct {
		name, body, reward string
		status             int
		found              bool
	}{
		{"reward", `{"code":"return response.data.amount;","response":{"data":{"amount":0.25}}}`, "0.25", 200, true},
		{"zero", `{"code":"return response.reward;","response":{"reward":0}}`, "0", 200, true},
		{"status response", `{"code":"return responses.status.data.reward;","response":null,"status_response":{"data":{"reward":1.5}}}`, "1.5", 200, true},
		{"missing", `{"code":"return null;","response":{}}`, "", 200, false},
		{"missing code", `{"response":{}}`, "", 400, false},
		{"missing response", `{"code":"return 1;"}`, "", 400, false},
		{"invalid JSON", `{`, "", 400, false},
		{"wrong type", `{"code":7,"response":{}}`, "", 400, false},
		{"invalid result", `{"code":"return -1;","response":{}}`, "", 400, false},
		{"safe exception", `{"code":"throw response.secret;","response":{"secret":"private-response-value"}}`, "", 400, false},
		{"oversized body", `{"code":"return 1;","response":"` + strings.Repeat("a", 2<<20) + `"}`, "", 400, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/site/checkin-reward/test", strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			testCheckinRewardExtractor(c)
			if w.Code != tc.status {
				t.Fatalf("status=%d, body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private-response-value") {
				t.Fatal("response data leaked through exception")
			}
			if tc.status == 200 {
				var response struct {
					Data struct {
						Reward string `json:"reward"`
						Found  bool   `json:"found"`
					} `json:"data"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Data.Reward != tc.reward || response.Data.Found != tc.found {
					t.Fatalf("unexpected preview: %s", w.Body.String())
				}
			}
		})
	}
	var logs int64
	if err := db.GetDB().Model(&model.SiteCheckinLog{}).Count(&logs).Error; err != nil || logs != 0 {
		t.Fatalf("preview created checkin history: %d, %v", logs, err)
	}
}

func TestCheckinRewardPreviewRequiresAuthentication(t *testing.T) {
	engine := gin.New()
	engine.POST("/api/v1/site/checkin-reward/test", middleware.Auth(), middleware.RequireJSON(), testCheckinRewardExtractor)
	w := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/site/checkin-reward/test", strings.NewReader(`{"code":"while(true){}","response":{}}`))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w, request)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated preview accepted: %d", w.Code)
	}
}

func TestCheckinStatusRulePreviewAndAuthentication(t *testing.T) {
	setupSiteHandlerTestDB(t)
	for _, tc := range []struct {
		body, state string
		status      int
	}{
		{`{"config":{"path":"/status","checked_path":"data.done"},"http_status":200,"date":"2026-11-11","response":{"data":{"done":true}}}`, "checked_in", 200},
		{`{"config":{"path":"/status","checked_path":"data.done"},"http_status":200,"date":"2026-11-11","response":{}}`, "unknown", 200},
		{`{"config":{"path":"/status","code":"throw response.secret"},"http_status":200,"date":"2026-11-11","response":{"secret":"private-response-value"}}`, "", 400},
		{`{"config":{"path":"https://external.example/status","checked_path":"done"},"http_status":200,"date":"2026-11-11","response":{}}`, "", 400},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/site/checkin-status/test", strings.NewReader(tc.body))
		testCheckinStatusRule(c)
		if w.Code != tc.status || strings.Contains(w.Body.String(), "private-response-value") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if tc.state != "" && !strings.Contains(w.Body.String(), tc.state) {
			t.Fatalf("missing state: %s", w.Body.String())
		}
	}
	var logs int64
	if err := db.GetDB().Model(&model.SiteCheckinLog{}).Count(&logs).Error; err != nil || logs != 0 {
		t.Fatal("preview created execution records")
	}
	engine := gin.New()
	engine.POST("/api/v1/site/checkin-status/test", middleware.Auth(), middleware.RequireJSON(), testCheckinStatusRule)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/site/checkin-status/test", strings.NewReader(`{}`)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status preview accepted: %d", w.Code)
	}
}
