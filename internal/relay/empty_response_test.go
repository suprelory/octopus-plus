package relay

import (
	"context"
	"errors"
	"testing"

	"github.com/suprelory/octopus-plus/internal/relay/stream"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestHasNonStreamContent(t *testing.T) {
	tests := []struct {
		name     string
		response *model.InternalLLMResponse
		want     bool
	}{
		{
			name:     "nil response",
			response: nil,
			want:     false,
		},
		{
			name: "empty choices",
			response: &model.InternalLLMResponse{
				ID:      "test-id",
				Object:  "chat.completion",
				Choices: []model.Choice{},
			},
			want: false,
		},
		{
			name: "choice with empty message",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "chat.completion",
				Choices: []model.Choice{
					{
						Index:   0,
						Message: &model.Message{Role: "assistant"},
					},
				},
			},
			want: false,
		},
		{
			name: "choice with empty content string",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "chat.completion",
				Choices: []model.Choice{
					{
						Index: 0,
						Message: &model.Message{
							Role:    "assistant",
							Content: model.MessageContent{Content: stringPtr("")},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "choice with whitespace-only content",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "chat.completion",
				Choices: []model.Choice{
					{
						Index: 0,
						Message: &model.Message{
							Role:    "assistant",
							Content: model.MessageContent{Content: stringPtr("   \n\t  ")},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "choice with valid text content",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "chat.completion",
				Choices: []model.Choice{
					{
						Index: 0,
						Message: &model.Message{
							Role:    "assistant",
							Content: model.MessageContent{Content: stringPtr("Hello, world!")},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "choice with tool calls",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "chat.completion",
				Choices: []model.Choice{
					{
						Index: 0,
						Message: &model.Message{
							Role: "assistant",
							ToolCalls: []model.ToolCall{
								{
									ID:   "call_123",
									Type: "function",
									Function: model.FunctionCall{
										Name:      "get_weather",
										Arguments: `{"location": "Boston"}`,
									},
								},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "choice with reasoning content",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "chat.completion",
				Choices: []model.Choice{
					{
						Index: 0,
						Message: &model.Message{
							Role:             "assistant",
							ReasoningContent: stringPtr("Let me think about this..."),
						},
					},
				},
			},
			want: true,
		},
		{
			name: "choice with reasoning (alternative field)",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "chat.completion",
				Choices: []model.Choice{
					{
						Index: 0,
						Message: &model.Message{
							Role:      "assistant",
							Reasoning: stringPtr("Analyzing the problem..."),
						},
					},
				},
			},
			want: true,
		},
		{
			name: "choice with reasoning blocks",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "chat.completion",
				Choices: []model.Choice{
					{
						Index: 0,
						Message: &model.Message{
							Role: "assistant",
							ReasoningBlocks: []model.ReasoningBlock{
								{Kind: model.ReasoningBlockKindThinking, Text: "thinking..."},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "choice with refusal",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "chat.completion",
				Choices: []model.Choice{
					{
						Index: 0,
						Message: &model.Message{
							Role:    "assistant",
							Refusal: "I cannot help with that request.",
						},
					},
				},
			},
			want: true,
		},
		{
			name: "choice with audio",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "chat.completion",
				Choices: []model.Choice{
					{
						Index: 0,
						Message: &model.Message{
							Role: "assistant",
							Audio: &struct {
								Data       string `json:"data,omitempty"`
								ExpiresAt  int64  `json:"expires_at,omitempty"`
								ID         string `json:"id,omitempty"`
								Transcript string `json:"transcript,omitempty"`
							}{
								ID:   "audio-123",
								Data: "base64data",
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "choice with images",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "chat.completion",
				Choices: []model.Choice{
					{
						Index: 0,
						Message: &model.Message{
							Role: "assistant",
							Images: []model.MessageContentPart{
								{Type: "image_url"},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "choice with multiple content parts",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "chat.completion",
				Choices: []model.Choice{
					{
						Index: 0,
						Message: &model.Message{
							Role: "assistant",
							Content: model.MessageContent{
								MultipleContent: []model.MessageContentPart{
									{Type: "text", Text: stringPtr("Hello")},
								},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "embedding response",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "list",
				EmbeddingData: []model.EmbeddingObject{
					{
						Object:    "embedding",
						Index:     0,
						Embedding: model.Embedding{FloatArray: []float64{0.1, 0.2, 0.3}},
					},
				},
			},
			want: true,
		},
		{
			name: "error response is considered meaningful",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "error",
				Error: &model.ResponseError{
					StatusCode: 500,
					Detail: model.ErrorDetail{
						Message: "API error",
						Type:    "api_error",
						Code:    "internal_error",
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasNonStreamContent(tt.response); got != tt.want {
				t.Errorf("hasNonStreamContent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateNonStreamResponse(t *testing.T) {
	tests := []struct {
		name     string
		response *model.InternalLLMResponse
		wantErr  bool
	}{
		{
			name: "valid response with content",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "chat.completion",
				Choices: []model.Choice{
					{
						Index: 0,
						Message: &model.Message{
							Role:    "assistant",
							Content: model.MessageContent{Content: stringPtr("Hello")},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "empty response triggers error",
			response: &model.InternalLLMResponse{
				ID:      "test-id",
				Object:  "chat.completion",
				Choices: []model.Choice{},
			},
			wantErr: true,
		},
		{
			name: "empty content triggers error",
			response: &model.InternalLLMResponse{
				ID:     "test-id",
				Object: "chat.completion",
				Choices: []model.Choice{
					{
						Index: 0,
						Message: &model.Message{
							Role:    "assistant",
							Content: model.MessageContent{Content: stringPtr("")},
						},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNonStreamResponse(tt.response)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateNonStreamResponse() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && err != stream.ErrEmptyUpstreamStream {
				t.Errorf("validateNonStreamResponse() error = %v, want %v", err, stream.ErrEmptyUpstreamStream)
			}
		})
	}
}

func TestHasMessageContentIgnoresEmptyBlocks(t *testing.T) {
	for _, test := range []struct {
		name    string
		message model.Message
		want    bool
	}{
		{"empty text block", model.Message{Content: model.MessageContent{MultipleContent: []model.MessageContentPart{{Type: "text", Text: stringPtr("")}}}}, false},
		{"missing text", model.Message{Content: model.MessageContent{MultipleContent: []model.MessageContentPart{{Type: "text"}}}}, false},
		{"whitespace text blocks", model.Message{Content: model.MessageContent{MultipleContent: []model.MessageContentPart{{Type: "text", Text: stringPtr(" \n\t\u3000")}, {Type: "text", Text: stringPtr("")}}}}, false},
		{"later text content", model.Message{Content: model.MessageContent{MultipleContent: []model.MessageContentPart{{Type: "text", Text: stringPtr("")}, {Type: "text", Text: stringPtr("answer")}}}}, true},
		{"citation content", model.Message{Content: model.MessageContent{MultipleContent: []model.MessageContentPart{{Type: "text", Citations: []model.Citation{{}}}}}}, true},
		{"empty thinking block", model.Message{ReasoningBlocks: []model.ReasoningBlock{{Kind: model.ReasoningBlockKindThinking, Text: " \n\t"}}}, false},
		{"signed thinking", model.Message{ReasoningBlocks: []model.ReasoningBlock{{Kind: model.ReasoningBlockKindThinking, Signature: "opaque-signature", Provider: "anthropic"}}}, true},
		{"redacted thinking", model.Message{ReasoningBlocks: []model.ReasoningBlock{{Kind: model.ReasoningBlockKindRedacted, Data: "opaque-data"}}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := hasMessageContent(&test.message); got != test.want {
				t.Fatalf("hasMessageContent() = %t, want %t", got, test.want)
			}
		})
	}
}

// The group-level switch must only suppress the empty-response verdict. The
// precommit buffer itself always runs, so failover on a late upstream error
// stays possible whether or not detection is enabled.
func TestAllowEmptyPayloadTracksGroupSwitch(t *testing.T) {
	enabled := &relayAttempt{emptyResponseDetection: true}
	if enabled.allowEmptyPayload() {
		t.Error("allowEmptyPayload() = true with detection enabled, want false")
	}

	disabled := &relayAttempt{emptyResponseDetection: false}
	if !disabled.allowEmptyPayload() {
		t.Error("allowEmptyPayload() = false with detection disabled, want true")
	}
}

// With detection disabled a metadata-only stream must still fail when nothing at
// all reached the client: forwarding zero bytes is broken regardless of the switch.
func TestEmptyStreamStillFailsWithDetectionDisabled(t *testing.T) {
	ra, _ := newEmptyStreamTestAttempt(t, inbound.InboundTypeOpenAIChat, model.APIFormatOpenAIChatCompletion, outbound.OutboundTypeOpenAIResponse)
	ra.emptyResponseDetection = false

	err := ra.handleStreamResponseV2(context.Background(), sseTestResponse(""))
	if !errors.Is(err, stream.ErrEmptyUpstreamStream) {
		t.Fatalf("expected stream.ErrEmptyUpstreamStream even with detection disabled, got %v", err)
	}
}
