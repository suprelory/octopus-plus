package sitesync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestRequestJSONUsesBrowserHeaders(t *testing.T) {
	observedUserAgent := ""
	observedAccept := ""
	observedAcceptLanguage := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedUserAgent = r.Header.Get("User-Agent")
		observedAccept = r.Header.Get("Accept")
		observedAcceptLanguage = r.Header.Get("Accept-Language")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	_, err := requestJSON(context.Background(), &model.Site{BaseURL: server.URL}, http.MethodGet, server.URL, nil, nil)
	if err != nil {
		t.Fatalf("requestJSON returned error: %v", err)
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

func TestRequestJSONCustomHeaderOverridesUserAgent(t *testing.T) {
	observedUserAgent := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	_, err := requestJSON(
		context.Background(),
		&model.Site{BaseURL: server.URL, CustomHeader: []model.CustomHeader{{HeaderKey: "User-Agent", HeaderValue: "Octopus-Test-UA"}}},
		http.MethodGet,
		server.URL,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("requestJSON returned error: %v", err)
	}
	if observedUserAgent != "Octopus-Test-UA" {
		t.Fatalf("expected custom user-agent, got %q", observedUserAgent)
	}
}

func TestRequestJSONFormatsHTMLErrorSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="en-US"><head><title>Upstream Error</title></head><body>blocked</body></html>`))
	}))
	defer server.Close()

	_, err := requestJSON(context.Background(), &model.Site{BaseURL: server.URL}, http.MethodGet, server.URL, nil, nil)
	if err == nil {
		t.Fatalf("expected requestJSON to fail")
	}
	if !strings.Contains(err.Error(), "http 502: Upstream Error") {
		t.Fatalf("expected summarized HTML error, got %v", err)
	}
}

func TestRequestJSONDetectsCloudflareAttentionRequired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("CF-Ray", "abc123-LAX")
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>Attention Required! | Cloudflare</title></head><body>Cloudflare Ray ID: abc123</body></html>`))
	}))
	defer server.Close()

	_, err := requestJSON(context.Background(), &model.Site{BaseURL: server.URL}, http.MethodGet, server.URL, nil, nil)
	if err == nil {
		t.Fatalf("expected requestJSON to fail")
	}
	var cfErr *CloudflareProtectionError
	if !errors.As(err, &cfErr) {
		t.Fatalf("expected CloudflareProtectionError, got %T %v", err, err)
	}
	if cfErr.RetryAfter != 60*time.Second {
		t.Fatalf("expected retry-after capped to 60s, got %s", cfErr.RetryAfter)
	}
	if got := apperror.Code(err); got != CodeSiteUpstreamCloudflareChallenge {
		t.Fatalf("expected error code %q, got %q", CodeSiteUpstreamCloudflareChallenge, got)
	}
}

