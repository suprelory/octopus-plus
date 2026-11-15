package openai

import (
	"encoding/json"
	"fmt"

	wire "github.com/suprelory/octopus-plus/polywire/protocol/openai"
)

// Request types

type ResponsesRequest struct {
	Model             string                `json:"model"`
	Instructions      string                `json:"instructions,omitempty"`
	Input             ResponsesInput        `json:"input"`
	Tools             []ResponsesTool       `json:"tools,omitempty"`
	ToolChoice        *ResponsesToolChoice  `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool                 `json:"parallel_tool_calls,omitempty"`
	Stream            *bool                 `json:"stream,omitempty"`
	Text              *ResponsesTextOptions `json:"text,omitempty"`
	Store             *bool                 `json:"store,omitempty"`
	ServiceTier       *string               `json:"service_tier,omitempty"`
	User              *string               `json:"user,omitempty"`
	Metadata          map[string]string     `json:"metadata,omitempty"`
	MaxOutputTokens   *int64                `json:"max_output_tokens,omitempty"`
	Temperature       *float64              `json:"temperature,omitempty"`
	TopP              *float64              `json:"top_p,omitempty"`
	Reasoning         *ResponsesReasoning   `json:"reasoning,omitempty"`
	Include           []string              `json:"include,omitempty"`
	TopLogprobs       *int64                `json:"top_logprobs,omitempty"`
	Truncation        *string               `json:"truncation,omitempty"`

	// Pass-through fields for OpenAI Responses API
	PreviousResponseID   *string         `json:"previous_response_id,omitempty"`
	Background           *bool           `json:"background,omitempty"`
	Prompt               json.RawMessage `json:"prompt,omitempty"`
	PromptCacheKey       *string         `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention *string         `json:"prompt_cache_retention,omitempty"`
	SafetyIdentifier     *string         `json:"safety_identifier,omitempty"`
	MaxToolCalls         *int64          `json:"max_tool_calls,omitempty"`
	Conversation         json.RawMessage `json:"conversation,omitempty"`
	ContextManagement    json.RawMessage `json:"context_management,omitempty"`
	StreamOptions        json.RawMessage `json:"stream_options,omitempty"`
	RawInputItems        json.RawMessage `json:"-"`
	RawTools             json.RawMessage `json:"-"`
}

type ResponsesInput struct {
	Text  *string
	Items []ResponsesItem
}

func (i ResponsesInput) MarshalJSON() ([]byte, error) {
	if i.Text != nil {
		return json.Marshal(i.Text)
	}
	return json.Marshal(i.Items)
}

func (i *ResponsesInput) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		i.Text = &text
		return nil
	}
	var items []ResponsesItem
	if err := json.Unmarshal(data, &items); err == nil {
		i.Items = items
		return nil
	}
	return fmt.Errorf("invalid input format")
}

type ResponsesItem struct {
	ID       string          `json:"id,omitempty"`
	Type     string          `json:"type,omitempty"`
	Role     string          `json:"role,omitempty"`
	Content  *ResponsesInput `json:"content,omitempty"`
	Status   *string         `json:"status,omitempty"`
	Text     *string         `json:"text,omitempty"`
	Refusal  *string         `json:"refusal,omitempty"`
	ImageURL *string         `json:"image_url,omitempty"`
	Detail   *string         `json:"detail,omitempty"`

	// Annotations for output_text content
	Annotations *[]ResponsesAnnotation `json:"annotations,omitempty"`

	// Function call fields
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Arguments string `json:"arguments,omitempty"`

	// Function call output
	Output        *ResponsesInput `json:"output,omitempty"`
	ItemReference *string         `json:"item_reference,omitempty"`

	// Image generation fields
	Result       *string `json:"result,omitempty"`
	Background   *string `json:"background,omitempty"`
	OutputFormat *string `json:"output_format,omitempty"`
	Quality      *string `json:"quality,omitempty"`
	Size         *string `json:"size,omitempty"`

	// Reasoning fields
	Summary          []ResponsesReasoningSummary `json:"summary,omitempty"`
	EncryptedContent *string                     `json:"encrypted_content,omitempty"`

	// Multimodal input fields. OpenAI Responses accepts an `input_file`
	// item as either { file_id } for an uploaded file, { filename,
	// file_data } with an inline base64 (optionally a data URL), or
	// { file_url } for fetch-on-demand. O-H6.
	FileID   *string `json:"file_id,omitempty"`
	Filename *string `json:"filename,omitempty"`
	FileData *string `json:"file_data,omitempty"`
	FileURL  *string `json:"file_url,omitempty"`

	// InputAudio carries the `input_audio` nested object for audio inputs.
	// O-H6.
	InputAudio *ResponsesInputAudio `json:"input_audio,omitempty"`
}

