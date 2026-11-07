package sitesync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

// readSiteResponseBody must reject a body past the cap instead of buffering it.
func TestReadSiteResponseBodyRejectsOversizedBody(t *testing.T) {
	body := strings.NewReader(strings.Repeat("a", siteResponseBodyLimit+1))
	if _, err := readSiteResponseBody(body); err == nil {
		t.Fatal("expected an oversized body to be rejected")
	}
}

func TestReadSiteResponseBodyAcceptsBodyAtLimit(t *testing.T) {
	body := strings.NewReader(strings.Repeat("a", 1024))
	got, err := readSiteResponseBody(body)
	if err != nil {
		t.Fatalf("readSiteResponseBody returned error: %v", err)
	}
	if len(got) != 1024 {
		t.Fatalf("expected 1024 bytes, got %d", len(got))
	}
}

func TestReadSiteResponseBodyHandlesNil(t *testing.T) {
	got, err := readSiteResponseBody(nil)
	if err != nil {
		t.Fatalf("readSiteResponseBody returned error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil body to read as nil, got %d bytes", len(got))
	}
}

// A response larger than the cap surfaces as an error rather than being read
// into memory in full.
func TestRequestJSONRejectsOversizedResponse(t *testing.T) {
	chunk := strings.Repeat("a", 1<<20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for written := 0; written <= siteResponseBodyLimit; written += len(chunk) {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	_, err := requestJSON(context.Background(), &model.Site{BaseURL: server.URL}, http.MethodGet, server.URL, nil, nil)
	if err == nil {
		t.Fatal("expected an oversized response to be rejected")
	}
	if !strings.Contains(err.Error(), "上限") {
		t.Fatalf("expected a size limit error, got %v", err)
	}
}

// requestJSON must impose its own deadline: the caller's context here has none,
// and the shared relay client sets no Timeout.
func TestRequestJSONAppliesPerRequestTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()

	// Shorten the bound for the test rather than blocking for the real timeout.
	original := siteRequestTimeout
	siteRequestTimeout = 150 * time.Millisecond
	t.Cleanup(func() { siteRequestTimeout = original })

	start := time.Now()
	_, err := requestJSON(context.Background(), &model.Site{BaseURL: server.URL}, http.MethodGet, server.URL, nil, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the hung request to time out")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("request was not bounded by the per-request timeout (took %s)", elapsed)
	}
}
