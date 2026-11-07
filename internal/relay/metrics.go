package relay

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/suprelory/octopus-plus/internal/conf"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/price"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/utils/tokenizer"
	transformerModel "github.com/suprelory/octopus-plus/polywire/model"
)

// RelayMetrics 负责最终的日志收集与持久化
type RelayMetrics struct {
	capture      *relayCapture
	execution    *relayExecution
	affinity     balancer.AffinityOptions
	APIKeyID     int
	RequestModel string
	EndpointType string
	ClientIP     string
	StartTime    time.Time

	// 首 Token 时间
	FirstTokenTime time.Time

	// 请求和响应内容
	RawRequest       []byte
	InternalRequest  *transformerModel.InternalLLMRequest
	InternalResponse *transformerModel.InternalLLMResponse

	// 统计指标
	ActualModel string
	Stats       model.StatsMetrics
	UsedWS      bool
	WSMode      *model.RelayLogWSMode
	WSExecMode  *model.RelayLogWSExecMode
	WSRecovery  *model.RelayLogWSRecovery
	costPrice   *model.LLMPrice

	TransportInputTokens *int
	BillInputTokens      *int
	CacheReadTokens      *int
	CacheWriteTokens     *int
}

func NewRelayMetrics(apiKeyID int, requestModel, endpointType, clientIP string, rawBody []byte, req *transformerModel.InternalLLMRequest) *RelayMetrics {
	return &RelayMetrics{
		APIKeyID:        apiKeyID,
		RequestModel:    requestModel,
		EndpointType:    endpointType,
		ClientIP:        clientIP,
		StartTime:       time.Now(),
		RawRequest:      rawBody,
		InternalRequest: req,
	}
}

func (m *RelayMetrics) SetFirstTokenTime(t time.Time) {
	m.FirstTokenTime = t
}

func (m *RelayMetrics) SetTransportRequestPayload(payload []byte, modelName string) {
	if len(payload) == 0 {
		return
	}
	count := tokenizer.CountTokensBytes(payload, modelName)
	m.TransportInputTokens = intPtr(count)
}

func (m *RelayMetrics) SetWSMode(mode model.RelayLogWSMode) {
	if mode == "" {
		return
	}
	m.WSMode = wsModePtr(mode)
}

func (m *RelayMetrics) SetWSExecMode(mode model.RelayLogWSExecMode) {
	if mode == "" {
		return
	}
	m.WSExecMode = wsExecModePtr(mode)
}

func (m *RelayMetrics) SetWSRecovery(recovery model.RelayLogWSRecovery) {
	if recovery == "" {
		return
	}
	m.WSRecovery = wsRecoveryPtr(recovery)
}

func (m *RelayMetrics) SetInternalResponse(resp *transformerModel.InternalLLMResponse, actualModel string) {
	m.InternalResponse = resp
	m.ActualModel = actualModel

	if resp == nil {
		return
	}

	inputReported := false
	if usage := resp.Usage; usage != nil {
		nonCachedInput := usage.BillableNonCachedInput()
		cacheReadTokens := usage.BillableCacheReadInput()
		cacheWriteTokens := usage.BillableCacheWriteInput()

		m.BillInputTokens = intPtr(int(nonCachedInput))
		m.CacheReadTokens = intPtr(int(cacheReadTokens))
		m.CacheWriteTokens = intPtr(int(cacheWriteTokens))
		m.Stats.InputToken = usage.PromptTokens
		m.Stats.OutputToken = usage.CompletionTokens
		inputReported = usage.EffectiveInputTokens() > 0

		if modelPrice := m.modelPrice(); modelPrice != nil {
			m.Stats.InputCost = (float64(cacheReadTokens)*modelPrice.CacheRead +
				float64(cacheWriteTokens)*modelPrice.CacheWrite +
				float64(nonCachedInput)*modelPrice.Input) * 1e-6
			m.Stats.OutputCost = float64(usage.CompletionTokens) * modelPrice.Output * 1e-6
		}
	}

	// 降级：上游未上报 input（usage 缺失，或 usage 中输入侧全为 0）时，用请求侧
	// 估算的 TransportInputTokens 兜底，使 input token/费用不为 0；output 无法从
	// 请求侧估算，保持 0。tiktoken 统一用 o200k_base，对 Claude/Gemini 为近似值。
	if !inputReported && m.TransportInputTokens != nil && *m.TransportInputTokens > 0 {
		estimated := int64(*m.TransportInputTokens)
		m.Stats.InputToken = estimated
		m.BillInputTokens = intPtr(int(estimated))
		if modelPrice := m.modelPrice(); modelPrice != nil {
			m.Stats.InputCost = float64(estimated) * modelPrice.Input * 1e-6
		}
	}
}

