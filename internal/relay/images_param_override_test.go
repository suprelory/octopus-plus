package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/relay/bodycache"
)

func TestImagesAttemptAppliesJSONOverrideAndTracksFinalModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()

	override := `{"model":"override-image-model","quality":"hd"}`
	channel := &dbmodel.Channel{
		BaseUrls:      []dbmodel.BaseUrl{{URL: server.URL}},
		ParamOverride: &override,
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	metrics := newImagesRelayMetrics(0, "alias", "")

	status, written, _, _, err := imagesAttempt(
		context.Background(), "/images/generations", c, nil, false, "",
		map[string]any{"model": "alias", "prompt": "hello"}, false,
		channel, "key", 0, metrics, "mapped-image-model", nil, new(time.Time),
	)
	if err != nil || status != http.StatusOK || !written {
		t.Fatalf("imagesAttempt() status=%d written=%t err=%v", status, written, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("decode upstream payload: %v", err)
	}
	if payload["model"] != "override-image-model" || payload["quality"] != "hd" {
		t.Fatalf("upstream payload = %#v", payload)
	}
	if metrics.ActualModel != "override-image-model" {
		t.Fatalf("actual model = %q, want override-image-model", metrics.ActualModel)
	}
}

func TestImagesAttemptPreservesMultipartWhenJSONOverrideConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var upstreamModel string
	var upstreamQuality string
	var upstreamFile []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err == nil && mediaType == "multipart/form-data" {
			reader := multipart.NewReader(r.Body, params["boundary"])
			for {
				part, nextErr := reader.NextPart()
				if nextErr != nil {
					break
				}
				value, _ := io.ReadAll(part)
				switch part.FormName() {
				case "model":
					upstreamModel = string(value)
				case "quality":
					upstreamQuality = string(value)
				case "image":
					upstreamFile = append([]byte(nil), value...)
				}
				_ = part.Close()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()

	var original bytes.Buffer
	writer := multipart.NewWriter(&original)
	boundary := writer.Boundary()
	_ = writer.WriteField("model", "alias")
	file, err := writer.CreateFormFile("image", "input.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("image-bytes"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	cache, err := bodycache.New(io.NopCloser(bytes.NewReader(original.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	override := `{"quality":"hd"}`
	channel := &dbmodel.Channel{
		BaseUrls:      []dbmodel.BaseUrl{{URL: server.URL}},
		ParamOverride: &override,
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(""))
	metrics := newImagesRelayMetrics(0, "alias", "")

	status, written, _, _, err := imagesAttempt(
		context.Background(), "/images/edits", c, cache, true, boundary, nil, false,
		channel, "key", 0, metrics, "mapped-image-model", nil, new(time.Time),
	)
	if err != nil || status != http.StatusOK || !written {
		t.Fatalf("imagesAttempt() status=%d written=%t err=%v", status, written, err)
	}
	if upstreamModel != "mapped-image-model" {
		t.Fatalf("multipart model = %q, want mapped-image-model", upstreamModel)
	}
	if upstreamQuality != "" {
		t.Fatalf("JSON override must not be injected into multipart payload, got quality=%q", upstreamQuality)
	}
	if string(upstreamFile) != "image-bytes" {
		t.Fatalf("multipart file = %q, want original bytes", upstreamFile)
	}
	if metrics.ActualModel != "mapped-image-model" {
		t.Fatalf("actual model = %q, want mapped-image-model", metrics.ActualModel)
	}
}
