package polywire_test

import (
	"context"
	"net/http"
	"testing"

	anthropicInbound "github.com/suprelory/octopus-plus/polywire/inbound/anthropic"
	openaiInbound "github.com/suprelory/octopus-plus/polywire/inbound/openai"
	anthropicOutbound "github.com/suprelory/octopus-plus/polywire/outbound/anthropic"
	geminiOutbound "github.com/suprelory/octopus-plus/polywire/outbound/gemini"
)

func TestCrossProtocolErrorTransformation(t *testing.T) {
	ctx := context.Background()

	t.Run("gemini to anthropic", func(t *testing.T) {
		outbound := &geminiOutbound.MessagesOutbound{}
		normalized := outbound.TransformError(ctx, http.StatusTooManyRequests, http.Header{
			"X-Request-Id": []string{"req-gemini-1"},
		}, []byte(`{"error":{"code":429,"message":"quota exceeded","status":"RESOURCE_EXHAUSTED"}}`))
		inbound := &anthropicInbound.MessagesInbound{}
		response, err := inbound.TransformError(ctx, normalized)
		if err != nil {
			t.Fatalf("TransformError inbound: %v", err)
		}
		assertJSONEqual(t, response.Body, []byte(`{"type":"error","error":{"type":"RESOURCE_EXHAUSTED","message":"quota exceeded"},"request_id":"req-gemini-1"}`))
		if response.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status = %d", response.StatusCode)
		}
	})

	t.Run("anthropic to openai", func(t *testing.T) {
		outbound := &anthropicOutbound.MessageOutbound{}
		normalized := outbound.TransformError(ctx, http.StatusBadRequest, nil, []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad tool schema"},"request_id":"req-anthropic-1"}`))
		inbound := &openaiInbound.ChatInbound{}
		response, err := inbound.TransformError(ctx, normalized)
		if err != nil {
			t.Fatalf("TransformError inbound: %v", err)
		}
		assertJSONEqual(t, response.Body, []byte(`{"error":{"message":"bad tool schema","type":"invalid_request_error","request_id":"req-anthropic-1"}}`))
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d", response.StatusCode)
		}
	})
}

func assertJSONEqual(t *testing.T, actual, expected []byte) {
	t.Helper()
	actual = canonicalJSON(t, actual)
	expected = canonicalJSON(t, expected)
	if string(actual) != string(expected) {
		t.Fatalf("JSON mismatch\nactual:\n%s\nexpected:\n%s", actual, expected)
	}
}
