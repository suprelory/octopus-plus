// Package openai contains wire types shared by OpenAI protocol adapters.
package openai

import "encoding/json"

type ResponsesInputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format,omitempty"`
}

type ResponsesReasoningSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ResponsesTextOptions struct {
	Format    *ResponsesTextFormat `json:"format,omitempty"`
	Verbosity *string              `json:"verbosity,omitempty"`
}

type ResponsesTextFormat struct {
	Strict      *bool           `json:"strict,omitempty"`
	Description string          `json:"description,omitempty"`
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

type ResponsesReasoning struct {
	Effort          string  `json:"effort,omitempty"`
	MaxTokens       *int64  `json:"max_tokens,omitempty"`
	Summary         *string `json:"summary,omitempty"`
	GenerateSummary *string `json:"generate_summary,omitempty"`
}

type ResponsesUsage struct {
	InputTokens       int64 `json:"input_tokens"`
	InputTokenDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens       int64 `json:"output_tokens"`
	OutputTokenDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
	TotalTokens int64 `json:"total_tokens"`
}
