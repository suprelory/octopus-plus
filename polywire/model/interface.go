package model

import (
	"context"
	"net/http"
	"net/url"
)

type Inbound interface {
	// 入站请求转为内部通用格式
	TransformRequest(ctx context.Context, body []byte) (*InternalLLMRequest, error)

	// 将出站内部通用响应转为入站对应的响应格式
	TransformResponse(ctx context.Context, response *InternalLLMResponse) ([]byte, error)

	// 将出站内部通用流式响应转为入站对应的流式响应格式
	// Deprecated: use TransformStreamEvents. Retained for one release cycle.
	TransformStream(ctx context.Context, stream *InternalLLMResponse) ([]byte, error)

	// TransformStreamEvents converts canonical stream events into the inbound
	// wire format. Relay streaming always uses this contract.
	TransformStreamEvents(ctx context.Context, events []StreamEvent) ([]byte, error)

	// TransformError converts a normalized error into the inbound protocol's
	// HTTP and streaming wire representations.
	TransformError(ctx context.Context, response *ResponseError) (*ProtocolErrorResponse, error)

	// 获取完整的内部响应，用于日志记录、数据统计等
	// 流式场景：将储存的流式响应聚合为完整的响应
	// 非流式场景：返回储存的完整响应
	GetInternalResponse(ctx context.Context) (*InternalLLMResponse, error)
}

type Outbound interface {
	// 将入站内部通用请求转为出站对应的请求格式。request 属于调用方，
	// 实现必须将其视为只读数据，并在 normalize 或 patch 前创建副本。
	TransformRequest(ctx context.Context, request *InternalLLMRequest, baseUrl, key string) (*http.Request, error)

	// 将出站响应转为内部通用响应格式
	TransformResponse(ctx context.Context, response *http.Response) (*InternalLLMResponse, error)

	// 将出站流式转为内部通用流式响应格式
	// Deprecated: use TransformSourceEvent. Retained for one release cycle.
	TransformStream(ctx context.Context, eventData []byte) (*InternalLLMResponse, error)

	// TransformStreamEvent converts provider bytes into canonical stream events.
	// This compatibility method wraps TransformSourceEvent with a data-only
	// source event. Relay must not inspect provider-specific chunks after this
	// boundary.
	TransformStreamEvent(ctx context.Context, eventData []byte) ([]StreamEvent, error)

	// TransformSourceEvent converts a complete provider event into canonical
	// stream events without reconstructing transport metadata from payload JSON.
	TransformSourceEvent(ctx context.Context, event SourceEvent) ([]StreamEvent, error)

	// TransformError converts an upstream protocol error into the normalized
	// error model used by relay failover and the inbound transformer.
	TransformError(ctx context.Context, statusCode int, headers http.Header, body []byte) *ResponseError
}

// RequestTarget contains the transport inputs that are independent of the
// converted body. Query is read-only and preserves nil versus an empty query.
type RequestTarget struct {
	Model     string
	Streaming bool
	Query     url.Values
}

func RequestTargetFrom(request *InternalLLMRequest) RequestTarget {
	return RequestTarget{Model: request.Model, Streaming: request.Stream != nil && *request.Stream, Query: request.Query}
}

// RequestTargeter allows a validated wire body to be reused for another attempt.
// Implementations set only the endpoint and authentication, must not mutate
// target, and must not read or modify adapter stream state.
type RequestTargeter interface {
	RetargetRequest(wire *http.Request, target RequestTarget, baseURL, key string) error
}

// RequestTransformationAction describes a deterministic request change made by
// an outbound adapter while preparing provider wire data.
type RequestTransformationAction string

const (
	RequestTransformationPreserve  RequestTransformationAction = "preserve"
	RequestTransformationTranslate RequestTransformationAction = "translate"
	RequestTransformationDrop      RequestTransformationAction = "drop"
	RequestTransformationTruncate  RequestTransformationAction = "truncate"
	RequestTransformationRepair    RequestTransformationAction = "repair"
	RequestTransformationReject    RequestTransformationAction = "reject"
)

