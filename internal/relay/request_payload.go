package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/suprelory/octopus-plus/internal/helper"
	"github.com/suprelory/octopus-plus/polywire/httpio"
)

func (ra *relayAttempt) clientRequestHeaders() http.Header {
	if ra == nil || ra.c == nil || ra.c.Request == nil {
		return nil
	}
	return ra.c.Request.Header
}

func readOutboundRequestBody(req *http.Request) ([]byte, error) {
	if req == nil || req.Body == nil {
		return nil, nil
	}
	// 出站 body 由入站 body 转换而来，本身已被 middleware.MaxBodySize 限住，
	// 这里用同一个上限兜底，避免 transformer 里的意外膨胀。
	if req.GetBody != nil {
		bodyReader, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer bodyReader.Close()
		return httpio.ReadResponseBody(bodyReader)
	}
	body, err := httpio.ReadResponseBody(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	return body, nil
}

// applyParamOverride merges channel-level JSON request overrides and records the final upstream payload.
func (ra *relayAttempt) applyParamOverride(outboundRequest *http.Request) error {
	originalBody, err := readOutboundRequestBody(outboundRequest)
	if err != nil {
		return classifyLocalRelayError(FailureConfiguration, fmt.Errorf("failed to inspect original outbound request body: %w", err))
	}
	requestBody, captured, err := helper.ApplyParamOverrideWithPayload(outboundRequest, ra.channel.ParamOverride)
	if err != nil {
		return classifyLocalRelayError(FailureConfiguration, fmt.Errorf("invalid channel param override: %w", err))
	}
	if !captured {
		requestBody, err = readOutboundRequestBody(outboundRequest)
		if err != nil {
			return classifyLocalRelayError(FailureConfiguration, fmt.Errorf("failed to inspect outbound request body: %w", err))
		}
	}
	return ra.recordTransportRequestPayloadWithModelRequirement(requestBody, jsonPayloadHasTopLevelModel(originalBody))
}

// applyParamOverridePayload is the transport-neutral counterpart used by
// Responses WebSocket requests. It applies the exact same channel policy as
// the HTTP path and records the final bytes that will be sent upstream.
func (ra *relayAttempt) applyParamOverridePayload(payload []byte) ([]byte, error) {
	modified, _, err := helper.ApplyParamOverridePayload(payload, ra.channel.ParamOverride)
	if err != nil {
		return nil, classifyLocalRelayError(FailureConfiguration, fmt.Errorf("invalid channel param override: %w", err))
	}
	if err := ra.recordTransportRequestPayload(modified); err != nil {
		return nil, err
	}
	return modified, nil
}

func (ra *relayAttempt) recordTransportRequestPayload(payload []byte) error {
	return ra.recordTransportRequestPayloadWithModelRequirement(payload, false)
}

func (ra *relayAttempt) recordTransportRequestPayloadWithModelRequirement(payload []byte, requireModel bool) error {
	inspection := helper.InspectParamOverride(ra.channel.ParamOverride)
	if inspection.Valid {
		// A valid override may be configured on a transport with a non-JSON body
		// (multipart images, audio, or a provider-specific binary payload). The
		// helper deliberately leaves those bytes untouched.
		if json.Valid(payload) {
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(payload, &envelope); err == nil && envelope != nil {
				if rawModel, ok := envelope["model"]; ok {
					var finalModel string
					if err := json.Unmarshal(rawModel, &finalModel); err != nil || strings.TrimSpace(finalModel) == "" {
						return classifyLocalRelayError(FailureConfiguration, fmt.Errorf("channel param override produced an invalid model"))
					}
					if ra.internalRequest != nil {
						ra.internalRequest.Model = strings.TrimSpace(finalModel)
					}
				} else if requireModel || containsString(inspection.Paths, "/model") {
					return classifyLocalRelayError(FailureConfiguration, fmt.Errorf("channel param override removed the required model"))
				}
			} else if requireModel {
				return classifyLocalRelayError(FailureConfiguration, fmt.Errorf("channel param override produced a non-object request"))
			}
		} else if requireModel {
			return classifyLocalRelayError(FailureConfiguration, fmt.Errorf("channel param override produced invalid JSON"))
		}
	}
	if ra.metrics != nil {
		modelName := ""
		if ra.internalRequest != nil {
			modelName = ra.internalRequest.Model
		}
		ra.metrics.SetTransportRequestPayload(payload, modelName)
	}
	return ra.checkRequestCost(payload)
}

// copyHeaders 复制请求头，过滤 hop-by-hop 头
func (ra *relayAttempt) copyHeaders(outboundRequest *http.Request) {
	if ra.c != nil {
		for key, values := range ra.c.Request.Header {
			lowerKey := strings.ToLower(key)
			if hopByHopHeaders[lowerKey] {
				continue
			}
			// anthropic-beta 需要与出站默认值合并去重，避免覆盖掉
			// 透传路径预置的 prompt-caching / extended-cache-ttl 基线。
			if lowerKey == "anthropic-beta" {
				existing := outboundRequest.Header.Get(key)
				for _, value := range values {
					existing = mergeBetaHeader(existing, value)
				}
				if existing != "" {
					outboundRequest.Header.Set(key, existing)
				}
				continue
			}
			for _, value := range values {
				outboundRequest.Header.Set(key, value)
			}
		}
	}
	if outboundRequest.Header.Get("User-Agent") == "" {
		outboundRequest.Header.Set("User-Agent", "")
	}
	if len(ra.channel.CustomHeader) > 0 {
		for _, header := range ra.channel.CustomHeader {
			outboundRequest.Header.Set(header.HeaderKey, header.HeaderValue)
		}
	}
}

// mergeBetaHeader 合并两个逗号分隔的 anthropic-beta 字段值，去重并保留先后顺序。
func mergeBetaHeader(existing, incoming string) string {
	seen := make(map[string]struct{}, 8)
	merged := make([]string, 0, 8)
	for _, source := range []string{existing, incoming} {
		for _, entry := range strings.Split(source, ",") {
			normalized := strings.TrimSpace(entry)
			if normalized == "" {
				continue
			}
			if _, ok := seen[normalized]; ok {
				continue
			}
			seen[normalized] = struct{}{}
			merged = append(merged, normalized)
		}
	}
	return strings.Join(merged, ",")
}
