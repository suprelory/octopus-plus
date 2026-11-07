package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
)

func TestWebSocketRevalidatesEachRequest(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	key := model.APIKey{APIKey: "sk-octopus-ws-auth-test", Enabled: true, MaxRPM: 1}
	if err := op.APIKeyCreate(&key, ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.RateLimitDel(key.ID) })
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/v1/responses", middleware.APIKeyWSAuth(), HandleWSResponse)
	server := httptest.NewServer(router)
	defer server.Close()
	wsctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(wsctx, server.URL+"/v1/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + key.APIKey}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	request := func(wantCode string) map[string]any {
		t.Helper()
		if err := conn.Write(wsctx, websocket.MessageText, []byte(`{"type":"response.create","model":"missing-auth-test-model","input":"hello"}`)); err != nil {
			t.Fatal(err)
		}
		_, data, err := conn.Read(wsctx)
		if err != nil {
			t.Fatal(err)
		}
		var event struct {
			Type       string         `json:"type"`
			Error      map[string]any `json:"error"`
			RetryAfter int            `json:"retry_after"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != "error" || event.Error["code"] != wantCode {
			t.Fatalf("want %s, got %s", wantCode, data)
		}
		if wantCode == apperror.CodeAuthAPIKeyRateLimited && event.RetryAfter <= 0 {
			t.Fatal("missing retry_after")
		}
		return event.Error
	}
	update := func() {
		t.Helper()
		if err := op.APIKeyUpdate(&key, ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Admission succeeds once (routing then fails for the absent model). The
	// upgrade must not consume the sole RPM slot.
	request("model_not_found")
	request(apperror.CodeAuthAPIKeyRateLimited)
	key.MaxRPM = 0
	key.Enabled = false
	update()
	request(apperror.CodeAuthAPIKeyDisabled)
	key.Enabled = true
	key.ExpireAt = time.Now().Unix()
	update()
	request(apperror.CodeAuthAPIKeyExpired)
	key.ExpireAt = 0
	key.MaxCost = 1
	update()
	if err := op.StatsAPIKeyUpdate(key.ID, model.StatsMetrics{InputCost: 1}); err != nil {
		t.Fatal(err)
	}
	request(apperror.CodeAuthAPIKeyCostExceeded)
	key.MaxCost = 0
	key.SupportedModels = "another-model"
	update()
	if got := request("invalid_request"); got["message"] != "model not supported" {
		t.Fatalf("stale model permissions: %v", got)
	}
	key.SupportedModels = ""
	update()
	request("model_not_found")
	if err := op.APIKeyDelete(key.ID, ctx); err != nil {
		t.Fatal(err)
	}
	request(apperror.CodeAuthInvalidToken)
}
