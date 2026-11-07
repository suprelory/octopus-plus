package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/helper"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/relay/bodycache"
	"github.com/suprelory/octopus-plus/internal/relay/capture"
	transformerModel "github.com/suprelory/octopus-plus/polywire/model"
)

const imagesUpstreamErrorBodyLimit = 16 * 1024

func imagesAttempt(
	ctx context.Context,
	endpoint string,
	c *gin.Context,
	bc *bodycache.BodyCache,
	isMultipart bool,
	boundary string,
	jsonPayload map[string]any,
	stream bool,
	channel *model.Channel,
	channelKey string,
	firstTokenTimeOutSec int,
	metrics *imagesRelayMetrics,
	actualModel string,
	hb *earlyHeartbeat,
	retryAtOut *time.Time,
	executions ...*relayExecution,
) (statusCode int, written bool, usage *imagesUsage, upstreamCT string, err error) {
	var trace *relayCapture
	var exchange *exchangeCapture
	if metrics != nil {
		trace = metrics.capture
		exchange = trace.beginAttempt(channel, actualModel, channelKey)
		metrics.attemptCapture = exchange
	}
	defer func() {
		if exchange == nil {
			return
		}
		if err != nil {
			exchange.info.Error = capture.Text(err.Error(), exchange.secrets...)
		}
		if stream {
			exchange.info.CompletionStatus = "completed"
			if err != nil {
				exchange.info.CompletionStatus = "interrupted"
			}
		}
	}()
	var execution *relayExecution
	var budget *firstTokenBudget
	if len(executions) > 0 {
		execution = executions[0]
	}
	if execution != nil {
		timeout, cause := time.Until(execution.deadline()), error(errLocalRelayBudgetExceeded)
		if stream && firstTokenTimeOutSec > 0 && time.Duration(firstTokenTimeOutSec)*time.Second < timeout {
			timeout, cause = time.Duration(firstTokenTimeOutSec)*time.Second, errFirstTokenTimeout
		}
		budget = newFirstTokenBudget(ctx, timeout, cause)
		ctx = budget.ctx
		defer budget.close()
		defer func() {
			if err != nil && context.Cause(ctx) != nil {
				err = context.Cause(ctx)
			}
		}()
	}
	commit := func() {
		if !stream {
			hb.Stop()
		}
		trace.bindDownstream(exchange)
		budget.stopTimer()
		if execution != nil {
			execution.committed.Store(true)
		}
	}
	// 构建 URL（baseUrl.Path 后追加 endpoint）
	baseURL := channel.GetBaseUrl()
	parsedURL, err := url.Parse(strings.TrimSuffix(baseURL, "/"))
	if err != nil {
		return 0, false, nil, "", classifyLocalRelayError(FailureConfiguration, fmt.Errorf("failed to parse base url: %w", err))
	}
	parsedURL.Path = parsedURL.Path + endpoint

	var bodyReader io.Reader
	var contentType string

	if isMultipart {
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		contentType = mw.FormDataContentType()
		bodyReader = pr
		defer pr.Close()

		go func() {
			src, err := bc.NewReader()
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			defer src.Close()

			if err := copyMultipartReplaceModel(src, boundary, mw, actualModel); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			// 先关闭 multipart.Writer 写入结束 boundary，再关闭 pipe writer
			if err := mw.Close(); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			_ = pw.Close()
		}()
	} else {
		// JSON：仅改写 model 字段，其余保持不变
		// 注意：每次尝试都重新 marshal 生成 body，确保可重试重建
		if jsonPayload == nil {
			return 0, false, nil, "", classifyLocalRelayError(FailureConfiguration, errors.New("nil json payload"))
		}
		jsonPayload["model"] = actualModel
		b, err := json.Marshal(jsonPayload)
		if err != nil {
			return 0, false, nil, "", classifyLocalRelayError(FailureConfiguration, fmt.Errorf("failed to marshal json: %w", err))
		}
		if channelParamOverrideConfigured(channel) {
			b, _, err = helper.ApplyParamOverridePayload(b, channel.ParamOverride)
			if err != nil {
				return 0, false, nil, "", classifyLocalRelayError(FailureConfiguration, fmt.Errorf("invalid channel param override: %w", err))
			}
		}
		actualModel, err = requiredJSONModel(b)
		if err != nil {
			return 0, false, nil, "", classifyLocalRelayError(FailureConfiguration, fmt.Errorf("channel param override produced an invalid images request: %w", err))
		}
		bodyReader = bytes.NewReader(b)
		contentType = "application/json"
	}
	if metrics != nil {
		metrics.ActualModel = actualModel
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "", bodyReader)
	if err != nil {
		return 0, false, nil, "", classifyLocalRelayError(FailureConfiguration, fmt.Errorf("failed to create request: %w", err))
	}
	req.URL = parsedURL
	req.Method = http.MethodPost

	// Header 透传：复制下游 header，过滤 hop-by-hop 与鉴权相关
	copyHeadersToUpstream(req, c, channel, channelKey, contentType, stream)

	// 发送请求
	httpClient, err := helper.ChannelHTTPClientWithContext(ctx, channel)
	if err != nil {
		return 0, false, nil, "", classifyLocalRelayError(FailureConfiguration, err)
	}

	if execution != nil {
		if err := execution.reserveSubmission(ctx, relayCandidate{channel.ID, executionKeyID(channel, channelKey), actualModel}); err != nil {
			return 0, false, nil, "", err
		}
	}
	if exchange != nil {
		exchange.info.Model = actualModel
		if isMultipart {
			exchange.traceHTTP(req)
			exchange.request = trace.captureImagesRequest(req, bc, boundary, actualModel, exchange.secrets...)
		} else {
			trace.upstreamRequest(exchange, req)
		}
	}
	respUp, err := httpClient.Do(req)
	trace.upstreamResponse(exchange, respUp)
	if err != nil {
		return 0, false, nil, "", fmt.Errorf("failed to send request: %w", err)
	}
	defer respUp.Body.Close()

	upstreamCT = respUp.Header.Get("Content-Type")

	// stream=true：逐行解析 event/data/空行边界透传
	if stream {
		if respUp.StatusCode < 200 || respUp.StatusCode >= 300 {
			if retryAtOut != nil {
				*retryAtOut = parseRetryAt(respUp.Header.Get("Retry-After"))
			}
			b, _ := io.ReadAll(io.LimitReader(respUp.Body, imagesUpstreamErrorBodyLimit))
			return respUp.StatusCode, false, nil, upstreamCT, transformerModel.NormalizeHTTPError(respUp.StatusCode, respUp.Header, b, "api_error")
		}
		u, w, err := proxySSE(ctx, c, respUp, firstTokenTimeOutSec, metrics, hb, commit)
		return respUp.StatusCode, w, u, upstreamCT, err
	}

	// 非流式：2xx 透传，否则读取限长错误体用于错误信息与重试判定
	if respUp.StatusCode < 200 || respUp.StatusCode >= 300 {
		if retryAtOut != nil {
			*retryAtOut = parseRetryAt(respUp.Header.Get("Retry-After"))
		}
		b, _ := io.ReadAll(io.LimitReader(respUp.Body, imagesUpstreamErrorBodyLimit))
		return respUp.StatusCode, false, nil, upstreamCT, transformerModel.NormalizeHTTPError(respUp.StatusCode, respUp.Header, b, "api_error")
	}

	u, w, err := proxyNonStream(c, respUp, commit)

	// Empty response detection: if nothing was written, treat as empty response
	if err == nil && !w {
		err = fmt.Errorf("empty image response: no data written")
	}

	return respUp.StatusCode, w, u, upstreamCT, err
}

// proxyNonStream 将上游非流式响应原样透传到下游，同时尽量提取 usage（避免解析巨大 b64_json）。
func proxyNonStream(c *gin.Context, respUp *http.Response, onCommit ...func()) (*imagesUsage, bool, error) {
	ct := respUp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	scanner := newUsageScanner()
	written := false

	buf := make([]byte, 32*1024)
	for {
		n, rerr := respUp.Body.Read(buf)
		if n > 0 {
			for _, commit := range onCommit {
				commit()
			}
			if !written {
				c.Header("Content-Type", ct)
				c.Status(respUp.StatusCode)
				written = true
			}
			chunk := buf[:n]
			scanner.Feed(chunk)
			if _, werr := c.Writer.Write(chunk); werr != nil {
				return scanner.Usage(), true, werr
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return scanner.Usage(), written, rerr
		}
	}

	return scanner.Usage(), written, nil
}

func executionKeyID(channel *model.Channel, value string) int {
	for _, key := range channel.Keys {
		if key.ChannelKey == value {
			return key.ID
		}
	}
	return 0
}
