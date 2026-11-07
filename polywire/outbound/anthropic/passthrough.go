package anthropic

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/rawjson"
)

// DefaultAnthropicPassthroughBeta 是 Anthropic→Anthropic 直通路径在未从客户端收到
// 显式 anthropic-beta 时写入的默认基线；同时作为 copyHeaders 合并客户端值时的基线。
// 取这两个主要是为了让扩展缓存 TTL（1h）以及新版缓存作用域稳定生效。
const DefaultAnthropicPassthroughBeta = "prompt-caching-2024-07-31,extended-cache-ttl-2025-04-11"

// TransformRequestRaw 把客户端原始 Anthropic 请求字节直接转发给上游，仅重写顶层 model 为
// 当前命中的实际上游模型，不做其他字段白名单解析。
// 用于 Anthropic → Anthropic 的同协议直通路径，保证 anthropic-beta 相关字段（context_management、
// betas 等）、内容块原始顺序、extended thinking 签名等信息尽量完整传递到上游。
//
// 仅设置上游必需的鉴权/URL；Accept、Content-Type、Anthropic-Version、anthropic-beta 等请求头由
// 上层 copyHeaders 从客户端透传（已被 hop-by-hop 过滤保护，x-api-key/authorization 不会覆盖）。
// 注意：为了 HTTP/2 与 401/429/5xx 重试时可以重放 body，同时设置 ContentLength 与 GetBody。
func (o *MessageOutbound) TransformRequestRaw(ctx context.Context, rawBody []byte, modelName, baseUrl, key string, query url.Values) (*http.Request, error) {
	if len(rawBody) == 0 {
		return nil, fmt.Errorf("raw body is empty")
	}
	if strings.TrimSpace(modelName) != "" {
		rewrittenBody, err := rewriteRawRequestModel(rawBody, modelName)
		if err != nil {
			return nil, err
		}
		rawBody = rewrittenBody
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "", bytes.NewReader(rawBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.ContentLength = int64(len(rawBody))
	bodyBytes := rawBody
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bodyBytes)), nil
	}

	// 默认请求头：上层 copyHeaders 随后会用客户端真实值覆盖 Content-Type / Accept /
	// Anthropic-Version；anthropic-beta 在 copyHeaders 里会与此默认值做合并去重，
	// 确保即使客户端未显式声明也能触发 prompt-caching / extended-cache-ttl 等缓存
	// 相关 beta（参考 metapi headerUtils.mergeClaudeBetaHeader 的做法）。
	// x-api-key 与 authorization 被 hop-by-hop 过滤，因此上游密钥不会被客户端覆盖。
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("anthropic-beta", DefaultAnthropicPassthroughBeta)
	req.Header.Set("X-API-Key", key)

	parsedUrl, err := url.Parse(strings.TrimSuffix(baseUrl, "/"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse base url: %w", err)
	}
	parsedUrl.Path = parsedUrl.Path + "/messages"
	if query != nil {
		parsedUrl.RawQuery = query.Encode()
	}
	req.URL = parsedUrl

	return req, nil
}

// rewriteRawRequestModel 仅替换顶层 "model" 字段的 JSON 字符串值，保持其他字节原封不动。
func rewriteRawRequestModel(rawBody []byte, modelName string) ([]byte, error) {
	return rawjson.ReplaceTopLevelString(rawBody, "model", strings.TrimSpace(modelName))
}

// CanPassthrough implements model.PassthroughCapable.
// Returns true when the inbound format is Anthropic Messages API.
func (o *MessageOutbound) CanPassthrough(inboundFormat model.APIFormat) bool {
	return inboundFormat == model.APIFormatAnthropicMessage
}

// PassthroughConfig implements model.PassthroughCapable.
// Returns Anthropic-specific passthrough settings.
func (o *MessageOutbound) PassthroughConfig() model.PassthroughConfig {
	return model.PassthroughConfig{
		TerminalEvents: map[string]struct{}{
			"message_stop": {},
			"error":        {},
		},
		CollectMetrics: true, // Anthropic requires full response aggregation for metrics
	}
}