func TestRequestJSONKeepsJSONForbiddenMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("CF-Ray", "abc123-LAX")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"token forbidden"}`))
	}))
	defer server.Close()

	_, err := requestJSON(context.Background(), &model.Site{BaseURL: server.URL}, http.MethodGet, server.URL, nil, nil)
	if err == nil {
		t.Fatalf("expected requestJSON to fail")
	}
	if IsCloudflareProtectionError(err) {
		t.Fatalf("expected JSON business error, got Cloudflare error")
	}
	if err.Error() != "http 403: token forbidden" {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := apperror.Code(err); got != CodeSiteUpstreamHTTPError {
		t.Fatalf("expected error code %q, got %q", CodeSiteUpstreamHTTPError, got)
	}
	if got := apperror.Params(err)["statusCode"]; got != http.StatusForbidden {
		t.Fatalf("expected statusCode param %d, got %#v", http.StatusForbidden, got)
	}
}

func TestSiteHTTPErrorDistinguishesCloudflareFailures(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		mitigated  string
		want       string
		protection bool
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
			name:   "empty gateway error",
			status: http.StatusBadGateway,
			want:   "http 502: Bad Gateway",
		},
		{
			name:   "forbidden page served through Cloudflare",
			status: http.StatusForbidden,
			body:   `<html><head><title>Forbidden</title></head><body>Cloudflare Ray ID: abc123</body></html>`,
			want:   "http 403: Forbidden",
		},
		{
			name:   "JSON business error served through Cloudflare",
			status: http.StatusForbidden,
			body:   `{"message":"token forbidden"}`,
			want:   "http 403: token forbidden",
		},
		{
			name:   "tunnel failure",
			status: http.StatusBadGateway,
			body:   `<html><head><title>Cloudflare Tunnel error | Cloudflare</title></head><body>Error 1033</body></html>`,
			want:   "http 502: Cloudflare Tunnel error (Error 1033)",
		},
		{
			name:       "challenge page",
			status:     http.StatusForbidden,
			body:       `<html><head><title>Just a moment...</title></head><body><script src="/cdn-cgi/challenge-platform/test"></script></body></html>`,
			want:       "http 403: 站点触发 Cloudflare 保护",
			protection: true,
		},
		{
			name:       "explicit challenge header",
			status:     http.StatusForbidden,
			body:       `<html><body>Verification required</body></html>`,
			mitigated:  "challenge",
			want:       "http 403: 站点触发 Cloudflare 保护",
			protection: true,
		},
		{
			name:       "service unavailable challenge",
			status:     http.StatusServiceUnavailable,
			body:       `<html><head><title>Just a moment...</title></head><body>Cloudflare</body></html>`,
			want:       "http 503: 站点触发 Cloudflare 保护",
			protection: true,
		},
	}
	formatters := map[string]func(int, http.Header, []byte) error{
		"standard": formatSiteHTTPError,
		"anyrouter": func(status int, header http.Header, body []byte) error {
			return anyRouterFormatHTTPError(status, header, string(body))
		},
	}
	for formatterName, format := range formatters {
		for _, tc := range cases {
			t.Run(formatterName+"/"+tc.name, func(t *testing.T) {
				header := make(http.Header)
				header.Set("Content-Type", "text/html")
				header.Set("Server", "cloudflare")
				header.Set("CF-Ray", "abc123-LAX")
				header.Set("Cf-Mitigated", tc.mitigated)
				err := format(tc.status, header, []byte(tc.body))
				if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
					t.Fatalf("error = %v, want %q", err, tc.want)
				}
				if got := IsCloudflareProtectionError(err); got != tc.protection {
					t.Fatalf("Cloudflare protection = %v, want %v", got, tc.protection)
				}
				wantCode := CodeSiteUpstreamHTTPError
				if tc.protection {
					wantCode = CodeSiteUpstreamCloudflareChallenge
				}
				if got := apperror.Code(err); got != wantCode {
					t.Fatalf("error code = %q, want %q", got, wantCode)
				}
				if got := siteBatchReason(err) == SiteBatchReasonCloudflareProtection; got != tc.protection {
					t.Fatalf("batch reason = %q, Cloudflare protection should be %v", siteBatchReason(err), tc.protection)
				}
				if !tc.protection && strings.Contains(sanitizeSiteStatusMessage(err), "Cloudflare challenge") {
					t.Fatalf("status message incorrectly describes a challenge: %v", err)
				}
			})
		}
	}
}

func TestNormalizeModelNamesPreservesCaseDistinctVariants(t *testing.T) {
	models := normalizeModelNames([]string{" GPT-5.5 ", "gpt-5.5", "gpt-5.5", ""})

	if len(models) != 2 {
		t.Fatalf("expected case-distinct model names to be preserved, got %+v", models)
	}
	seen := make(map[string]struct{}, len(models))
	for _, item := range models {
		seen[item] = struct{}{}
	}
	if _, ok := seen["GPT-5.5"]; !ok {
		t.Fatalf("expected GPT-5.5 to be preserved, got %+v", models)
	}
	if _, ok := seen["gpt-5.5"]; !ok {
		t.Fatalf("expected gpt-5.5 to be preserved, got %+v", models)
	}
}

func TestParseGroupItemsPreservesScalarMapLabels(t *testing.T) {
	groups := parseGroupItems(map[string]any{
		"data": map[string]any{
			"vip":   "VIP Group",
			"trial": map[string]any{"name": "Trial Group"},
		},
	})

	seen := make(map[string]string)
	for _, group := range groups {
		seen[group.GroupKey] = group.Name
	}
	if seen["vip"] != "VIP Group" {
		t.Fatalf("expected scalar group label, got %+v", groups)
	}
	if seen["trial"] != "Trial Group" {
		t.Fatalf("expected nested group label, got %+v", groups)
	}
}
