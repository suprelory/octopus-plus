package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
	wire "github.com/suprelory/octopus-plus/polywire/protocol/anthropic"
)

func parseSourceEvent(source model.SourceEvent) (model.SourceEvent, wire.StreamEvent, error) {
	parsed, ok := source.Decoded.(*wire.StreamEvent)
	if !ok {
		parsed = &wire.StreamEvent{}
		var err error
		if len(bytes.TrimSpace(source.Data)) > 0 {
			err = json.Unmarshal(source.Data, parsed)
		}
		if err != nil {
			switch strings.TrimSpace(source.Type) {
			case "message_stop", "ping":
			default:
				return source, wire.StreamEvent{}, fmt.Errorf("failed to unmarshal stream event: %w", err)
			}
		} else {
			source.Decoded = parsed
		}
	}
	event := *parsed
	if source.Type != "" {
		event.Type = strings.TrimSpace(source.Type)
	}
	return source, event, nil
}

func (o *MessageOutbound) InspectSourceEvent(_ context.Context, source model.SourceEvent) (model.SourceEvent, model.StreamEventPreview, error) {
	if bytes.Equal(bytes.TrimSpace(source.Data), []byte("[DONE]")) || source.Type == "[DONE]" || strings.EqualFold(strings.TrimSpace(source.Type), "done") {
		return source, model.StreamEventPreview{EventType: "[DONE]", Terminal: true}, nil
	}
	source, event, err := parseSourceEvent(source)
	if err != nil {
		return source, model.StreamEventPreview{}, err
	}
	preview := model.StreamEventPreview{EventType: event.Type, Terminal: event.Type == "message_stop" || event.Type == "error"}
	switch event.Type {
	case "content_block_start":
		if block := event.ContentBlock; block != nil {
			preview.Semantic = block.Type != "text" && block.Type != "thinking" || block.Text != nil && strings.TrimSpace(*block.Text) != "" || block.Thinking != nil && strings.TrimSpace(*block.Thinking) != "" || len(block.Citations) > 0
		}
	case "content_block_delta":
		if delta := event.Delta; delta != nil {
			preview.Semantic = delta.Text != nil && strings.TrimSpace(*delta.Text) != "" || delta.Thinking != nil && strings.TrimSpace(*delta.Thinking) != "" || delta.Signature != nil && *delta.Signature != "" || delta.PartialJSON != nil && *delta.PartialJSON != "" || delta.Citation != nil
		}
	case "message_start", "message_delta", "message_stop", "content_block_stop", "ping", "error":
	default:
		preview.Semantic = len(source.Data) > 0
	}
	return source, preview, nil
}
