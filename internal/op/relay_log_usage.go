package op

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
)

type capturedUsageFields struct {
	Usage              json.RawMessage `json:"usage"`
	UsageMetadata      json.RawMessage `json:"usageMetadata"`
	UsageMetadataSnake json.RawMessage `json:"usage_metadata"`
	TokenUsage         json.RawMessage `json:"token_usage"`
}

type capturedUsageEnvelope struct {
	capturedUsageFields
	Response *capturedUsageFields `json:"response"`
	Message  *capturedUsageFields `json:"message"`
}

// This is an on-demand view of retained provider data, never billing input.
// Keep early observations and the latest one; index limits do not hide final
// usage in a long stream. Oversized individual SSE events are skipped.
func extractRelayRawUsage(body []byte, contentType string) []model.RelayRawUsage {
	var samples []model.RelayRawUsage
	add := func(event int, prefix string, fields *capturedUsageFields) {
		if fields == nil {
			return
		}
		for _, field := range []struct {
			name  string
			value json.RawMessage
		}{
			{"usage", fields.Usage}, {"usageMetadata", fields.UsageMetadata}, {"usage_metadata", fields.UsageMetadataSnake}, {"token_usage", fields.TokenUsage},
		} {
			if len(field.value) == 0 || len(field.value) > 64<<10 || bytes.Equal(field.value, []byte("null")) {
				continue
			}
			sample := model.RelayRawUsage{Event: event, Path: prefix + field.name, Value: field.value}
			if len(samples) < 16 {
				samples = append(samples, sample)
			} else {
				samples[len(samples)-1] = sample
			}
		}
	}
	observe := func(event int, envelope *capturedUsageEnvelope) {
		add(event, "", &envelope.capturedUsageFields)
		add(event, "response.", envelope.Response)
		add(event, "message.", envelope.Message)
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
		decoder := json.NewDecoder(bytes.NewReader(body))
		for event := 1; ; event++ {
			var envelope capturedUsageEnvelope
			if decoder.Decode(&envelope) != nil {
				break
			}
			observe(event, &envelope)
		}
		return samples
	}
	var data []byte
	oversized := false
	event := 1
	flush := func() {
		if len(data) > 0 && !oversized {
			var envelope capturedUsageEnvelope
			if json.Unmarshal(data, &envelope) == nil {
				observe(event, &envelope)
			}
		}
		data = data[:0]
		oversized = false
		event++
	}
	for len(body) > 0 {
		i := bytes.IndexAny(body, "\r\n")
		var line []byte
		if i < 0 {
			line = body
			body = nil
		} else {
			line = body[:i]
			delimiter := body[i]
			body = body[i+1:]
			if delimiter == '\r' && len(body) > 0 && body[0] == '\n' {
				body = body[1:]
			}
		}
		if len(line) == 0 {
			flush()
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) && !oversized {
			payload := bytes.TrimPrefix(line[5:], []byte(" "))
			if len(data)+len(payload)+1 > 1<<20 {
				oversized = true
				data = data[:0]
				continue
			}
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, payload...)
		}
	}
	flush()
	return samples
}
