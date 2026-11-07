package relay

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	openaiOutbound "github.com/suprelory/octopus-plus/polywire/outbound/openai"
)

func validateWSResponseCreatePayload(payload []byte) error {
	var envelope struct {
		Type   string `json:"type"`
		Stream *bool  `json:"stream"`
		Model  string `json:"model"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("invalid websocket response.create payload: %w", err)
	}
	if envelope.Type != "response.create" {
		return fmt.Errorf("websocket payload type must be response.create")
	}
	if envelope.Stream == nil || !*envelope.Stream {
		return fmt.Errorf("websocket response.create payload must use stream=true")
	}
	if strings.TrimSpace(envelope.Model) == "" {
		return fmt.Errorf("websocket response.create payload requires model")
	}
	return nil
}

func parseWSRetryDeadline(now time.Time, retryAfter, retryAt json.RawMessage) time.Time {
	return openaiOutbound.ParseStreamRetryDeadline(now, retryAfter, retryAt)
}