func (m *RelayMetrics) SaveWithChannelStats(ctx context.Context, success bool, err error, attempts []model.ChannelAttempt, updateChannelStats bool) {
	m.capture.finishOutcome(success, err)
	duration := time.Since(m.StartTime)

	globalStats := model.StatsMetrics{
		WaitTime:    duration.Milliseconds(),
		InputToken:  m.Stats.InputToken,
		OutputToken: m.Stats.OutputToken,
		InputCost:   m.Stats.InputCost,
		OutputCost:  m.Stats.OutputCost,
	}
	if success {
		globalStats.RequestSuccess = 1
	} else {
		globalStats.RequestFailed = 1
	}

	channelID, channelName := finalChannel(attempts)
	if saveErr := op.StatsRecordRequest(context.Background(), m.APIKeyID, channelID, globalStats, updateChannelStats, attempts, m.ActualModel); saveErr != nil {
		log.Warnf("failed to persist daily rollover: %v", saveErr)
	}

	// 上游未上报 usage（或输入侧全为 0）时打告警，便于定位是哪个通道缺失 usage。
	if success && (m.InternalResponse == nil || m.InternalResponse.Usage == nil ||
		m.InternalResponse.Usage.EffectiveInputTokens() == 0) {
		fallbackInput := 0
		if m.TransportInputTokens != nil {
			fallbackInput = *m.TransportInputTokens
		}
		log.Debugw("relay.usage_missing",
			"actual_model", m.ActualModel,
			"channel_id", channelID,
			"channel", channelName,
			"had_usage", m.InternalResponse != nil && m.InternalResponse.Usage != nil,
			"fallback_input_tokens", fallbackInput,
		)
	}

	if conf.AppConfig.Log.Relay.Summary || !success {
		fields := []interface{}{
			"model", m.RequestModel,
			"actual_model", m.ActualModel,
			"channel_id", channelID,
			"channel", channelName,
			"success", success,
			"duration_ms", duration.Milliseconds(),
			"input_token", m.Stats.InputToken,
			"output_token", m.Stats.OutputToken,
			"input_cost", m.Stats.InputCost,
			"output_cost", m.Stats.OutputCost,
			"total_cost", m.Stats.InputCost + m.Stats.OutputCost,
			"attempts", len(attempts),
			"ws", m.UsedWS,
		}
		if success {
			log.Infow("relay.complete", fields...)
		} else {
			log.Warnw("relay.complete", fields...)
		}
	}

	m.saveLog(ctx, success, err, duration, attempts, channelID, channelName)
}

func finalChannel(attempts []model.ChannelAttempt) (int, string) {
	var lastID int
	var lastName string
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if a.Status == model.AttemptSuccess {
			return a.ChannelID, a.ChannelName
		}
		if a.Status == model.AttemptFailed && lastID == 0 {
			lastID = a.ChannelID
			lastName = a.ChannelName
		}
	}
	return lastID, lastName
}

func (m *RelayMetrics) saveLog(ctx context.Context, success bool, err error, duration time.Duration, attempts []model.ChannelAttempt, channelID int, channelName string) {
	attempts = explainRouting(attempts, m.execution, m.affinity, success, err)
	actualModel := m.ActualModel
	if actualModel == "" {
		actualModel = m.RequestModel
	}

	relayLog := model.RelayLog{
		Trace:            m.capture.snapshot(ctx),
		Time:             m.StartTime.Unix(),
		RequestModelName: m.RequestModel,
		RequestAPIKeyID:  m.APIKeyID,
		ClientIP:         m.ClientIP,
		EndpointType:     m.EndpointType,
		ChannelName:      channelName,
		ChannelId:        channelID,
		ActualModelName:  actualModel,
		UseTime:          int(duration.Milliseconds()),
		Attempts:         attempts,
		TotalAttempts:    len(attempts),
		UsedWS:           m.UsedWS,
	}
	if m.InternalRequest != nil {
		relayLog.ReasoningEffort = strings.TrimSpace(m.InternalRequest.ReasoningEffort)
	}

	if apiKey, getErr := op.APIKeyGet(m.APIKeyID, ctx); getErr == nil {
		relayLog.RequestAPIKeyName = apiKey.Name
	}

	// 首字时间
	if !m.FirstTokenTime.IsZero() {
		relayLog.Ftut = int(m.FirstTokenTime.Sub(m.StartTime).Milliseconds())
	}

	// Usage：统一从 Stats 读取。Stats 在 SetInternalResponse 中已由上游 usage 填充，
	// 或在 usage 缺失时由 TransportInputTokens 降级填充，确保降级值也写入日志。
	relayLog.InputTokens = int(m.Stats.InputToken)
	relayLog.OutputTokens = int(m.Stats.OutputToken)
	relayLog.Cost = m.Stats.InputCost + m.Stats.OutputCost
	relayLog.TransportInputTokens = m.TransportInputTokens
	relayLog.BillInputTokens = m.BillInputTokens
	relayLog.CacheReadTokens = m.CacheReadTokens
	relayLog.CacheWriteTokens = m.CacheWriteTokens
	if m.InternalResponse != nil && m.InternalResponse.Usage != nil && m.InternalResponse.Usage.CompletionTokensDetails != nil {
		relayLog.ReasoningTokens = int(m.InternalResponse.Usage.CompletionTokensDetails.ReasoningTokens)
	}
	if relayLog.ReasoningTokens <= 0 {
		relayLog.ReasoningChars = reasoningCharsFromResponse(m.InternalResponse)
	}
	relayLog.WSMode = m.WSMode
	relayLog.WSExecMode = m.WSExecMode
	relayLog.WSRecovery = m.WSRecovery

	// 请求内容：优先原始请求体，保留 provider 专有字段（如 Anthropic cache_control）
	if m.capture == nil && len(m.RawRequest) > 0 {
		relayLog.RequestContent = string(m.RawRequest)
	} else if m.capture == nil && m.InternalRequest != nil {
		if reqJSON, jsonErr := json.Marshal(m.InternalRequest); jsonErr == nil {
			relayLog.RequestContent = string(reqJSON)
		}
	}

	// 响应内容
	if m.capture == nil && m.InternalResponse != nil {
		respForLog := m.filterResponseForLog(m.InternalResponse)
		if respJSON, jsonErr := json.Marshal(respForLog); jsonErr == nil {
			relayLog.ResponseContent = string(respJSON)
		}
	}

	// 错误信息
	if err != nil {
		relayLog.Error = err.Error()
	}
	relayLog.Success = success

	if logErr := op.RelayLogAdd(relayLog); logErr != nil {
		log.Warnf("failed to save relay log: %v", logErr)
	}
}

