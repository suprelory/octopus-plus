package middleware

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func newTestRequest(method, path string) *http.Request {
	return httptest.NewRequest(method, path, nil)
}

func TestNormalizeOrigin(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{"https", "https://example.com", "https://example.com", true},
		{"http", "http://example.com", "http://example.com", true},
		{"trailing slash", "https://example.com/", "https://example.com", true},
		{"uppercase", "HTTPS://Example.COM", "https://example.com", true},
		{"default https port", "https://example.com:443", "https://example.com", true},
		{"default http port", "http://example.com:80", "http://example.com", true},
		{"explicit port kept", "https://example.com:8443", "https://example.com:8443", true},
		{"padded", "  https://example.com  ", "https://example.com", true},

		// 缺 scheme 的条目必须被拒绝：否则 "example.com" 会同时放行 http 与 https。
		{"host only", "example.com", "", false},
		{"host and port only", "example.com:8080", "", false},
		{"empty", "", "", false},
		{"unsupported scheme", "ftp://example.com", "", false},
		{"scheme without host", "https://", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeOrigin(tc.input)
			if ok != tc.ok {
				t.Fatalf("normalizeOrigin(%q) ok = %v, want %v", tc.input, ok, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("normalizeOrigin(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestBodyLimitForPath(t *testing.T) {
	const base = int64(32 << 20)
	const importLimit = base * importBodyLimitMultiplier

	cases := []struct {
		path string
		want int64
	}{
		{"/v1/chat/completions", base},
		{"/v1/messages", base},
		{"/api/v1/channel/create", base},
		// images 由 bodycache 自己限流并落盘，不重复包一层。
		{"/v1/images/generations", 0},
		{"/v1/images/edits", 0},
		// 导入端点上传数据库快照，给宽松但有界的上限。
		{"/api/v1/setting/import", importLimit},
		{"/api/v1/site/import/all-api-hub", base},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := bodyLimitForPath(tc.path, base, importLimit); got != tc.want {
				t.Fatalf("bodyLimitForPath(%q) = %d, want %d", tc.path, got, tc.want)
			}
		})
	}
}

func TestCORSAllowsSupportedBrowserSDKHeaders(t *testing.T) {
	for _, header := range []string{
		"OpenAI-Organization",
		"OpenAI-Project",
		"anthropic-dangerous-direct-browser-access",
		"x-stainless-lang",
		"x-stainless-runtime-version",
	} {
		if !slices.ContainsFunc(corsAllowHeaders, func(allowed string) bool {
			return http.CanonicalHeaderKey(allowed) == http.CanonicalHeaderKey(header)
		}) {
			t.Errorf("CORS allow headers missing %q", header)
		}
	}
}

func TestSkipStaticLookup(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		// relay 热路径不该做文件系统查找。
		{"relay chat", "POST", "/v1/chat/completions", true},
		{"relay ws upgrade", "GET", "/v1/responses", true},
		{"api", "GET", "/api/v1/log/list", true},
		{"non-read method", "POST", "/index.html", true},
		{"static asset", "GET", "/_next/static/chunks/abc.js", false},
		{"spa root", "GET", "/", false},
		{"head asset", "HEAD", "/favicon.ico", false},
		// 前缀不能宽到误伤同名开头的静态路径。
		{"apidocs is not api", "GET", "/apidocs", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := newTestRequest(tc.method, tc.path)
			if got := skipStaticLookup(req); got != tc.want {
				t.Fatalf("skipStaticLookup(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
			}
		})
	}
}
