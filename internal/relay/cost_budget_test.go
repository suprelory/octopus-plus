package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/apperror"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func setupCostRoute(t *testing.T, ctx context.Context, upstream string, mode dbmodel.ChannelPassthroughMode) (*gin.Engine, dbmodel.APIKey, *dbmodel.Group, *dbmodel.Channel) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	name := strings.ToLower(t.Name())
	group := &dbmodel.Group{Name: name, Mode: dbmodel.GroupModeFailover}
	channels := addHTTPRelayTestChannels(t, ctx, group, mode, upstream)
	if err := op.LLMCreate(dbmodel.LLMInfo{Name: name, LLMPrice: dbmodel.LLMPrice{Input: 1, Output: 1}}, ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = op.LLMDelete(name, ctx) })
	key := dbmodel.APIKey{Name: "limited", APIKey: "sk-octopus-test-cost", Enabled: true, MaxCost: 1}
	if err := op.APIKeyCreate(&key, ctx); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/v1/responses", middleware.APIKeyAuth(), func(c *gin.Context) { Handler(inbound.InboundTypeOpenAIResponse, c) })
	router.POST("/v1/responses/compact", middleware.APIKeyAuth(), HandleResponsesCompact)
	router.POST("/v1/images/generations", middleware.APIKeyAuth(), func(c *gin.Context) { ImagesHandler("/images/generations", c) })
	return router, key, group, channels[0]
}

func costHTTPRequest(ctx context.Context, router http.Handler, key dbmodel.APIKey, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+key.APIKey)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestCostLimitedHTTPRejectsConcurrentRequestsUntilSettlement(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, relayTestResponseJSON("resp_budget", "ok"))
	}))
	defer upstream.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	router, key, group, _ := setupCostRoute(t, ctx, upstream.URL, dbmodel.ChannelPassthroughModeOff)
	body := fmt.Sprintf(`{"model":%q,"input":"hello","max_output_tokens":10}`, group.Name)
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- costHTTPRequest(ctx, router, key, "/v1/responses", body) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first request did not reach upstream")
	}
	const workers = 16
	results := make(chan *httptest.ResponseRecorder, workers)
	for range workers {
		go func() { results <- costHTTPRequest(ctx, router, key, "/v1/responses", body) }()
	}
	for range workers {
		response := <-results
		if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
			t.Fatalf("concurrent admission=%d", response.Code)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream received %d concurrent paid requests", hits.Load())
	}
	close(release)
	if response := <-first; response.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", response.Code, response.Body.String())
	}
	stats := op.StatsAPIKeyGet(key.ID)
	if stats.RequestSuccess != 1 || stats.InputCost+stats.OutputCost <= 0 {
		t.Fatalf("reservation released without settlement: %+v", stats)
	}
	if response := costHTTPRequest(ctx, router, key, "/v1/responses", body); response.Code != http.StatusOK {
		t.Fatalf("settled reservation still held: %d %s", response.Code, response.Body.String())
	}
}

