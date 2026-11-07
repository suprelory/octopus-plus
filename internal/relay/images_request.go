package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/relay/bodycache"
)

func parseJSONModelAndStream(bc *bodycache.BodyCache) (payload map[string]any, modelName string, stream bool, err error) {
	r, err := bc.NewReader()
	if err != nil {
		return nil, "", false, err
	}
	defer r.Close()

	body, err := io.ReadAll(r)
	if err != nil {
		return nil, "", false, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, "", false, errors.New("empty body")
	}

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, "", false, errors.New("invalid json")
	}

	rawModel, ok := m["model"]
	if !ok {
		return nil, "", false, errors.New("model is required")
	}
	modelStr, ok := rawModel.(string)
	if !ok || strings.TrimSpace(modelStr) == "" {
		return nil, "", false, errors.New("model is required")
	}

	stream = false
	if v, ok := m["stream"]; ok {
		switch vv := v.(type) {
		case bool:
			stream = vv
		case string:
			stream = strings.EqualFold(strings.TrimSpace(vv), "true")
		case float64:
			stream = vv != 0
		}
	}

	return m, strings.TrimSpace(modelStr), stream, nil
}

func parseMultipartModelAndStream(bc *bodycache.BodyCache, boundary string) (modelName string, stream bool, err error) {
	r, err := bc.NewReader()
	if err != nil {
		return "", false, err
	}
	defer r.Close()

	mr := multipart.NewReader(r, boundary)

	stream = false
	for {
		part, err := mr.NextPart()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", false, err
		}

		name := part.FormName()
		if name == "" {
			_, _ = io.Copy(io.Discard, part)
			_ = part.Close()
			continue
		}

		switch name {
		case "model":
			b, _ := io.ReadAll(io.LimitReader(part, 1024))
			modelName = strings.TrimSpace(string(b))
		case "stream":
			b, _ := io.ReadAll(io.LimitReader(part, 16))
			stream = strings.EqualFold(strings.TrimSpace(string(b)), "true")
		default:
			_, _ = io.Copy(io.Discard, part)
		}
		_ = part.Close()
	}

	if strings.TrimSpace(modelName) == "" {
		return "", false, errors.New("model is required")
	}
	return modelName, stream, nil
}

func copyHeadersToUpstream(req *http.Request, c *gin.Context, channel *model.Channel, channelKey string, contentType string, stream bool) {
	for k, values := range c.Request.Header {
		if hopByHopHeaders[strings.ToLower(k)] {
			continue
		}
		for _, v := range values {
			req.Header.Add(k, v)
		}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+channelKey)

	// 防止 Go 默认 User-Agent 泄露到上游
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "")
	}

	if len(channel.CustomHeader) > 0 {
		for _, h := range channel.CustomHeader {
			req.Header.Set(h.HeaderKey, h.HeaderValue)
		}
	}
}

func copyMultipartReplaceModel(src io.Reader, boundary string, dst *multipart.Writer, newModel string) error {
	mr := multipart.NewReader(src, boundary)

	for {
		part, err := mr.NextPart()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}

		hdr := make(textproto.MIMEHeader, len(part.Header))
		for k, vv := range part.Header {
			cp := make([]string, len(vv))
			copy(cp, vv)
			hdr[k] = cp
		}

		pw, err := dst.CreatePart(hdr)
		if err != nil {
			_ = part.Close()
			return err
		}

		if part.FormName() == "model" && part.FileName() == "" {
			// 丢弃原值，写入替换后的 model（继续复制后续 part）
			_, _ = io.Copy(io.Discard, part)
			_, werr := io.WriteString(pw, newModel)
			_ = part.Close()
			if werr != nil {
				return werr
			}
			continue
		}

		_, err = io.Copy(pw, part)
		_ = part.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
