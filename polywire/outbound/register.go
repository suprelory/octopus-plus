package outbound

import (
	"maps"
	"slices"
	"sort"

	"github.com/suprelory/octopus-plus/polywire/config"
	"github.com/suprelory/octopus-plus/polywire/model"
	outAnthropic "github.com/suprelory/octopus-plus/polywire/outbound/anthropic"
	"github.com/suprelory/octopus-plus/polywire/outbound/gemini"
	"github.com/suprelory/octopus-plus/polywire/outbound/openai"
)

type OutboundType int

const (
	OutboundTypeOpenAIChat      OutboundType = 0
	OutboundTypeOpenAIResponse  OutboundType = 1
	OutboundTypeAnthropic       OutboundType = 2
	OutboundTypeGemini          OutboundType = 3
	OutboundTypeOpenAIEmbedding OutboundType = 5
)

const (
	RelayOperationImages             = "images"
	RelayOperationResponsesCompact   = "responses/compact"
	RelayOperationResponsesWebSocket = "responses/websocket"
)

// ProtocolDescriptor is the single registry entry for an outbound protocol.
// It combines wire format, semantic operations, native passthrough support,
// relay-only auxiliary endpoints, transport metadata, and the transformer
// factory so those declarations cannot drift across separate maps.
type ProtocolDescriptor struct {
	Name               string
	APIFormat          model.APIFormat
	RequestTypes       map[model.RequestType]struct{}
	NativeInputFormats map[model.APIFormat]struct{}
	RelayOperations    map[string]struct{}
	Transport          string
	TerminalPolicy     model.StreamTerminalPolicy
	FieldRules         []FieldConversionRule
	Factory            func(config.Config) model.Outbound
}

func (c ProtocolDescriptor) Supports(requestType model.RequestType) bool {
	_, ok := c.RequestTypes[requestType]
	return ok
}

func (c ProtocolDescriptor) SupportsRelayOperation(operation string) bool {
	_, ok := c.RelayOperations[operation]
	return ok
}

var protocolDescriptors = map[OutboundType]ProtocolDescriptor{
	OutboundTypeOpenAIChat: {
		Name:            "openai_chat",
		FieldRules:      protocolFieldRules(OutboundTypeOpenAIChat),
		APIFormat:       model.APIFormatOpenAIChatCompletion,
		RequestTypes:    requestTypes(model.RequestTypeChat, model.RequestTypeResponses),
		RelayOperations: relayOperations(RelayOperationImages),
		Transport:       "http",
		TerminalPolicy: model.StreamTerminalPolicy{
			TerminalEvents:          apiTerminalEvents("[DONE]", "done"),
			RequiredLifecycleEvents: []model.StreamEventKind{model.StreamEventKindMessageStart, model.StreamEventKindMessageStop},
			DefaultFinishReason:     model.FinishReasonStop,
		},
		Factory: func(config.Config) model.Outbound { return &openai.ChatOutbound{} },
	},
	OutboundTypeOpenAIResponse: {
		Name:               "openai_responses",
		FieldRules:         protocolFieldRules(OutboundTypeOpenAIResponse),
		APIFormat:          model.APIFormatOpenAIResponse,
		RequestTypes:       requestTypes(model.RequestTypeChat, model.RequestTypeResponses),
		NativeInputFormats: apiFormats(model.APIFormatOpenAIResponse),
		RelayOperations: relayOperations(
			RelayOperationImages,
			RelayOperationResponsesCompact,
			RelayOperationResponsesWebSocket,
		),
		Transport: "http+websocket",
		TerminalPolicy: model.StreamTerminalPolicy{
			TerminalEvents:          apiTerminalEvents("response.completed", "response.done", "response.incomplete", "response.failed", "response.cancelled", "response.canceled", "response.error", "error", "[DONE]", "done"),
			RequiredLifecycleEvents: []model.StreamEventKind{model.StreamEventKindMessageStart, model.StreamEventKindMessageStop},
			DefaultFinishReason:     model.FinishReasonStop,
		},
		Factory: func(config.Config) model.Outbound { return &openai.ResponseOutbound{} },
	},
	OutboundTypeAnthropic: {
		Name:               "anthropic_messages",
		FieldRules:         protocolFieldRules(OutboundTypeAnthropic),
		APIFormat:          model.APIFormatAnthropicMessage,
		RequestTypes:       requestTypes(model.RequestTypeChat, model.RequestTypeResponses),
		NativeInputFormats: apiFormats(model.APIFormatAnthropicMessage),
		Transport:          "http",
		TerminalPolicy: model.StreamTerminalPolicy{
			TerminalEvents:          apiTerminalEvents("message_stop", "error", "[DONE]", "done"),
			RequiredLifecycleEvents: []model.StreamEventKind{model.StreamEventKindMessageStart, model.StreamEventKindMessageStop},
			DefaultFinishReason:     model.FinishReasonStop,
		},
		Factory: func(cfg config.Config) model.Outbound { return outAnthropic.New(cfg) },
	},
	OutboundTypeGemini: {
		Name:         "gemini_contents",
		FieldRules:   protocolFieldRules(OutboundTypeGemini),
		APIFormat:    model.APIFormatGeminiContents,
		RequestTypes: requestTypes(model.RequestTypeChat, model.RequestTypeResponses),
		Transport:    "http",
		TerminalPolicy: model.StreamTerminalPolicy{
			TerminalEvents:          apiTerminalEvents("[DONE]", "done"),
			RequiredLifecycleEvents: []model.StreamEventKind{model.StreamEventKindMessageStart, model.StreamEventKindMessageStop},
			DefaultFinishReason:     model.FinishReasonStop,
		},
		Factory: func(cfg config.Config) model.Outbound { return gemini.New(cfg) },
	},
	OutboundTypeOpenAIEmbedding: {
		Name:         "openai_embeddings",
		FieldRules:   protocolFieldRules(OutboundTypeOpenAIEmbedding),
		APIFormat:    model.APIFormatOpenAIEmbedding,
		RequestTypes: requestTypes(model.RequestTypeEmbedding),
		Transport:    "http",
		TerminalPolicy: model.StreamTerminalPolicy{
			DefaultFinishReason: model.FinishReasonStop,
		},
		Factory: func(config.Config) model.Outbound { return &openai.EmbeddingOutbound{} },
	},
}

