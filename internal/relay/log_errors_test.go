package relay

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestUpstreamErrorBodyNeverAppearsInConsole(t *testing.T) {
	for _, mode := range []dbmodel.ChannelPassthroughMode{dbmodel.ChannelPassthroughModeOff, dbmodel.ChannelPassthroughModeAuto} {
		for _, capture := range []bool{false, true} {
			t.Run(string(mode)+"/capture="+strconv.FormatBool(capture), func(t *testing.T) {
				ctx := setupHTTPRelayTestDB(t)
				if err := op.SettingSetString(dbmodel.SettingKeyRelayLogContentEnabled, strconv.FormatBool(capture)); err != nil {
					t.Fatal(err)
				}
				const marker = "PRIVATE_PROMPT_MARKER_78d3"
				const secret = "FAKE_PROVIDER_CREDENTIAL_61c2"
				body := fmt.Sprintf(`{"error":{"code":%q,"type":%q,"message":%q}}`, marker, secret, marker+" "+secret)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, body)
				}))
				defer upstream.Close()
				group := &dbmodel.Group{Name: "safe-console", Mode: dbmodel.GroupModeFailover}
				channels := addHTTPRelayTestChannels(t, ctx, group, mode, upstream.URL)
				core, entries := observer.New(zap.DebugLevel)
				previous := log.Logger
				log.Logger = zap.New(core).Sugar()
				defer func() { log.Logger = previous }()
				gin.SetMode(gin.TestMode)
				router := gin.New()
				router.Use(middleware.Logger(middleware.LoggerConfig{Enabled: true}))
				router.POST("/v1/responses", func(c *gin.Context) { Handler(inbound.InboundTypeOpenAIResponse, c) })
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"safe-console","input":"hello"}`)).WithContext(ctx)
				request.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusBadRequest {
					t.Fatalf("status=%d", recorder.Code)
				}
				if !strings.Contains(recorder.Body.String(), marker) {
					t.Fatal("fixture did not exercise upstream error propagation")
				}
				for _, entry := range entries.All() {
					fields, err := json.Marshal(entry.ContextMap())
					if err != nil {
						t.Fatal(err)
					}
					for _, sensitive := range []string{marker, secret} {
						if strings.Contains(entry.Message+string(fields), sensitive) {
							t.Fatal("console retained provider-controlled body or error code")
						}
					}
				}
				warnings := entries.FilterMessage("relay.upstream_error").All()
				if len(warnings) != 1 {
					t.Fatalf("safe diagnostics=%d", len(warnings))
				}
				fields := warnings[0].ContextMap()
				if fields["channel_id"] != int64(channels[0].ID) || fields["status"] != int64(400) || fields["request_id"] != recorder.Header().Get("X-Octopus-Request-Id") {
					t.Fatalf("missing correlation/status fields: %+v", fields)
				}
			})
		}
	}
}

func TestRelayErrorDiagnosticDiscardsWrappedProviderText(t *testing.T) {
	err := fmt.Errorf("wrapper containing private request: %w", relayProtocolError(400, "PRIVATE_CODE", "private plaintext prompt"))
	got := relayErrorDiagnostic(err)
	if strings.Contains(got, "private") || strings.Contains(got, "PRIVATE") || len(got) > 200 {
		t.Fatalf("unsafe diagnostic: %s", got)
	}
}
