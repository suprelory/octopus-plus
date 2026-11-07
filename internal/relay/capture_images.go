package relay

import (
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/relay/bodycache"
	"github.com/suprelory/octopus-plus/internal/relay/capture"
)

type capturedFormPart struct {
	Name        string `json:"name"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Bytes       int64  `json:"bytes"`
	Value       string `json:"value,omitempty"`
	Omitted     bool   `json:"file_content_omitted,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// A bounded description of the prepared form, not a copy of uploaded files.
// Duplicate fields retain their order; model replacement matches the sender.
func captureMultipartMetadata(bc *bodycache.BodyCache, boundary, mappedModel string) ([]byte, error) {
	source, err := bc.NewReader()
	if err != nil {
		return nil, err
	}
	defer source.Close()
	reader := multipart.NewReader(source, boundary)
	parts := make([]capturedFormPart, 0)
	truncated := false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(parts) >= 128 {
			truncated = true
			break
		}
		item := capturedFormPart{Name: part.FormName(), Filename: part.FileName(), ContentType: part.Header.Get("Content-Type")}
		if item.Filename != "" {
			item.Omitted = true
			item.Bytes, err = io.Copy(io.Discard, part)
		} else {
			var data []byte
			data, err = io.ReadAll(io.LimitReader(part, 16<<10))
			if err == nil {
				var rest int64
				rest, err = io.Copy(io.Discard, part)
				item.Bytes = int64(len(data)) + rest
				item.Truncated = rest > 0
				item.Value = string(data)
			}
			if item.Name == "model" && mappedModel != "" {
				item.Value = mappedModel
				item.Bytes = int64(len(mappedModel))
				item.Truncated = false
			}
		}
		if err != nil {
			return nil, err
		}
		parts = append(parts, item)
	}
	return json.Marshal(struct {
		Parts     []capturedFormPart `json:"parts"`
		Truncated bool               `json:"truncated"`
		Source    string             `json:"source"`
	}{parts, truncated, "prepared_form"})
}

func (t *relayCapture) captureImagesRequest(req *http.Request, bc *bodycache.BodyCache, boundary, mappedModel string, secrets ...string) *capture.Body {
	message := model.RelayMessage{Method: req.Method, URL: capture.URL(req.URL.String()), Headers: capture.Headers(req.Header, secrets...), ContentType: req.Header.Get("Content-Type")}
	if boundary != "" {
		message.Representation = "multipart_metadata"
	}
	body := capture.NewBody(message, t.budget, t.limit)
	if t.limit == 0 {
		body.Finish(true)
		return body
	}
	if boundary != "" {
		data, err := captureMultipartMetadata(bc, boundary, mappedModel)
		if err == nil {
			body.Observe(data)
		}
		body.Finish(err == nil)
		return body
	}
	source, err := bc.NewReader()
	if err == nil {
		_, err = io.Copy(io.Discard, capture.Reader(source, body, bc.Size()))
		_ = source.Close()
	}
	body.Finish(err == nil)
	return body
}
