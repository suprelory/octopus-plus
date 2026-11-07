package capture

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestCaptureBoundsStorageWithoutChangingReads(t *testing.T) {
	budget := NewBudget(9)
	for i, want := range []string{"abcdef", "abc"} {
		body := NewBody(model.RelayMessage{}, budget, 6)
		r := Reader(io.NopCloser(strings.NewReader("abcdefghij")), body, 10)
		got, err := io.ReadAll(r)
		if err != nil || string(got) != "abcdefghij" {
			t.Fatalf("transport bytes changed: %q %v", got, err)
		}
		_ = r.Close()
		message := body.Snapshot()
		zr, err := gzip.NewReader(bytes.NewReader(message.Data))
		if err != nil {
			t.Fatal(err)
		}
		stored, err := io.ReadAll(zr)
		_ = zr.Close()
		if err != nil || string(stored) != want || message.State != "truncated" || message.Bytes != 10 {
			t.Fatalf("capture %d: %q %+v %v", i, stored, message, err)
		}
	}
}

func TestPartialAndDisabledCapture(t *testing.T) {
	body := NewBody(model.RelayMessage{}, NewBudget(100), 100)
	r := Reader(io.NopCloser(strings.NewReader("partial response")), body, 16)
	_, _ = r.Read(make([]byte, 3))
	_ = r.Close()
	if got := body.Snapshot(); got.State != "partial" || got.Bytes != 3 {
		t.Fatalf("partial capture: %+v", got)
	}
	disabled := NewBody(model.RelayMessage{}, nil, 0)
	disabled.Observe([]byte("secret"))
	disabled.Finish(true)
	if got := disabled.Snapshot(); got.State != "not_captured" || len(got.Data) != 0 || got.Bytes != 6 {
		t.Fatalf("disabled capture: %+v", got)
	}
}

func TestCredentialRedactionDoesNotMutateTransport(t *testing.T) {
	headers := http.Header{"Authorization": {"Bearer secret"}, "X-Custom": {"credential"}, "Set-Cookie": {"session=secret"}, "X-Trace": {"trace-1", "trace-2"}}
	got := Headers(headers, "credential")
	if got["Authorization"][0] != Redacted || got["X-Custom"][0] != Redacted || got["Set-Cookie"][0] != Redacted || len(got["X-Trace"]) != 2 {
		t.Fatalf("redaction failed: %+v", got)
	}
	if headers.Get("Authorization") != "Bearer secret" || headers.Get("X-Custom") != "credential" {
		t.Fatal("transport headers mutated")
	}
	url := URL("https://name:password@example.com/v1?key=secret&model=gpt&access_token=secret")
	if strings.Contains(url, "secret") || strings.Contains(url, "password") || !strings.Contains(url, "model=gpt") {
		t.Fatalf("URL redaction failed: %s", url)
	}
}