// ResponsesInputAudio mirrors OpenAI's `input_audio` content shape used for
// audio inputs on Responses requests. O-H6.
type ResponsesInputAudio = wire.ResponsesInputAudio

func (item ResponsesItem) isOutputMessageContent() bool {
	if item.Content == nil || len(item.Content.Items) == 0 {
		return false
	}
	for _, ci := range item.Content.Items {
		if ci.Type == "output_text" {
			return true
		}
	}
	return false
}

func (item ResponsesItem) GetContentItems() []ResponsesContentItem {
	if item.Content == nil || len(item.Content.Items) == 0 {
		return nil
	}
	result := make([]ResponsesContentItem, 0, len(item.Content.Items))
	for _, ci := range item.Content.Items {
		text := ""
		if ci.Text != nil {
			text = *ci.Text
		}
		result = append(result, ResponsesContentItem{
			Type: ci.Type,
			Text: text,
		})
	}
	return result
}

type ResponsesContentItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type ResponsesReasoningSummary = wire.ResponsesReasoningSummary

type ResponsesAnnotation struct {
	Raw        json.RawMessage `json:"-"`
	Type       string          `json:"type"`
	StartIndex *int            `json:"start_index,omitempty"`
	EndIndex   *int            `json:"end_index,omitempty"`
	URL        *string         `json:"url,omitempty"`
	Title      *string         `json:"title,omitempty"`
	FileID     *string         `json:"file_id,omitempty"`
	Filename   *string         `json:"filename,omitempty"`
}

type ResponsesTool struct {
	Type              string         `json:"type,omitempty"`
	Name              string         `json:"name,omitempty"`
	Description       string         `json:"description,omitempty"`
	Parameters        map[string]any `json:"parameters,omitempty"`
	Strict            *bool          `json:"strict,omitempty"`
	Background        string         `json:"background,omitempty"`
	OutputFormat      string         `json:"output_format,omitempty"`
	Quality           string         `json:"quality,omitempty"`
	Size              string         `json:"size,omitempty"`
	OutputCompression *int64         `json:"output_compression,omitempty"`
}

type ResponsesToolChoice struct {
	Mode *string `json:"mode,omitempty"`
	Type *string `json:"type,omitempty"`
	Name *string `json:"name,omitempty"`
}

func (t *ResponsesToolChoice) UnmarshalJSON(data []byte) error {
	var mode string
	if err := json.Unmarshal(data, &mode); err == nil {
		t.Mode = &mode
		return nil
	}

	type Alias ResponsesToolChoice
	var alias Alias
	if err := json.Unmarshal(data, &alias); err == nil {
		*t = ResponsesToolChoice(alias)
		return nil
	}

	return fmt.Errorf("invalid tool choice format")
}

type ResponsesTextOptions = wire.ResponsesTextOptions
type ResponsesTextFormat = wire.ResponsesTextFormat
type ResponsesReasoning = wire.ResponsesReasoning

// Response types

type ResponsesResponse struct {
	Object     string          `json:"object"`
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	CreatedAt  int64           `json:"created_at"`
	Output     []ResponsesItem `json:"output"`
	Status     *string         `json:"status,omitempty"`
	Truncation *string         `json:"truncation,omitempty"`
	Usage      *ResponsesUsage `json:"usage,omitempty"`
	Error      *ResponsesError `json:"error,omitempty"`
}

type ResponsesUsage = wire.ResponsesUsage

type ResponsesError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type ResponsesStreamEvent struct {
	Annotation      json.RawMessage       `json:"annotation,omitempty"`
	AnnotationIndex *int                  `json:"annotation_index,omitempty"`
	Type            string                `json:"type"`
	SequenceNumber  int                   `json:"sequence_number"`
	Response        *ResponsesResponse    `json:"response,omitempty"`
	OutputIndex     *int                  `json:"output_index,omitempty"`
	Item            *ResponsesItem        `json:"item,omitempty"`
	ItemID          *string               `json:"item_id,omitempty"`
	ContentIndex    *int                  `json:"content_index,omitempty"`
	Delta           string                `json:"delta,omitempty"`
	Text            string                `json:"text,omitempty"`
	Name            string                `json:"name,omitempty"`
	Namespace       string                `json:"namespace,omitempty"`
	CallID          string                `json:"call_id,omitempty"`
	Arguments       string                `json:"arguments,omitempty"`
	SummaryIndex    *int                  `json:"summary_index,omitempty"`
	Part            *ResponsesContentPart `json:"part,omitempty"`
}

type ResponsesContentPart struct {
	Type        string                `json:"type"`
	Text        *string               `json:"text,omitempty"`
	Annotations []ResponsesAnnotation `json:"annotations,omitempty"`
}
