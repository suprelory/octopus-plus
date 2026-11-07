package relay

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	transformerModel "github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestKeyFailuresRotateWithinChannelAndRespectBudget(t *testing.T) {
	for _, operation := range []string{"text", "images", "compact"} {
		for _, limited := range []bool{false, true} {
			t.Run(operation+map[bool]string{false: "/rotate", true: "/budget"}[limited], func(t *testing.T) {
				ctx := setupHTTPRelayTestDB(t)
				if limited {
					if err := op.SettingSetInt(dbmodel.SettingKeyRelayMaxTotalAttempts, 1); err != nil {
						t.Fatal(err)
					}
				}
				var badHits, goodHits atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") == "Bearer key-0" {
						badHits.Add(1)
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = io.WriteString(w, `{"error":{"code":"invalid_api_key","message":"invalid api key"}}`)
						return
					}
					goodHits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					if operation == "images" {
						_, _ = io.WriteString(w, `{"data":[{"url":"image"}]}`)
					} else {
						_, _ = io.WriteString(w, relayTestResponseJSON("resp_ok", "answer"))
					}
				}))
				defer server.Close()
				group := &dbmodel.Group{Name: "key-rotation", Mode: dbmodel.GroupModeFailover, RetryEnabled: true, MaxRetries: 3}
				channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, server.URL)
				update := &dbmodel.ChannelUpdateRequest{ID: channels[0].ID, KeysToAdd: []dbmodel.ChannelKeyAddRequest{{Enabled: true, ChannelKey: "good"}}}
				if operation == "images" {
					typ := outbound.OutboundTypeOpenAIChat
					update.Type = &typ
				}
				channel, err := op.ChannelUpdate(update, ctx)
				if err != nil {
					t.Fatal(err)
				}
				balancer.SetRoutingAffinity(7, group.ID, group.Name, channel.ID, channels[0].Keys[0].ID)
				c, recorder := newHTTPRelayTestContext(ctx, `{"model":"key-rotation","input":"hello","prompt":"hello"}`)
				switch operation {
				case "images":
					ImagesHandler("/images/generations", c)
				case "compact":
					HandleResponsesCompact(c)
				default:
					Handler(inbound.InboundTypeOpenAIResponse, c)
				}
				wantGood, wantStatus := int32(1), http.StatusOK
				if limited {
					wantGood, wantStatus = 0, http.StatusGatewayTimeout
				}
				if badHits.Load() != 1 || goodHits.Load() != wantGood || recorder.Code != wantStatus {
					t.Fatalf("bad=%d good=%d status=%d body=%s", badHits.Load(), goodHits.Load(), recorder.Code, recorder.Body.String())
				}
				if balancer.CanAttempt(channel.ID, channels[0].Keys[0].ID, "other-model") {
					t.Fatal("bad key was not isolated across models")
				}
				logs, err := op.RelayLogList(ctx, nil, nil, nil, 1, 10)
				if err != nil || len(logs) != 1 || logs[0].Attempts[0].FailureScope != "key" {
					t.Fatalf("logs=%+v err=%v", logs, err)
				}
				assertHTTPRelayKeysReleased(t, []*dbmodel.Channel{channel})
			})
		}
	}
}

func TestFailureScopesDistinguishIndependentQuota(t *testing.T) {
	for _, tc := range []struct{ message, scope string }{
		{"api_key_quota_exceeded", "key"}, {"insufficient_quota: account billing hard limit", "channel"},
		{"key_rate_limit_exceeded", "key"}, {"rate_limit_exceeded", "channel"}, {"model_not_found", "model"},
	} {
		t.Run(tc.message, func(t *testing.T) {
			failure := classifyRelayFailure(429, relayProtocolError(429, tc.message, tc.message), time.Time{})
			// A model error must be explicit rather than a generic 429.
			if tc.scope == "model" {
				failure = classifyRelayFailure(400, relayProtocolError(400, tc.message, tc.message), time.Time{})
			}
			if failure.Scope != tc.scope {
				t.Fatalf("failure=%+v", failure)
			}
			result := attemptResult{Failure: failure}
			if mayRotateKey(result, true) {
				t.Fatal("native continuation must not rotate keys")
			}
			result.Written = true
			if mayRotateKey(result, false) {
				t.Fatal("committed response must not rotate keys")
			}
		})
	}
}

func TestSiteQuotaFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		class     FailureClass
		scope     string
		retryable bool
	}{
		{"new-api balance code", 403, `{"error":{"code":"insufficient_user_quota","type":"new_api_error","message":"用户额度不足, 剩余额度: ＄0.000000 (request id: example)"}}`, FailureQuota, "channel", false},
		{"new-api Claude balance", 403, `{"type":"error","error":{"type":"new_api_error","message":"用户额度不足, 剩余额度: ＄0.000000"}}`, FailureQuota, "channel", false},
		{"new-api Claude pre-consume", 403, `{"type":"error","error":{"type":"new_api_error","message":"预扣费额度失败, 用户剩余额度: ＄0.010000, 需要预扣费额度: ＄0.020000"}}`, FailureQuota, "channel", false},
		{"new-api Claude subscription", 403, `{"type":"error","error":{"type":"new_api_error","message":"订阅额度不足或未配置订阅: subscription quota insufficient"}}`, FailureQuota, "channel", false},
		{"new-api token quota", 403, `{"error":{"code":"pre_consume_token_quota_failed","type":"new_api_error","message":"token quota is not enough, token remain quota: ＄0.010000, need quota: ＄0.020000"}}`, FailureQuota, "key", false},
		{"sub2api balance", 403, `{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`, FailureQuota, "channel", false},
		{"sub2api billing type", 403, `{"error":{"type":"billing_error","message":"insufficient balance"}}`, FailureQuota, "channel", false},
		{"sub2api billing code", 403, `{"error":{"code":"billing_error","message":"insufficient balance"}}`, FailureQuota, "channel", false},
		{"sub2api Gemini balance", 403, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Insufficient account balance"}}`, FailureQuota, "channel", false},
		{"sub2api Responses key quota", 429, `{"error":{"code":"insufficient_quota","type":"insufficient_quota","param":null,"message":"API key 额度已用完"}}`, FailureQuota, "key", false},
		{"sub2api legacy key quota", 429, `{"code":"API_KEY_QUOTA_EXHAUSTED","message":"API key 额度已用完"}`, FailureQuota, "key", false},
		{"generic quota keeps retry policy", 429, `{"error":{"code":"insufficient_quota","message":"Check your plan and billing details"}}`, FailureQuota, "channel", true},
		{"unrelated billing failure", 403, `{"error":{"type":"billing_error","message":"billing backend unavailable"}}`, FailurePermission, "key", false},
		{"token reservation database failure", 403, `{"error":{"code":"pre_consume_token_quota_failed","message":"database unavailable"}}`, FailurePermission, "key", false},
		{"ordinary permission failure", 403, `{"error":{"code":"access_denied","message":"access denied"}}`, FailurePermission, "key", false},
		{"ordinary rate limit", 429, `{"error":{"code":"rate_limit_exceeded","message":"Too many requests"}}`, FailureRateLimit, "channel", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstreamErr := transformerModel.NormalizeHTTPError(tc.status, nil, []byte(tc.body), "api_error")
			err := fmt.Errorf("upstream request: %w", upstreamErr)
			retryAt := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
			failure := classifyRelayFailure(tc.status, err, retryAt)
			if failure.Class != tc.class || failure.Scope != tc.scope || failure.Retryable != tc.retryable || !failure.Record || !failure.RetryAt.Equal(retryAt) {
				t.Fatalf("classification = %+v, want class=%s scope=%s retryable=%t with retry deadline retained", failure, tc.class, tc.scope, tc.retryable)
			}
			if tc.class != FailureQuota {
				return
			}
			publicErr, ok := classifyWSPublicError(err, tc.status)
			if !ok || publicErr.Status != http.StatusServiceUnavailable || publicErr.Code != "upstream_quota_exceeded" {
				t.Fatalf("public quota error = %+v, recognized=%t", publicErr, ok)
			}
			if !tc.retryable && decideRetry(attemptResult{Failure: failure}, true, false) != retryNextCandidate {
				t.Fatal("exhausted balance must not retry the same credential")
			}
		})
	}
}

func TestSiteQuotaStreamErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event string
		scope string
	}{
		{"new-api error", `{"type":"error","status":403,"error":{"code":"insufficient_user_quota","type":"new_api_error","message":"用户额度不足, 剩余额度: ＄0.000000"}}`, "channel"},
		{"sub2api response.failed", `{"type":"response.failed","sequence_number":0,"response":{"id":"resp_failed","object":"response","created_at":1,"status":"failed","output":[],"error":{"code":"billing_error","message":"insufficient balance"}}}`, "channel"},
		{"sub2api error", `{"type":"error","error":{"type":"billing_error","message":"insufficient balance"}}`, "channel"},
		{"sub2api key quota", `{"type":"error","status":429,"error":{"code":"insufficient_quota","message":"API key 额度已用完"}}`, "key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stats := &wsPassthroughStats{}
			observeWSPassthroughEvent(stats, []byte(tc.event))
			if stats.Error == nil {
				t.Fatal("expected upstream error event")
			}
			failure := classifyRelayFailure(stats.Error.Status, stats.Error, stats.Error.RetryAt)
			if failure.Class != FailureQuota || failure.Scope != tc.scope || failure.Retryable {
				t.Fatalf("stream classification = %+v, want non-retryable quota scoped to %s", failure, tc.scope)
			}
			publicErr, ok := classifyWSPublicError(stats.Error, stats.Error.Status)
			if !ok || publicErr.Code != "upstream_quota_exceeded" || publicErr.Status != http.StatusServiceUnavailable {
				t.Fatalf("stream public error = %+v, recognized=%t", publicErr, ok)
			}
		})
	}
}

