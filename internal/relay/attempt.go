package relay

import (
	"errors"
	"fmt"
	"github.com/suprelory/octopus-plus/internal/relay/capture"
	"net/http"
	"strings"
	"time"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/internal/relay/stream"
)

// attempt 统一管理一次通道尝试的完整生命周期
func (ra *relayAttempt) attempt() attemptResult {
	defer ra.closeFirstTokenBudget()

	span := ra.iter.StartAttempt(ra.channel.ID, ra.usedKey.ID, ra.channel.Name)
	ra.capture = ra.metrics.capture.beginAttempt(ra.channel, ra.internalRequest.Model, ra.usedKey.ChannelKey)
	span.SetAdapterType(ra.channel.Type.String())
	span.SetCapability(capabilityTrace(ra.capabilityDecision, ra.capabilityPolicy, ra.channel.Type.String()))

	// 转发请求
	statusCode, fwdErr := ra.forward()
	if ra.capture != nil {
		ra.capture.info.Transport = ra.upstreamTransport
		if ra.streamDiagnostics != nil {
			ra.capture.info.CompletionStatus = ra.streamDiagnostics.CompletionStatus
			ra.capture.info.FinishCause = string(ra.streamDiagnostics.FinishCause)
		}
		if fwdErr != nil {
			ra.capture.info.Error = capture.Text(fwdErr.Error(), ra.capture.secrets...)
		}
		exchange := ra.capture.snapshot()
		var status *int
		if exchange.Response != nil {
			status = exchange.Response.StatusCode
		}
		span.SetHTTPDetails(exchange.AttemptID, status, exchange.UpstreamRequestID)
	}
	span.SetCapability(capabilityTrace(ra.capabilityDecision, ra.capabilityPolicy, ra.channel.Type.String()))
	span.SetStreamDiagnostics(ra.streamDiagnostics)
	mode, recoveryMode := dbmodel.RelayLogWSMode(""), dbmodel.RelayLogWSRecovery("")
	if ra.upstreamTransport == "ws" {
		mode = defaultWSModeForRequest(ra.internalRequest)
	}
	if ra.internalRequest.IsOpenAIExactReplayRequest() {
		mode, recoveryMode = dbmodel.RelayLogWSModeReplay, dbmodel.RelayLogWSRecoveryReplay
	} else if ra.transportRecovery == upstreamRecoveryReconnect {
		recoveryMode = dbmodel.RelayLogWSRecoveryReconnect
	} else if ra.transportRecovery == upstreamRecoveryHTTP {
		recoveryMode = dbmodel.RelayLogWSRecoveryDowngrade
	}
	span.SetTransport(ra.upstreamTransport, mode, recoveryMode)
	// Some transports surface a canceled read instead of the cancel cause. Do
	// the cause translation once at the attempt boundary so HTTP error bodies,
	// transformed streams, and WS passthrough share the same timeout semantics.
	if fwdErr != nil &&
		!isLocalRelayBudgetError(fwdErr) &&
		!isFirstTokenTimeoutError(fwdErr) {
		if timeoutErr := ra.firstTokenTimeoutIfNeeded(ra.requestContext(), fwdErr); timeoutErr != nil {
			fwdErr = timeoutErr
		}
	}

	// 更新 channel key 状态
	ra.usedKey.StatusCode = statusCode
	ra.usedKey.LastUseTimeStamp = time.Now().Unix()

	if fwdErr == nil {
		// ====== 成功 ======
		// Passthrough handlers collect response at stream end via PassthroughConfig.CollectMetrics
		ra.collectResponse()
		op.ChannelKeyUpdateWithDelta(ra.usedKey, ra.metrics.Stats.InputCost+ra.metrics.Stats.OutputCost)

		span.End(dbmodel.AttemptSuccess, statusCode, "")

		// Channel 维度统计
		op.StatsChannelUpdate(ra.channel.ID, dbmodel.StatsMetrics{
			WaitTime:       span.Duration().Milliseconds(),
			RequestSuccess: 1,
		})

		// 熔断器：记录成功
		balancer.RecordSuccess(ra.channel.ID, ra.usedKey.ID, ra.breakerModelName())
		// Refresh model affinity only after the complete response succeeds.
		ra.iter.RecordAffinity(ra.channel.ID, ra.usedKey.ID)

		return attemptResult{Success: true}
	}

	// ====== 失败 ======
	if isClientCancellation(ra.requestContext(), fwdErr) || errors.Is(fwdErr, stream.ErrDownstreamWrite) {
		written := ra.responseCommitted()
		if written {
			ra.collectResponse()
		}
		op.ChannelKeyUpdateWithDelta(ra.usedKey, 0)
		span.SetFailure(string(FailureClientCanceled), false, time.Time{})
		span.End(dbmodel.AttemptFailed, statusCode, fwdErr.Error())
		return attemptResult{
			Success:    false,
			Written:    written,
			Canceled:   true,
			Err:        fwdErr,
			StatusCode: statusCode,
			Failure:    FailureClassification{Class: FailureClientCanceled, StatusCode: statusCode},
		}
	}

	failure := classifyRelayFailureContext(ra.requestContext(), statusCode, fwdErr, ra.retryAt)
	span.SetFailure(string(failure.Class), failure.Retryable, failure.RetryAt, failure.Scope)
	op.ChannelKeyUpdateWithDelta(ra.usedKey, 0)
	span.End(dbmodel.AttemptFailed, statusCode, fwdErr.Error())

	// Channel 维度统计
	op.StatsChannelUpdate(ra.channel.ID, dbmodel.StatsMetrics{
		WaitTime:      span.Duration().Milliseconds(),
		RequestFailed: 1,
	})

	// 注意：熔断器记录在公共执行器的同通道重试循环外，
	// 避免重试期间过早触发熔断

	written := ra.responseCommitted()
	if written {
		ra.collectResponse()
		if responseError := protocolErrorFromAttempt(ra.upstreamError, statusCode, fwdErr); responseError != nil && !ra.protocolErrorWritten {
			writeStreamProtocolError(ra.requestContext(), ra.getStreamWriter(), ra.inAdapter, responseError)
		}
	}
	firstTokenTimeout := isFirstTokenTimeoutError(fwdErr)
	var recoveryErr *upstreamRecoveryError
	recovery := upstreamRecoveryNone
	if errors.As(fwdErr, &recoveryErr) && !firstTokenTimeout && !isLocalRelayBudgetError(fwdErr) {
		recovery = recoveryErr.recovery
	}
	return attemptResult{
		Success:           false,
		Written:           written,
		ResetConversation: statusCode == http.StatusConflict && needsConversationRestart(relayErrorMessage(fwdErr)),
		FirstTokenTimeout: firstTokenTimeout,
		EmptyResponse:     errors.Is(fwdErr, stream.ErrEmptyUpstreamStream),
		Err:               fmt.Errorf("channel %s failed: %w", ra.channel.Name, fwdErr),
		StatusCode:        statusCode,
		RetryAt:           ra.retryAt,
		Failure:           failure,
		ProtocolError:     protocolErrorFromAttempt(ra.upstreamError, statusCode, fwdErr),
		Recovery:          recovery,
	}
}

func (ra *relayAttempt) breakerModelName() string {
	if ra != nil && ra.iter != nil && ra.iter.Index() >= 0 && ra.iter.Index() < ra.iter.Len() {
		if modelName := strings.TrimSpace(ra.iter.Item().ModelName); modelName != "" {
			return modelName
		}
	}
	if ra != nil && ra.internalRequest != nil {
		return strings.TrimSpace(ra.internalRequest.Model)
	}
	return ""
}
