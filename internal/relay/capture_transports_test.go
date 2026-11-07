package relay

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/bodycache"
	"github.com/suprelory/octopus-plus/internal/relay/capture"
)

func decodedCapture(t *testing.T, m *model.RelayMessage) string {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(m.Data))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWSWriterCapturesDeliveredJSONMessages(t *testing.T) {
	client, server := newTestWSConnPair(t)
	defer client.CloseNow()
	defer server.CloseNow()
	w := NewWSStreamWriter(context.Background(), server)
	w.capture = capture.NewBody(model.RelayMessage{}, nil, 4096)
	input := "data: {\"delta\":\"你好\"}\n\ndata: [DONE]\n\n"
	if _, err := w.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	_, sent, err := client.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w.finishCapture(true)
	m := w.capture.Snapshot()
	if decodedCapture(t, m) != string(sent) || len(m.Events) != 1 || m.Events[0].Type != "ws_text" || m.StatusCode != nil {
		t.Fatalf("bad WS capture: %+v", m)
	}
}

type disconnectedCaptureWriter struct{ gin.ResponseWriter }

func (w disconnectedCaptureWriter) Write([]byte) (int, error) { return 0, errors.New("disconnected") }

func TestServingAttemptRequiresDeliveredBytes(t *testing.T) {
	ctx := setupHTTPRelayTestDB(t)
	for _, disconnected := range []bool{false, true} {
		c, _ := newHTTPRelayTestContext(ctx, "")
		if disconnected {
			c.Writer = disconnectedCaptureWriter{c.Writer}
		}
		trace := startHTTPRelayCapture(c)
		_, _ = c.Writer.Write([]byte(": heartbeat\n\n"))
		if trace.served.Load() != nil {
			t.Fatal("heartbeat assigned a serving attempt")
		}
		attempt := trace.beginAttempt(&model.Channel{ID: 1}, "model", "")
		trace.bindDownstream(attempt)
		_, _ = c.Writer.Write([]byte("payload"))
		if (trace.served.Load() != nil) == disconnected {
			t.Fatal("serving attempt did not reflect accepted bytes")
		}
	}
}

func TestWSUpstreamCapturesErrorBeforeClassification(t *testing.T) {
	client, server := newTestWSConnPair(t)
	defer client.CloseNow()
	defer server.CloseNow()
	r := newWSUpstreamReader(&pooledConn{conn: client}, 1, 1)
	r.capture = capture.NewBody(model.RelayMessage{}, nil, 4096)
	errBody := `{"type":"error","status":429,"error":{"code":"rate_limit_exceeded","message":"slow down"}}`
	if err := server.Write(context.Background(), websocket.MessageText, []byte(errBody)); err != nil {
		t.Fatal(err)
	}
	_, err := r.ReadSourceEvent(context.Background())
	if err == nil {
		t.Fatal("expected upstream error")
	}
	r.capture.Finish(false)
	if got := decodedCapture(t, r.capture.Snapshot()); got != errBody {
		t.Fatal(got)
	}
}

func TestMultipartCaptureOmitsFilesAndMapsModel(t *testing.T) {
	var wire bytes.Buffer
	w := multipart.NewWriter(&wire)
	_ = w.WriteField("model", "alias")
	_ = w.WriteField("prompt", "你好")
	file, _ := w.CreateFormFile("image", "source.png")
	_, _ = file.Write([]byte("private-image-bytes"))
	_ = w.Close()
	cache, err := bodycache.New(io.NopCloser(bytes.NewReader(wire.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	b, err := captureMultipartMetadata(cache, w.Boundary(), "mapped")
	if err != nil || strings.Contains(string(b), "private-image-bytes") {
		t.Fatalf("%s %v", b, err)
	}
	var m struct {
		Parts []capturedFormPart `json:"parts"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Parts) != 3 || m.Parts[0].Value != "mapped" || m.Parts[1].Value != "你好" || m.Parts[2].Bytes != 19 || !m.Parts[2].Omitted {
		t.Fatalf("%+v", m)
	}
}

func TestImageAndCompactCaptureFinalError(t *testing.T) {
	for _, endpoint := range []string{"images", "compact"} {
		t.Run(endpoint, func(t *testing.T) {
			ctx := setupHTTPRelayTestDB(t)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"error":{"message":"bad parameter","type":"invalid_request_error"}}`)
			}))
			defer upstream.Close()
			group := &model.Group{Name: "capture-" + endpoint, Mode: model.GroupModeFailover}
			channels := addHTTPRelayTestChannels(t, ctx, group, model.ChannelPassthroughModeOff, upstream.URL)
			c, recorder := newHTTPRelayTestContext(ctx, `{"model":"capture-`+endpoint+`","prompt":"hi","input":"hello"}`)
			if endpoint == "images" {
				ImagesHandler("/images/generations", c)
			} else {
				HandleResponsesCompact(c)
			}
			entry := assertHTTPRelaySettlement(t, ctx, false, channels[0].ID)
			message, err := op.RelayLogContentGet(ctx, entry.ID, "", "response")
			if err != nil || message.Body == "" || message.Body != recorder.Body.String() {
				t.Fatalf("%+v %v", message, err)
			}
			message, err = op.RelayLogContentGet(ctx, entry.ID, "1", "request")
			if err != nil || !strings.Contains(message.Body, `"model":"model_1"`) {
				t.Fatalf("%+v %v", message, err)
			}
		})
	}
}