func TestCostLimitChecksFinalHTTPPayload(t *testing.T) {
	for _, mode := range []dbmodel.ChannelPassthroughMode{dbmodel.ChannelPassthroughModeOff, dbmodel.ChannelPassthroughModeAuto} {
		for _, scenario := range []string{"missing-limit", "too-expensive", "override-limit", "unknown-price", "images", "compact", "allowed"} {
			t.Run(string(mode)+"/"+scenario, func(t *testing.T) {
				ctx := setupHTTPRelayTestDB(t)
				var hits atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, relayTestResponseJSON("resp_limit", "ok"))
				}))
				defer upstream.Close()
				router, key, group, channel := setupCostRoute(t, ctx, upstream.URL, mode)
				path := "/v1/responses"
				payload := map[string]any{"model": group.Name, "input": "hello", "max_output_tokens": 10}
				switch scenario {
				case "missing-limit":
					delete(payload, "max_output_tokens")
				case "too-expensive":
					payload["max_output_tokens"] = 10000000
				case "override-limit":
					override := `{"max_output_tokens":10000000}`
					if _, err := op.ChannelUpdate(&dbmodel.ChannelUpdateRequest{ID: channel.ID, ParamOverride: &override}, ctx); err != nil {
						t.Fatal(err)
					}
				case "unknown-price":
					if err := op.LLMDelete(group.Name, ctx); err != nil {
						t.Fatal(err)
					}
				case "images":
					path = "/v1/images/generations"
				case "compact":
					path = "/v1/responses/compact"
				}
				body, _ := json.Marshal(payload)
				response := costHTTPRequest(ctx, router, key, path, string(body))
				want, wantHits := http.StatusBadRequest, int32(0)
				if scenario == "allowed" {
					want, wantHits = http.StatusOK, 1
				}
				if response.Code != want || hits.Load() != wantHits {
					t.Fatalf("status=%d hits=%d body=%s", response.Code, hits.Load(), response.Body.String())
				}
				if wantHits == 0 && !strings.Contains(response.Body.String(), apperror.CodeAuthAPIKeyCostExceeded) {
					t.Fatalf("missing cost rejection code: %s", response.Body.String())
				}
				reservation, err := op.APIKeyReserveCost(key.ID)
				if err != nil {
					t.Fatalf("completion leaked reservation: %v", err)
				}
				reservation.Release()
			})
		}
	}
}

func TestCostLimitedCancellationReleasesReservation(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	entered := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	router, key, group, _ := setupCostRoute(t, ctx, upstream.URL, dbmodel.ChannelPassthroughModeOff)
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		costHTTPRequest(requestCtx, router, key, "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hello","max_output_tokens":10}`, group.Name))
	}()
	select {
	case <-entered:
	case <-requestCtx.Done():
		t.Fatal("request did not start")
	}
	cancel()
	<-done
	reservation, err := op.APIKeyReserveCost(key.ID)
	if err != nil {
		t.Fatalf("cancel leaked reservation: %v", err)
	}
	reservation.Release()
}

func TestCostLimitIncludesRetries(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, `{"error":{"message":"temporarily unavailable"}}`, 503)
	}))
	defer upstream.Close()
	router, key, group, _ := setupCostRoute(t, ctx, upstream.URL, dbmodel.ChannelPassthroughModeOff)
	key.MaxCost = .0025
	if err := op.APIKeyUpdate(&key, ctx); err != nil {
		t.Fatal(err)
	}
	if err := op.LLMUpdate(dbmodel.LLMInfo{Name: group.Name, LLMPrice: dbmodel.LLMPrice{Output: 1000}}, ctx); err != nil {
		t.Fatal(err)
	}
	enabled, retries := true, 3
	if _, err := op.GroupUpdate(&dbmodel.GroupUpdateRequest{ID: group.ID, RetryEnabled: &enabled, MaxRetries: &retries}, ctx); err != nil {
		t.Fatal(err)
	}
	response := costHTTPRequest(ctx, router, key, "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hello","max_output_tokens":1}`, group.Name))
	if hits.Load() != 2 || response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), apperror.CodeAuthAPIKeyCostExceeded) {
		t.Fatalf("retry exceeded budget: hits=%d status=%d body=%s", hits.Load(), response.Code, response.Body.String())
	}
}