func TestSiteQuotaFailuresUseCorrectFailoverScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		scope  string
		stream bool
	}{
		{"new-api balance", 403, `{"error":{"code":"insufficient_user_quota","type":"new_api_error","message":"用户额度不足, 剩余额度: ＄0.000000"}}`, "channel", false},
		{"sub2api balance", 403, `{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`, "channel", false},
		{"new-api key quota", 403, `{"error":{"code":"pre_consume_token_quota_failed","message":"token quota is not enough, token remain quota: 0, need quota: 1"}}`, "key", false},
		{"sub2api key quota", 429, `{"error":{"code":"insufficient_quota","type":"insufficient_quota","message":"API key 额度已用完"}}`, "key", false},
		{"sub2api response.failed", 200, ": keepalive\n\nevent: response.failed\ndata: {\"type\":\"response.failed\",\"sequence_number\":0,\"response\":{\"id\":\"resp_failed\",\"object\":\"response\",\"created_at\":1,\"model\":\"model_1\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"billing_error\",\"message\":\"insufficient balance\"}}}\n\n", "channel", true},
	} {
		for _, operation := range []string{"text", "passthrough", "images", "compact"} {
			if tc.stream && (operation == "images" || operation == "compact") {
				continue
			}
			t.Run(tc.name+"/"+operation, func(t *testing.T) {
				ctx := setupHTTPRelayTestDB(t)
				successBody, successType := relayTestResponseJSON("resp_ok", "answer"), "application/json"
				if operation == "images" {
					successBody = `{"data":[{"url":"image"}]}`
				}
				if tc.stream {
					successBody, successType = relayTestResponseSSE("resp_ok", "answer"), "text/event-stream"
				}
				var failedHits, alternateKeyHits, fallbackHits atomic.Int32
				failedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") == "Bearer key-0" {
						failedHits.Add(1)
						w.Header().Set("Content-Type", successType)
						w.WriteHeader(tc.status)
						_, _ = io.WriteString(w, tc.body)
						return
					}
					alternateKeyHits.Add(1)
					w.Header().Set("Content-Type", successType)
					_, _ = io.WriteString(w, successBody)
				}))
				defer failedServer.Close()
				fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					fallbackHits.Add(1)
					w.Header().Set("Content-Type", successType)
					_, _ = io.WriteString(w, successBody)
				}))
				defer fallbackServer.Close()

				mode := dbmodel.ChannelPassthroughModeOff
				if operation == "passthrough" {
					mode = dbmodel.ChannelPassthroughModeAuto
				}
				group := &dbmodel.Group{Name: "site-quota", Mode: dbmodel.GroupModeFailover, RetryEnabled: true, MaxRetries: 3}
				channels := addHTTPRelayTestChannels(t, ctx, group, mode, failedServer.URL, fallbackServer.URL)
				update := &dbmodel.ChannelUpdateRequest{ID: channels[0].ID, KeysToAdd: []dbmodel.ChannelKeyAddRequest{{Enabled: true, ChannelKey: "alternate"}}}
				if operation == "images" {
					typ := outbound.OutboundTypeOpenAIChat
					update.Type = &typ
					if _, err := op.ChannelUpdate(&dbmodel.ChannelUpdateRequest{ID: channels[1].ID, Type: &typ}, ctx); err != nil {
						t.Fatal(err)
					}
				}
				channel, err := op.ChannelUpdate(update, ctx)
				if err != nil {
					t.Fatal(err)
				}
				channels[0] = channel
				balancer.SetRoutingAffinity(7, group.ID, group.Name, channel.ID, channel.Keys[0].ID)
				c, recorder := newHTTPRelayTestContext(ctx, fmt.Sprintf(`{"model":"site-quota","input":"hello","prompt":"hello","stream":%t}`, tc.stream))
				switch operation {
				case "images":
					ImagesHandler("/images/generations", c)
				case "compact":
					HandleResponsesCompact(c)
				default:
					Handler(inbound.InboundTypeOpenAIResponse, c)
				}
				wantAlternate, wantFallback, finalChannel := int32(0), int32(1), channels[1].ID
				if tc.scope == "key" {
					wantAlternate, wantFallback, finalChannel = 1, 0, channel.ID
				}
				if recorder.Code != http.StatusOK || failedHits.Load() != 1 || alternateKeyHits.Load() != wantAlternate || fallbackHits.Load() != wantFallback {
					t.Fatalf("status=%d failed=%d alternate=%d fallback=%d body=%s", recorder.Code, failedHits.Load(), alternateKeyHits.Load(), fallbackHits.Load(), recorder.Body.String())
				}
				if balancer.CanAttempt(channel.ID, channel.Keys[0].ID, "other-model") {
					t.Fatal("exhausted credential was not isolated across models")
				}
				if available := balancer.CanAttempt(channel.ID, channel.Keys[1].ID, "other-model"); available != (tc.scope == "key") {
					t.Fatalf("alternate credential availability=%t, scope=%s", available, tc.scope)
				}
				entry := assertHTTPRelaySettlement(t, ctx, true, channel.ID, finalChannel)
				if attempt := entry.Attempts[0]; attempt.FailureClass != string(FailureQuota) || attempt.FailureScope != tc.scope {
					t.Fatalf("quota attempt = %+v", attempt)
				}
				assertHTTPRelayKeysReleased(t, channels)
			})
		}
	}
}
