package openai

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/suprelory/octopus-plus/polywire/httpio"
	"github.com/suprelory/octopus-plus/polywire/model"
)

// ResponseOutbound implements the Outbound interface for OpenAI Responses API.
type ResponseOutbound struct {
	// Stream state tracking
	streamID    string
	streamModel string
	initialized bool
	outputItems map[int]ResponsesItem
	// toolCallIndexes maps a Responses output_index to a dense 0-based
	// tool_calls index: output_index counts all output items (reasoning,
	// message, function_call), so function calls may start above 0, while
	// the Chat Completions protocol expects dense 0-based tool_calls indices.
	toolCallIndexes            map[int]int
	toolCallStarted            map[int]bool
	toolCallForwardedArguments map[int]string
}

func (o *ResponseOutbound) TransformRequest(ctx context.Context, request *model.InternalLLMRequest, baseUrl, key string) (*http.Request, error) {
	if err := request.ValidateOperationConsistency(); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, fmt.Errorf("request is nil")
	}
	request = request.Clone()

	request.NormalizeMessages()

	// Convert to Responses API request format
	responsesReq := ConvertToResponsesRequest(request)

	body, err := model.MarshalRequestWithRecovery(request, model.APIFormatOpenAIResponse, responsesReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal responses api request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	applyOpenAIOrgProjectHeaders(req, request)

	if err := o.RetargetRequest(req, model.RequestTargetFrom(request), baseUrl, key); err != nil {
		req.Body.Close()
		return nil, err
	}

	return req, nil
}

func (o *ResponseOutbound) TransformResponse(ctx context.Context, response *http.Response) (*model.InternalLLMResponse, error) {
	if response == nil {
		return nil, fmt.Errorf("response is nil")
	}

	body, err := httpio.ReadResponseBody(response.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("response body is empty")
	}

	// Check for error response
	if response.StatusCode >= 400 {
		var errResp struct {
			Error model.ErrorDetail `json:"error"`
		}
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
			return nil, &model.ResponseError{
				StatusCode: response.StatusCode,
				Detail:     errResp.Error,
			}
		}
		return nil, fmt.Errorf("HTTP error %d: %s", response.StatusCode, string(body))
	}

	var resp ResponsesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal responses api response: %w", err)
	}

	// Convert to internal response
	return convertToLLMResponseFromResponses(&resp), nil
}

// generateResponsesItemID generates a unique ID for Responses API items (function_call, etc.).
// Format matches OpenAI's pattern: item_<random_base62_string>
func generateResponsesItemID() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// fallback: use timestamp + counter
		return fmt.Sprintf("item_%016x%08x", time.Now().UnixNano(), itemIDCounter.Add(1))
	}
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for i := range b {
		b[i] = charset[b[i]%byte(len(charset))]
	}
	return "item_" + string(b)
}

var itemIDCounter atomic.Uint64