func TestMaximumRequestCostRejectsUnboundedInputs(t *testing.T) {
	price := dbmodel.LLMPrice{Input: 1, Output: 2}
	for _, body := range []string{
		`{"max_output_tokens":null}`, `{"max_output_tokens":-1}`, `{"max_output_tokens":1.5}`,
		`{"max_output_tokens":10,"previous_response_id":"remote"}`,
		`{"max_output_tokens":10,"input":[{"type":"input_image","image_url":"https://example.invalid/image"}]}`,
		`{"max_output_tokens":10,"tools":[{"type":"web_search"}]}`,
		`{"max_output_tokens":10,"modalities":["audio"]}`,
	} {
		if _, err := maximumRequestCost(outbound.OutboundTypeOpenAIResponse, []byte(body), price); err == nil {
			t.Fatalf("accepted unbounded request: %s", body)
		}
	}
	for _, value := range []float64{-1, math.NaN(), math.Inf(1)} {
		if _, err := maximumRequestCost(outbound.OutboundTypeOpenAIResponse, []byte(`{"max_output_tokens":1}`), dbmodel.LLMPrice{Input: value, Output: 1}); err == nil {
			t.Fatal("accepted invalid price")
		}
	}
	for protocol, body := range map[outbound.OutboundType]string{
		outbound.OutboundTypeOpenAIChat:      `{"messages":[],"max_completion_tokens":10,"n":2}`,
		outbound.OutboundTypeOpenAIResponse:  `{"input":"hello","max_output_tokens":10}`,
		outbound.OutboundTypeAnthropic:       `{"messages":[],"max_tokens":10}`,
		outbound.OutboundTypeGemini:          `{"contents":[],"generationConfig":{"maxOutputTokens":10,"candidateCount":2}}`,
		outbound.OutboundTypeOpenAIEmbedding: `{"input":[1,2,3]}`,
	} {
		if cost, err := maximumRequestCost(protocol, []byte(body), price); err != nil || cost <= 0 {
			t.Fatalf("protocol %v: cost=%v err=%v", protocol, cost, err)
		}
	}
}

func TestCostLimitedWebSocketAdmissionAndSubmission(t *testing.T) {
	for _, mode := range []dbmodel.ChannelWSMode{dbmodel.ChannelWSModeTransform, dbmodel.ChannelWSModePassthrough} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := setupHTTPRelayTestDB(t)
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := op.SettingSetString(dbmodel.SettingKeyResponsesWSEnabled, "true"); err != nil {
				t.Fatal(err)
			}
			var hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				for {
					_, data, err := conn.Read(r.Context())
					if err != nil {
						return
					}
					hits.Add(1)
					if !strings.Contains(string(data), `"max_output_tokens":10`) {
						t.Error("output bound was lost in WS payload")
					}
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_ws_cost","model":"model_1"}}`))
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.output_text.delta","delta":"ok"}`))
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":`+relayTestResponseJSON("resp_ws_cost", "ok")+`}`))
					break
				}
			}))
			defer upstream.Close()
			router, key, group, channel := setupCostRoute(t, ctx, upstream.URL, dbmodel.ChannelPassthroughModeAuto)
			if _, err := op.ChannelUpdate(&dbmodel.ChannelUpdateRequest{ID: channel.ID, WSMode: &mode}, ctx); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			router.GET("/v1/responses", middleware.APIKeyWSAuth(), func(c *gin.Context) { defer close(done); HandleWSResponse(c) })
			server := httptest.NewServer(router)
			defer server.Close()
			reservation, err := op.APIKeyReserveCost(key.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer reservation.Release()
			conn, _, err := websocket.Dial(ctx, server.URL+"/v1/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + key.APIKey}}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			send := func(limit string) {
				t.Helper()
				body := fmt.Sprintf(`{"type":"response.create","model":%q,"input":"hello"%s}`, group.Name, limit)
				if err := conn.Write(ctx, websocket.MessageText, []byte(body)); err != nil {
					t.Fatal(err)
				}
			}
			readUntil := func(want string) string {
				t.Helper()
				for {
					_, data, err := conn.Read(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(data), want) {
						return string(data)
					}
					if strings.Contains(string(data), `"type":"error"`) {
						t.Fatalf("unexpected WS error: %s", data)
					}
				}
			}
			send(`,"max_output_tokens":10`)
			readUntil(apperror.CodeAuthAPIKeyRateLimited)
			reservation.Release()
			send("")
			readUntil(apperror.CodeAuthAPIKeyCostExceeded)
			send(`,"max_output_tokens":10`)
			readUntil("response.completed")
			conn.CloseNow()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("WS request did not finish")
			}
			if hits.Load() != 1 {
				t.Fatalf("rejected WS turns reached upstream: %d", hits.Load())
			}
			stats := op.StatsAPIKeyGet(key.ID)
			if stats.RequestSuccess != 1 || stats.OutputCost <= 0 {
				t.Fatalf("WS settlement missing: %+v", stats)
			}
			available, err := op.APIKeyReserveCost(key.ID)
			if err != nil {
				t.Fatalf("WS leaked reservation: %v", err)
			}
			available.Release()
		})
	}
}
