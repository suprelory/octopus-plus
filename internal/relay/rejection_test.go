package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestEarlyRelayRejectionsAreLoggedWithoutSettlingUsage(t *testing.T) {
	for _, endpoint := range []struct {
		path   string
		handle gin.HandlerFunc
	}{
		{"/v1/responses", func(c *gin.Context) { Handler(inbound.InboundTypeOpenAIResponse, c) }},
		{"/v1/responses/compact", HandleResponsesCompact},
		{"/v1/images/generations", func(c *gin.Context) { ImagesHandler("/images/generations", c) }},
	} {
		t.Run(endpoint.path, func(t *testing.T) {
			for _, tc := range []struct {
				name, body, allowed string
				status              int
				emptyGroup          bool
			}{
				{"invalid request", `{"input":"private-input",`, "", 400, false},
				{"model forbidden", `{"model":"rejected-model","input":"private-input"}`, "allowed-model", 400, false},
				{"model missing", `{"model":"rejected-model","input":"private-input"}`, "", 404, false},
				{"no channels", `{"model":"rejected-model","input":"private-input"}`, "", 503, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx := setupHTTPRelayTestDB(t)
					modelName := strings.ReplaceAll(t.Name(), "/", "_")
					if tc.emptyGroup {
						if err := op.GroupCreate(&model.Group{Name: modelName, Mode: model.GroupModeRoundRobin}, ctx); err != nil {
							t.Fatal(err)
						}
					}
					core, entries := observer.New(zap.InfoLevel)
					previous := log.Logger
					log.Logger = zap.New(core).Sugar()
					t.Cleanup(func() { log.Logger = previous })
					router := gin.New()
					router.Use(middleware.Logger(middleware.LoggerConfig{}))
					router.POST(endpoint.path, func(c *gin.Context) {
						c.Set("api_key_id", 7)
						c.Set("supported_models", tc.allowed)
						endpoint.handle(c)
					})
					recorder := httptest.NewRecorder()
					body := strings.ReplaceAll(tc.body, "rejected-model", modelName)
					request := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(body)).WithContext(ctx)
					request.Header.Set("Content-Type", "application/json")
					router.ServeHTTP(recorder, request)
					if recorder.Code != tc.status {
						t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
					}
					if entries.Len() != 1 || entries.All()[0].Message != "relay.rejected" {
						t.Fatalf("missing or duplicate diagnostic: %+v", entries.All())
					}
					items, err := op.RelayLogList(ctx, nil, nil, nil, 1, 10)
					if err != nil || len(items) != 1 {
						t.Fatalf("rejection logs=%d err=%v", len(items), err)
					}
					item := items[0]
					if item.Success || item.TotalAttempts != 0 || item.RequestAPIKeyID != 7 || item.Error == "" || item.RequestContent != "" || strings.Contains(item.Error, "private-input") {
						t.Fatalf("unsafe or incomplete rejection: %+v", item)
					}
					if stats := op.StatsTotalGet(); stats.RequestFailed != 0 || stats.RequestSuccess != 0 || stats.InputCost != 0 || stats.OutputCost != 0 {
						t.Fatalf("rejection changed usage accounting: %+v", stats)
					}
				})
			}
		})
	}
}