func requestTypes(types ...model.RequestType) map[model.RequestType]struct{} {
	result := make(map[model.RequestType]struct{}, len(types))
	for _, requestType := range types {
		result[requestType] = struct{}{}
	}
	return result
}

func apiFormats(formats ...model.APIFormat) map[model.APIFormat]struct{} {
	result := make(map[model.APIFormat]struct{}, len(formats))
	for _, format := range formats {
		result[format] = struct{}{}
	}
	return result
}

func relayOperations(operations ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(operations))
	for _, operation := range operations {
		result[operation] = struct{}{}
	}
	return result
}

func apiTerminalEvents(events ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(events))
	for _, event := range events {
		result[event] = struct{}{}
	}
	return result
}

// Descriptor returns a snapshot whose maps and slices can be changed without
// modifying the shared protocol registry.
func Descriptor(outboundType OutboundType) (ProtocolDescriptor, bool) {
	descriptor, ok := protocolDescriptors[outboundType]
	descriptor.RequestTypes = maps.Clone(descriptor.RequestTypes)
	descriptor.NativeInputFormats = maps.Clone(descriptor.NativeInputFormats)
	descriptor.RelayOperations = maps.Clone(descriptor.RelayOperations)
	descriptor.TerminalPolicy.TerminalEvents = maps.Clone(descriptor.TerminalPolicy.TerminalEvents)
	descriptor.TerminalPolicy.RequiredLifecycleEvents = slices.Clone(descriptor.TerminalPolicy.RequiredLifecycleEvents)
	descriptor.FieldRules = slices.Clone(descriptor.FieldRules)
	return descriptor, ok
}

func TerminalPolicy(outboundType OutboundType) (model.StreamTerminalPolicy, bool) {
	descriptor, ok := Descriptor(outboundType)
	if !ok {
		return model.DefaultStreamTerminalPolicy(), false
	}
	return descriptor.TerminalPolicy, true
}

func SupportsRequestType(outboundType OutboundType, requestType model.RequestType) bool {
	capability, ok := Descriptor(outboundType)
	return ok && capability.Supports(requestType)
}

func SupportsAPIFormat(outboundType OutboundType, format model.APIFormat) bool {
	capability, ok := Descriptor(outboundType)
	return ok && capability.APIFormat == format
}

func SupportsNativeFormat(outboundType OutboundType, format model.APIFormat) bool {
	capability, ok := Descriptor(outboundType)
	if !ok {
		return false
	}
	_, ok = capability.NativeInputFormats[format]
	return ok
}

func SupportsRelayOperation(outboundType OutboundType, operation string) bool {
	descriptor, ok := Descriptor(outboundType)
	return ok && descriptor.SupportsRelayOperation(operation)
}

func (t OutboundType) String() string {
	switch t {
	case OutboundTypeOpenAIChat:
		return "chat"
	case OutboundTypeOpenAIResponse:
		return "response"
	case OutboundTypeAnthropic:
		return "anthropic"
	case OutboundTypeGemini:
		return "gemini"
	case OutboundTypeOpenAIEmbedding:
		return "embedding"
	default:
		return "unknown"
	}
}

// Get returns a fresh provider adapter with host services disabled.
func Get(outboundType OutboundType) model.Outbound {
	return New(outboundType, config.Config{})
}

// New returns a fresh provider adapter configured with host services.
func New(outboundType OutboundType, cfg config.Config) model.Outbound {
	if descriptor, ok := protocolDescriptors[outboundType]; ok && descriptor.Factory != nil {
		return descriptor.Factory(cfg)
	}
	return nil
}

func Types() []OutboundType {
	types := make([]OutboundType, 0, len(protocolDescriptors))
	for typ := range protocolDescriptors {
		types = append(types, typ)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	return types
}
