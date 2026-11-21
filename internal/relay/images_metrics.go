package relay

import (
	"context"
	"time"

	"github.com/suprelory/octopus-plus/internal/conf"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/price"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

type imagesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type imagesRelayMetrics struct {
	capture        *relayCapture
	attemptCapture *exchangeCapture
	execution      *relayExecution
	affinity       balancer.AffinityOptions
	APIKeyID       int
	RequestModel   string
	ClientIP       string
	ActualModel    string
	StartTime      time.Time
	FirstToken     time.Time

	Stats model.StatsMetrics
}

func newImagesRelayMetrics(apiKeyID int, requestModel, clientIP string) *imagesRelayMetrics {
	return &imagesRelayMetrics{
		APIKeyID:     apiKeyID,
		RequestModel: requestModel,
		ClientIP:     clientIP,
		StartTime:    time.Now(),
	}
}

func (m *imagesRelayMetrics) SetFirstTokenTime(t time.Time) {
	if m.FirstToken.IsZero() {
		m.FirstToken = t
	}
}

func (m *imagesRelayMetrics) SetUsageFromImages(actualModel string, u imagesUsage) {
	m.ActualModel = actualModel
	m.Stats.InputToken = int64(u.InputTokens)
	m.Stats.OutputToken = int64(u.OutputTokens)

	modelPrice := price.GetLLMPrice(m.RequestModel)
	if modelPrice == nil {
		return
	}

	m.Stats.InputCost = float64(u.InputTokens) * modelPrice.Input * 1e-6
	m.Stats.OutputCost = float64(u.OutputTokens) * modelPrice.Output * 1e-6
}

func (m *imagesRelayMetrics) SaveWithChannelStats(ctx context.Context, success bool, err error, attempts []model.ChannelAttempt, updateChannelStats bool) {
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
	if saveErr := op.StatsRecordRequest(context.Background(), m.APIKeyID, channelID, globalStats, updateChannelStats); saveErr != nil {
		log.Warnf("failed to persist daily rollover: %v", saveErr)
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
		}
		if success {
			log.Infow("relay.images.complete", fields...)
		} else {
			log.Warnw("relay.images.complete", fields...)
		}
	}

	m.saveLog(ctx, success, err, duration, attempts, channelID, channelName)
}

func (m *imagesRelayMetrics) saveLog(ctx context.Context, success bool, err error, duration time.Duration, attempts []model.ChannelAttempt, channelID int, channelName string) {
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
		EndpointType:     "images",
		ClientIP:         m.ClientIP,
		ChannelName:      channelName,
		ChannelId:        channelID,
		ActualModelName:  actualModel,
		UseTime:          int(duration.Milliseconds()),
		Attempts:         attempts,
		TotalAttempts:    len(attempts),
	}

	if apiKey, getErr := op.APIKeyGet(m.APIKeyID, ctx); getErr == nil {
		relayLog.RequestAPIKeyName = apiKey.Name
	}

	// 首字时间
	if !m.FirstToken.IsZero() {
		relayLog.Ftut = int(m.FirstToken.Sub(m.StartTime).Milliseconds())
	}

	// Usage
	if m.Stats.InputToken > 0 || m.Stats.OutputToken > 0 {
		relayLog.InputTokens = int(m.Stats.InputToken)
		relayLog.OutputTokens = int(m.Stats.OutputToken)
		relayLog.Cost = m.Stats.InputCost + m.Stats.OutputCost
	}

	if err != nil {
		relayLog.Error = err.Error()
	}
	relayLog.Success = success

	if logErr := op.RelayLogAdd(relayLog); logErr != nil {
		log.Warnf("failed to save relay log: %v", logErr)
	}
}
