package anthropic

import (
	"github.com/suprelory/octopus-plus/polywire/model"
	wire "github.com/suprelory/octopus-plus/polywire/protocol/anthropic"
)

// Wire DTOs live in protocol/anthropic so inbound and outbound adapters depend
// on a neutral protocol package. These aliases preserve the existing public
// surface for callers that imported the former inbound-owned DTOs.
type MessageRequest = wire.MessageRequest
type AnthropicMetadata = wire.AnthropicMetadata
type SystemPrompt = wire.SystemPrompt
type SystemPromptPart = wire.SystemPromptPart
type Thinking = wire.Thinking
type OutputConfig = wire.OutputConfig
type ToolChoice = wire.ToolChoice
type Tool = wire.Tool
type CacheControl = wire.CacheControl
type InputSchema = wire.InputSchema
type MessageParam = wire.MessageParam
type MessageContent = wire.MessageContent
type MessageContentBlock = wire.MessageContentBlock
type Citation = wire.Citation
type DocumentCitationsControl = wire.DocumentCitationsControl
type ImageSource = wire.ImageSource
type StreamEvent = wire.StreamEvent
type StreamDelta = wire.StreamDelta
type StreamMessage = wire.StreamMessage
type Message = wire.Message
type ErrorDetail = wire.ErrorDetail
type AnthropicError = wire.AnthropicError
type Usage = wire.Usage
type CacheCreationUsage = wire.CacheCreationUsage

type ProviderExtensions = model.ProviderExtensions
type GeminiExtension = model.GeminiExtension

const (
	ThinkingTypeEnabled  = wire.ThinkingTypeEnabled
	ThinkingTypeDisabled = wire.ThinkingTypeDisabled
	ThinkingTypeAdaptive = wire.ThinkingTypeAdaptive

	EffortMax    = wire.EffortMax
	EffortXHigh  = wire.EffortXHigh
	EffortHigh   = wire.EffortHigh
	EffortMedium = wire.EffortMedium
	EffortLow    = wire.EffortLow

	ThinkingDisplaySummarized = wire.ThinkingDisplaySummarized
	ThinkingDisplayOmitted    = wire.ThinkingDisplayOmitted
)
