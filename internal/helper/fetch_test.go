package helper

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestFetchModelsUsesBrowserHeadersAndSummarizesHTMLError(t *testing.T) {
	observedUserAgent := ""
	observedAccept := ""
	observedAcceptLanguage := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedUserAgent = r.Header.Get("User-Agent")
		observedAccept = r.Header.Get("Accept")
		observedAcceptLanguage = r.Header.Get("Accept-Language")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="en-US"><head><title>Just a moment...</title></head><body>blocked</body></html>`))
	}))
	defer server.Close()

	_, err := FetchModels(context.Background(), model.Channel{
		Type:     outbound.OutboundTypeOpenAIChat,
		BaseUrls: []model.BaseUrl{{URL: server.URL, Delay: 0}},
		Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "managed-key"}},
	})
	if err == nil {
		t.Fatalf("expected FetchModels to fail")
	}
	if !strings.Contains(err.Error(), "http 403: Just a moment...") {
		t.Fatalf("expected summarized HTML error, got %v", err)
	}
	if !strings.Contains(observedUserAgent, "Mozilla/5.0") {
		t.Fatalf("expected browser user-agent, got %q", observedUserAgent)
	}
	if observedAccept == "" {
		t.Fatalf("expected Accept header to be set")
	}
	if observedAcceptLanguage == "" {
		t.Fatalf("expected Accept-Language header to be set")
	}
}

func TestFetchModelsDistinguishesCloudflareFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "gateway error with CDN branding and no title",
			status: http.StatusBadGateway,
			body:   `<html><body><h1>502 Bad Gateway</h1><footer>Cloudflare Ray ID: abc123</footer></body></html>`,
			want:   "http 502: Bad Gateway",
		},
		{
			name:   "unavailable error with CDN branding",
			status: http.StatusServiceUnavailable,
			body:   `<html><body>Service unavailable. Cloudflare Ray ID: abc123</body></html>`,
			want:   "http 503: Service Unavailable",
		},
		{
			name:   "tunnel error",
			status: http.StatusBadGateway,
			body:   `<html><body>Cloudflare Tunnel error</body></html>`,
			want:   "http 502: Cloudflare Tunnel error",
		},
		{
			name:   "challenge without title",
			status: http.StatusForbidden,
			body:   `<html><body><script src="/cdn-cgi/challenge-platform/test"></script></body></html>`,
			want:   "http 403: Cloudflare challenge",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.Header().Set("Server", "cloudflare")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			_, err := FetchModels(context.Background(), model.Channel{
				Type:     outbound.OutboundTypeOpenAIChat,
				BaseUrls: []model.BaseUrl{{URL: server.URL}},
				Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "test-key"}},
			})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

// hungModelServer serves requests that never respond until the test releases them.
func hungModelServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	return server
}

// The model fetch must impose its own deadline: the context here has none, and
// the shared relay client sets no Timeout, so an unresponsive upstream would
// otherwise hang the caller — site sync runs this once per candidate base URL.
func TestFetchModelsAppliesPerRequestTimeout(t *testing.T) {
	cases := []struct {
		name        string
		channelType outbound.OutboundType
	}{
		{name: "openai", channelType: outbound.OutboundTypeOpenAIChat},
		{name: "anthropic", channelType: outbound.OutboundTypeAnthropic},
		{name: "gemini", channelType: outbound.OutboundTypeGemini},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := hungModelServer(t)

			start := time.Now()
			_, err := fetchModels(context.Background(), model.Channel{
				Type:     testCase.channelType,
				BaseUrls: []model.BaseUrl{{URL: server.URL, Delay: 0}},
				Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "managed-key"}},
			}, 150*time.Millisecond, 5*time.Second)
			elapsed := time.Since(start)

			if err == nil {
				t.Fatal("expected the hung request to time out")
			}
			if elapsed > 5*time.Second {
				t.Fatalf("request was not bounded by the per-request timeout (took %s)", elapsed)
			}
		})
	}
}

// A paginated listing gets the bound per page, so the deadline of an early page
// must not leak into later ones.
func TestFetchModelsBoundsEachPageSeparately(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		w.Header().Set("Content-Type", "application/json")
		if pages == 1 {
			// Spend most of one page's budget, then hand back a next page.
			time.Sleep(200 * time.Millisecond)
			_, _ = w.Write([]byte(`{"data":[{"id":"first"}],"has_more":true,"last_id":"first"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"second"}],"has_more":false}`))
	}))
	defer server.Close()

	models, err := fetchModels(context.Background(), model.Channel{
		Type:     outbound.OutboundTypeAnthropic,
		BaseUrls: []model.BaseUrl{{URL: server.URL, Delay: 0}},
		Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "managed-key"}},
	}, 300*time.Millisecond, 2*time.Second)
	if err != nil {
		t.Fatalf("fetchModels returned error: %v", err)
	}
	if len(models) != 2 || models[0] != "first" || models[1] != "second" {
		t.Fatalf("expected both pages to be fetched, got %v", models)
	}
	if pages != 2 {
		t.Fatalf("expected 2 upstream requests, got %d", pages)
	}
}

func TestFetchModelsRejectsRepeatedGeminiPageToken(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-test"}],"nextPageToken":"repeated-token"}`))
	}))
	defer server.Close()

	models, err := fetchModels(context.Background(), model.Channel{
		Type:     outbound.OutboundTypeGemini,
		BaseUrls: []model.BaseUrl{{URL: server.URL, Delay: 0}},
		Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "managed-key"}},
	}, time.Second, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "repeated page token") {
		t.Fatalf("expected a repeated page token error, got %v", err)
	}
	if models != nil {
		t.Fatalf("expected no partial result, got %v", models)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("expected pagination to stop after 2 requests, got %d", got)
	}
}