func reasoningCharsFromResponse(resp *transformerModel.InternalLLMResponse) int {
	if resp == nil {
		return 0
	}
	total := 0
	for i := range resp.Choices {
		choice := &resp.Choices[i]
		if choice.Message != nil {
			total += utf8.RuneCountInString(choice.Message.GetReasoningContent())
		}
		if choice.Delta != nil {
			total += utf8.RuneCountInString(choice.Delta.GetReasoningContent())
		}
	}
	return total
}

func updateFinalChannelUsageStats(channelID int, metrics model.StatsMetrics) {
	if channelID == 0 {
		return
	}
	usageStats := model.StatsMetrics{
		InputToken:  metrics.InputToken,
		OutputToken: metrics.OutputToken,
		InputCost:   metrics.InputCost,
		OutputCost:  metrics.OutputCost,
	}
	if usageStats.InputToken == 0 && usageStats.OutputToken == 0 && usageStats.InputCost == 0 && usageStats.OutputCost == 0 {
		return
	}
	op.StatsChannelUpdate(channelID, usageStats)
}

func intPtr(value int) *int {
	return &value
}

// resolveModelPrice returns the global price configured for the client-requested model.
func resolveModelPrice(requestModel string) *model.LLMPrice {
	return price.GetLLMPrice(requestModel)
}

func (m *RelayMetrics) modelPrice() *model.LLMPrice {
	if m.costPrice != nil {
		return m.costPrice
	}
	return resolveModelPrice(m.RequestModel)
}

func wsModePtr(value model.RelayLogWSMode) *model.RelayLogWSMode {
	return &value
}

func wsExecModePtr(value model.RelayLogWSExecMode) *model.RelayLogWSExecMode {
	return &value
}

func wsRecoveryPtr(value model.RelayLogWSRecovery) *model.RelayLogWSRecovery {
	return &value
}

// filterResponseForLog 创建响应的浅拷贝，过滤掉 images、MultipleContent 中的图片数据和 Audio.Data 以减少存储压力
func (m *RelayMetrics) filterResponseForLog(resp *transformerModel.InternalLLMResponse) *transformerModel.InternalLLMResponse {
	if resp == nil {
		return nil
	}

	filterMsg := func(msg *transformerModel.Message) *transformerModel.Message {
		if msg == nil {
			return nil
		}
		c := *msg
		c.Images = nil
		if len(c.Content.MultipleContent) > 0 {
			parts := make([]transformerModel.MessageContentPart, 0, len(c.Content.MultipleContent))
			for _, p := range c.Content.MultipleContent {
				if p.Type == "image_url" && p.ImageURL != nil {
					parts = append(parts, transformerModel.MessageContentPart{
						Type:     "image_url",
						ImageURL: &transformerModel.ImageURL{URL: "[image data omitted for storage]"},
					})
				} else {
					parts = append(parts, p)
				}
			}
			c.Content = transformerModel.MessageContent{Content: c.Content.Content, MultipleContent: parts}
		}
		if c.Audio != nil && c.Audio.Data != "" {
			a := *c.Audio
			a.Data = "[audio data omitted for storage]"
			c.Audio = &a
		}
		return &c
	}

	filtered := *resp
	filtered.Choices = make([]transformerModel.Choice, len(resp.Choices))
	for i, choice := range resp.Choices {
		filtered.Choices[i] = choice
		filtered.Choices[i].Message = filterMsg(choice.Message)
		filtered.Choices[i].Delta = filterMsg(choice.Delta)
	}
	return &filtered
}