// RequestTransformationChange is adapter-owned evidence of a concrete wire
// transformation. Capability planning consumes these reports so it does not
// need to independently mirror every provider-specific repair or drop rule.
type RequestTransformationChange struct {
	Field       string                      `json:"field"`
	TargetField string                      `json:"target_field,omitempty"`
	Action      RequestTransformationAction `json:"action"`
	Condition   string                      `json:"condition,omitempty"`
	Reason      string                      `json:"reason"`
	Lossless    bool                        `json:"lossless,omitempty"`
	// UnknownTopLevelField identifies opaque request extensions that may be
	// dropped as a routing fallback, independently of known semantic losses.
	UnknownTopLevelField bool `json:"unknown_top_level_field,omitempty"`
	// NativeSemantic identifies provider-native semantics that may be lost
	// when no channel can preserve their source protocol.
	NativeSemantic bool `json:"native_semantic,omitempty"`
}

func (c RequestTransformationChange) IsLossy() bool {
	return c.Action != RequestTransformationPreserve && !c.Lossless
}

func (c RequestTransformationChange) IsUnknownTopLevelFieldDrop() bool {
	return c.UnknownTopLevelField && c.Action == RequestTransformationDrop
}

func (c RequestTransformationChange) IsNativeSemanticLoss() bool {
	return c.NativeSemantic && c.IsLossy() && c.Action != RequestTransformationReject
}

func (c RequestTransformationChange) IsAvailabilityFallbackLoss() bool {
	return c.IsUnknownTopLevelFieldDrop() || c.IsNativeSemanticLoss()
}

// RequestChangeReporter is an optional outbound capability. Implementations
// must treat request as read-only and use the same decision helpers as their
// wire builder.
type RequestChangeReporter interface {
	DescribeRequestChanges(request *InternalLLMRequest, effectiveModel string) []RequestTransformationChange
}

/*
请求流程
非流式

client		-> inbound.TransformRequest(ctx, body)
			-> outbound.TransformRequest(ctx, request)
 			-> http.Do(request)
 			-> outbound.TransformResponse(ctx, response)
			-> inbound.TransformResponse(ctx, response)
															-> client

流式
client		-> inbound.TransformRequest(ctx, body)
        	-> outbound.TransformStream(ctx, chunk)
        	-> http.Do(request)
        	-> outbound.TransformStream(ctx, chunk)
        	-> inbound.TransformStream(ctx, chunk)
															-> client
*/

// PassthroughCapable is an optional interface for Outbound transformers that support
// same-format passthrough (bypassing Internal Model round-trip).
//
// When both inbound and outbound use the same protocol (e.g., Anthropic→Anthropic or
// OpenAI Responses→OpenAI Responses), passthrough preserves request byte-stability
// (critical for prompt caching) and avoids transformation overhead.
type PassthroughCapable interface {
	// CanPassthrough returns true if this outbound can accept raw bytes from the given inbound format.
	// Example: Anthropic MessageOutbound returns true when inboundFormat == APIFormatAnthropicMessage.
	CanPassthrough(inboundFormat APIFormat) bool

	// TransformRequestRaw builds an HTTP request from raw client bytes, rewriting only essential
	// fields (model name, authorization) while preserving request structure.
	//
	// This method maintains byte-level stability for features like Anthropic's prompt caching,
	// where field order and whitespace matter.
	TransformRequestRaw(ctx context.Context, rawBody []byte, model, baseUrl, key string, query url.Values) (*http.Request, error)

	// PassthroughConfig returns passthrough-specific settings for this protocol.
	PassthroughConfig() PassthroughConfig
}

// PassthroughConfig provides protocol-specific settings for passthrough operation.
type PassthroughConfig struct {
	// TerminalEvents defines protocol-specific terminal event types for early completion detection.
	// When a stream contains a terminal event (e.g., "message_stop" for Anthropic, "response.completed"
	// for OpenAI Responses), the relay can treat client disconnection as success rather than failure.
	TerminalEvents map[string]struct{}

	// Deprecated: relay collects metrics through canonical stream events for all
	// protocols. Retained for callers of earlier versions.
	CollectMetrics bool
}

// RequestStateSeedable is an optional capability for Inbound transformers
// that derive internal state from the parsed request. Relay creates a fresh
// inbound adapter per upstream attempt for response-state isolation; instead
// of re-running TransformRequest (JSON parse + protocol conversion + token
// counting) on the unchanged body, the relay seeds the new adapter directly
// from the canonical InternalLLMRequest. Implementations must reproduce
// exactly the adapter state TransformRequest would have produced.
type RequestStateSeedable interface {
	// SeedRequestState initializes request-derived adapter state from the
	// original client request. The request carries the client's original
	// model name, not the channel-mapped one.
	SeedRequestState(request *InternalLLMRequest)
}