func TestFetchModelsRejectsInvalidAnthropicCursor(t *testing.T) {
	testCases := []struct {
		name             string
		response         string
		expectedError    string
		expectedRequests int32
	}{
		{
			name:             "missing last id",
			response:         `{"data":[{"id":"claude-test"}],"has_more":true}`,
			expectedError:    "omitted last_id",
			expectedRequests: 1,
		},
		{
			name:             "repeated last id",
			response:         `{"data":[{"id":"claude-test"}],"has_more":true,"last_id":"repeated-id"}`,
			expectedError:    "repeated last_id",
			expectedRequests: 2,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(testCase.response))
			}))
			defer server.Close()

			models, err := fetchModels(context.Background(), model.Channel{
				Type:     outbound.OutboundTypeAnthropic,
				BaseUrls: []model.BaseUrl{{URL: server.URL, Delay: 0}},
				Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "managed-key"}},
			}, time.Second, 5*time.Second)
			if err == nil || !strings.Contains(err.Error(), testCase.expectedError) {
				t.Fatalf("expected %q error, got %v", testCase.expectedError, err)
			}
			if models != nil {
				t.Fatalf("expected no partial result, got %v", models)
			}
			if got := requests.Load(); got != testCase.expectedRequests {
				t.Fatalf("expected %d requests, got %d", testCase.expectedRequests, got)
			}
		})
	}
}

func TestFetchModelsLimitsPaginationPages(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"models":[{"name":"models/gemini-%d"}],"nextPageToken":"token-%d"}`, page, page)
	}))
	defer server.Close()

	models, err := fetchModels(context.Background(), model.Channel{
		Type:     outbound.OutboundTypeGemini,
		BaseUrls: []model.BaseUrl{{URL: server.URL, Delay: 0}},
		Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "managed-key"}},
	}, time.Second, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "100-page limit") {
		t.Fatalf("expected a page limit error, got %v", err)
	}
	if models != nil {
		t.Fatalf("expected no partial result, got %v", models)
	}
	if got := requests.Load(); got != modelFetchMaxPages {
		t.Fatalf("expected %d requests, got %d", modelFetchMaxPages, got)
	}
}

func TestFetchModelsAppliesOperationTimeoutAcrossPages(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := requests.Add(1)
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":[{"id":"claude-%d"}],"has_more":true,"last_id":"cursor-%d"}`, page, page)
	}))
	defer server.Close()

	start := time.Now()
	models, err := fetchModels(context.Background(), model.Channel{
		Type:     outbound.OutboundTypeAnthropic,
		BaseUrls: []model.BaseUrl{{URL: server.URL, Delay: 0}},
		Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "managed-key"}},
	}, time.Second, 250*time.Millisecond)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the operation deadline to stop pagination, got %v", err)
	}
	if models != nil {
		t.Fatalf("expected no partial result, got %v", models)
	}
	if got := requests.Load(); got < 2 {
		t.Fatalf("expected multiple individually responsive pages before timeout, got %d request(s)", got)
	}
	if elapsed >= time.Second {
		t.Fatalf("operation timeout did not bound pagination (took %s)", elapsed)
	}
}

func TestFetchModelsRejectsUnsupportedChannelBeforeHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	models, err := fetchModels(context.Background(), model.Channel{
		Type:     outbound.OutboundType(-1),
		BaseUrls: []model.BaseUrl{{URL: server.URL}},
		Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "legacy-key"}},
	}, time.Second, time.Second)
	if err == nil || !strings.Contains(err.Error(), "unsupported channel type") {
		t.Fatalf("expected unsupported channel error, got models=%v err=%v", models, err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("expected unsupported channel to fail before HTTP, got %d request(s)", got)
	}
}
