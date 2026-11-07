package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/suprelory/octopus-plus/polywire/config"
	"github.com/suprelory/octopus-plus/polywire/httpio"
	"github.com/suprelory/octopus-plus/polywire/model"
)

type MessagesOutbound struct {
	config config.Config
	// streamReasoningIndex tracks the global (cross-chunk) emission order of
	// reasoning blocks per candidate. Gemini 3 interleaves thought /
	// signature parts across many SSE chunks; the inbound aggregator needs a
	// monotonically-increasing Index to bind signatures to the correct
	// thinking block. See G-C4.
	streamReasoningIndex map[int]int
	streamToolCallIndex  int
}

// New creates an adapter for one request attempt or stream.
func New(cfg config.Config) *MessagesOutbound { return &MessagesOutbound{config: cfg} }

func (o *MessagesOutbound) TransformResponse(ctx context.Context, response *http.Response) (*model.InternalLLMResponse, error) {
	body, err := httpio.ReadResponseBody(response.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("response body is empty")
	}

	var geminiResp model.GeminiGenerateContentResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal gemini response: %w", err)
	}

	// Convert Gemini response to internal format
	return o.convertGeminiToLLMResponse(&geminiResp, false, nil), nil
}
