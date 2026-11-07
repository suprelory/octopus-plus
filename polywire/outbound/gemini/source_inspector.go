package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func parseGeminiSource(source model.SourceEvent) (*model.GeminiGenerateContentResponse, error) {
	if parsed, ok := source.Decoded.(*model.GeminiGenerateContentResponse); ok {
		return parsed, nil
	}
	var response model.GeminiGenerateContentResponse
	if err := json.Unmarshal(source.Data, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal gemini stream chunk: %w", err)
	}
	return &response, nil
}

func (o *MessagesOutbound) InspectSourceEvent(_ context.Context, source model.SourceEvent) (model.SourceEvent, model.StreamEventPreview, error) {
	if bytes.Equal(bytes.TrimSpace(source.Data), []byte("[DONE]")) || source.Type == "[DONE]" || strings.EqualFold(strings.TrimSpace(source.Type), "done") {
		return source, model.StreamEventPreview{EventType: "[DONE]", Terminal: true}, nil
	}
	if len(bytes.TrimSpace(source.Data)) == 0 {
		return source, model.StreamEventPreview{EventType: source.Type}, nil
	}
	response, err := parseGeminiSource(source)
	if err != nil {
		return source, model.StreamEventPreview{}, err
	}
	source.Decoded = response
	preview := model.StreamEventPreview{EventType: source.Type, EventID: response.ResponseId}
	for _, candidate := range response.Candidates {
		if candidate == nil {
			continue
		}
		if candidate.Content != nil && len(candidate.Content.Parts) > 0 || candidate.GroundingMetadata != nil || candidate.CitationMetadata != nil {
			preview.Semantic = true
		}
	}
	// A candidate finish reason is not a response-wide terminal marker.
	return source, preview, nil
}
