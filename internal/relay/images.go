package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/helper"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/internal/relay/bodycache"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

// ImagesHandler 是 OpenAI Images API 的统一 relay 入口。
// endpoint 形如：/images/generations、/images/edits、/images/variations（不含 /v1 前缀）。
func ImagesHandler(endpoint string, c *gin.Context) {
	trace := startHTTPRelayCapture(c)
	requestModel, ready := "", false
	defer recordEarlyHTTPFailure(c, &requestModel, "images", &ready, time.Now())
	ctx := c.Request.Context()
	if op.APIKeyCostReservationFromContext(ctx).Limited() {
		err := newCostBudgetError("images do not support a reliable maximum cost estimate for cost-limited API keys")
		response := protocolErrorFromError(http.StatusBadRequest, err)
		resp.ErrorWithCode(c, response.StatusCode, response.Detail.Code, response.Detail.Message)
		return
	}

	apiKeyID := c.GetInt("api_key_id")

	// 缓存请求体，支持多次重试重放
	bc, err := bodycache.New(c.Request.Body)
	if err != nil {
		var tooLarge *bodycache.BodyTooLargeError
		if errors.As(err, &tooLarge) {
			resp.Error(c, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() {
		if cerr := bc.Close(); cerr != nil {
			log.Warnf("failed to close images body cache: %v", cerr)
		}
	}()

	contentType := c.GetHeader("Content-Type")
	isMultipart := strings.Contains(strings.ToLower(contentType), "multipart/form-data")

	// 解析 requestModel 与 stream（严格模式：model 必填）
	var (
		stream      bool
		boundary    string
		jsonPayload map[string]any
	)
	if isMultipart {
		_, params, perr := mime.ParseMediaType(contentType)
		if perr != nil {
			resp.Error(c, http.StatusBadRequest, "invalid multipart content-type")
			return
		}
		boundary = strings.TrimSpace(params["boundary"])
		if boundary == "" {
			resp.Error(c, http.StatusBadRequest, "invalid multipart boundary")
			return
		}
		m, s, perr := parseMultipartModelAndStream(bc, boundary)
		if perr != nil {
			resp.Error(c, http.StatusBadRequest, perr.Error())
			return
		}
		requestModel = m
		stream = s
	} else {
		payload, m, s, perr := parseJSONModelAndStream(bc)
		if perr != nil {
			resp.Error(c, http.StatusBadRequest, perr.Error())
			return
		}
		jsonPayload = payload
		requestModel = m
		stream = s
	}

	// supported_models 校验（复用 APIKeyAuth 注入）
	supportedModels := strings.TrimSpace(c.GetString("supported_models"))
	if supportedModels != "" {
		supportedModelsArray := strings.Split(supportedModels, ",")
		if !slices.Contains(supportedModelsArray, requestModel) {
			resp.ErrorWithCode(c, http.StatusBadRequest, CodeRelayModelNotSupported, "model not supported")
			return
		}
	}

	// 获取通道分组
	group, err := op.GroupGetEnabledMap(requestModel, ctx)
	if err != nil {
		resp.ErrorWithCode(c, http.StatusNotFound, CodeRelayModelNotFound, "model not found")
		return
	}
	candidateSnapshot := newCandidateSnapshot(ctx, group)

	// 创建迭代器（策略排序 + 粘性优先）
	affinityBody, _ := json.Marshal(jsonPayload)
	iter := balancer.NewIterator(group, apiKeyID, requestModel, resolveRequestAffinity(c.Request.Header, affinityBody))
	if iter.Len() == 0 {
		resp.ErrorWithCode(c, http.StatusServiceUnavailable, CodeRelayNoAvailableChannel, "no available channel")
		return
	}
	defer iter.Close()

	// 初始化 Metrics（Images 独立，避免 b64_json 内存膨胀）
	ready = true
	resp.UseSafeErrorLogDetails(c, CodeRelayUpstreamFailed, "relay response details omitted from console")
	metrics := newImagesRelayMetrics(apiKeyID, requestModel, middleware.ClientIP(c))
	metrics.capture, metrics.StartTime = trace, trace.started
	trace.client.request = trace.captureImagesRequest(c.Request, bc, boundary, "")

	// Keep the downstream alive during connection setup and retries. JSON
	// responses use leading whitespace when non-streaming keep-alives are enabled.
	hb := startEarlyHeartbeat(c, stream)
	ctx = c.Request.Context()
	defer hb.Stop()
	var savedSuccess bool
	var savedErr error
	defer func() {
		hb.Stop()
		metrics.SaveWithChannelStats(ctx, savedSuccess, savedErr, iter.Attempts(), false)
	}()

	var lastErr error
	var capabilityErr error
	var sawSupportedCapability bool
	var capabilityErrorCode string
	var capabilityErrorMessage string
	var lastRetryAt time.Time
	var lastFailure FailureClassification
	capabilityPolicy := getCapabilityDegradationPolicy()
	execution := newOperationExecution(group, "images")
	metrics.execution, metrics.affinity = execution, iter.Affinity()
	execution.strictAffinity = iter.StrictAffinity()

	for iter.Next() {
		select {
		case <-ctx.Done():
			log.Debugf("request context canceled, stopping retry")
			savedErr = context.Canceled
			return
		default:
		}
		if err := execution.attemptError(time.Now()); err != nil {
			lastErr, lastFailure = err, relayBudgetAttemptResult(err).Failure
			break
		}

		item := iter.Item()

		// 获取通道
		channel, err := candidateSnapshot.Channel(item.ChannelID)
		if err != nil {
			log.Warnf("failed to get channel %d: %v", item.ChannelID, err)
			iter.Skip(item.ChannelID, 0, fmt.Sprintf("channel_%d", item.ChannelID), fmt.Sprintf("channel not found: %v", err))
			lastErr = err
			continue
		}
		if !channel.Enabled {
			iter.Skip(channel.ID, 0, channel.Name, "channel disabled")
			continue
		}

		decision := outbound.PlanRelayOperation(channel.Type, outbound.RelayOperationImages)
		decorateParamOverrideDecision(&decision, helper.InspectParamOverride(channel.ParamOverride), channelParamOverrideActive(channel))
		logRelayCapability(channel, item.ModelName, decision, capabilityPolicy)
		if reject, errorCode := evaluateCapabilityPolicy(decision, capabilityPolicy); reject {
			message := capabilityRejectionMessage(decision, channel.Type.String())
			iter.SkipWithCapability(channel.ID, 0, channel.Name, message, capabilityTrace(decision, capabilityPolicy, channel.Type.String()))
			capabilityErr = fmt.Errorf("capability rejected: %s", message)
			capabilityErrorCode = errorCode
			capabilityErrorMessage = message
			continue
		}
		sawSupportedCapability = true
		if !execution.canAttemptChannel(channel.ID, time.Now()) {
			iter.Skip(channel.ID, 0, channel.Name, "candidate channel budget exhausted")
			lastErr = newRelayBudgetError("candidate channel budget exhausted")
			lastFailure = relayBudgetAttemptResult(lastErr).Failure
			continue
		}

		excludedKeyIDs := make(map[int]struct{})
		usedKey, releaseKey := selectAndReserveRelayKey(iter, channel, excludedKeyIDs)
		if usedKey.ChannelKey == "" {
			if len(excludedKeyIDs) == 0 {
				iter.Skip(channel.ID, 0, channel.Name, "no available key")
			} else {
				iter.InvalidateCurrentPreference()
			}
			continue
		}

		for {
			log.Debugf("images request model %s, mode: %d, forwarding to channel: %s model: %s (attempt %d/%d, sticky=%t, stream=%t)",
				requestModel, group.Mode, channel.Name, item.ModelName,
				iter.Index()+1, iter.Len(), iter.IsSticky(), stream)

			span := iter.StartAttempt(channel.ID, usedKey.ID, channel.Name)
			span.SetCapability(capabilityTrace(decision, capabilityPolicy, channel.Type.String()))

			// 尝试一次转发
			var retryAt time.Time
			statusCode, written, usage, upstreamCT, fwdErr := imagesAttempt(ctx, endpoint, c, bc, isMultipart, boundary, jsonPayload, stream, channel, usedKey.ChannelKey, group.FirstTokenTimeOut, metrics, item.ModelName, hb, &retryAt, execution)
			releaseKey()
			if metrics.attemptCapture != nil {
				a := metrics.attemptCapture.snapshot()
				var status *int
				if a.Response != nil {
					status = a.Response.StatusCode
				}
				span.SetHTTPDetails(a.AttemptID, status, a.UpstreamRequestID)
			}

			// 更新 channel key 状态
			usedKey.StatusCode = statusCode
			usedKey.LastUseTimeStamp = time.Now().Unix()

			if fwdErr == nil {
				// ====== 成功 ======
				actualModel := strings.TrimSpace(metrics.ActualModel)
				if actualModel == "" {
					actualModel = item.ModelName
					metrics.ActualModel = actualModel
				}
				if usage != nil {
					metrics.SetUsageFromImages(actualModel, *usage)
				}
				_ = upstreamCT

				op.ChannelKeyUpdateWithDelta(usedKey, metrics.Stats.InputCost+metrics.Stats.OutputCost)

				span.End(model.AttemptSuccess, statusCode, "")

				// Channel 维度统计
				op.StatsChannelUpdate(channel.ID, model.StatsMetrics{
					WaitTime:       span.Duration().Milliseconds(),
					RequestSuccess: 1,
				})

				// 熔断器：记录成功
				balancer.RecordSuccess(channel.ID, usedKey.ID, item.ModelName)
				// Refresh affinity only after the complete image response succeeds.
				iter.RecordAffinity(channel.ID, usedKey.ID)

				savedSuccess = true
				return
			}

			// ====== 失败 ======
			failure := classifyRelayFailureContext(ctx, statusCode, fwdErr, retryAt)
			if !written && failure.Class != FailureClientCanceled {
				hb.Resume()
			}
			op.ChannelKeyUpdateWithDelta(usedKey, 0)
			span.SetFailure(string(failure.Class), failure.Retryable, failure.RetryAt, failure.Scope)
			span.End(model.AttemptFailed, statusCode, fwdErr.Error())

			// Channel 维度统计
			op.StatsChannelUpdate(channel.ID, model.StatsMetrics{
				WaitTime:      span.Duration().Milliseconds(),
				RequestFailed: 1,
			})

			// 熔断器：只记录可归因于上游的失败；请求错误和客户端取消不污染渠道状态。
			if failure.Record {
				retryAt = recordFailureAndResolveRetryAt(channel.ID, usedKey.ID, item.ModelName, failure, retryAt)
				failure.RetryAt = retryAt
			}

			if written {
				savedErr = fwdErr
				return
			}

			iter.InvalidateCurrentPreference()
			lastErr = fmt.Errorf("channel %s failed: %w", channel.Name, fwdErr)
			lastRetryAt = retryAt
			lastFailure = failure
			if failure.Class == FailureBudgetExceeded || failure.Class == FailureClientCanceled {
				break
			}
			if failure.Scope == "key" {
				excludedKeyIDs[usedKey.ID] = struct{}{}
				usedKey, releaseKey = selectAndReserveRelayKey(iter, channel, excludedKeyIDs)
				if usedKey.ChannelKey != "" {
					continue
				}
			}
			break
		}
		if lastFailure.Class == FailureBudgetExceeded || lastFailure.Class == FailureClientCanceled {
			break
		}
	}

	// 所有通道都失败
	finalErr := lastErr
	if !sawSupportedCapability && capabilityErr != nil {
		finalErr = capabilityErr
	}
	savedErr = finalErr
	hb.Hand()
	if !sawSupportedCapability && capabilityErrorCode != "" {
		if hb.Handoff() {
			hb.WriteSSEError(http.StatusBadRequest, capabilityErrorMessage)
		} else {
			resp.ErrorWithCode(c, http.StatusBadRequest, capabilityErrorCode, capabilityErrorMessage)
		}
		return
	}
	if lastFailure.Passthrough {
		if value := retryAfterHeaderValue(lastRetryAt, time.Now()); value != "" {
			c.Header("Retry-After", value)
		}
		status := lastFailure.StatusCode
		if status < 400 {
			status = http.StatusBadGateway
		}
		writeImagesFailure(c, hb, attemptResult{
			Err:        finalErr,
			StatusCode: status,
			RetryAt:    lastRetryAt,
			Failure:    lastFailure,
		}, finalErr)
		return
	}
	writeImagesFailure(c, hb, attemptResult{
		Err:        finalErr,
		StatusCode: lastFailure.StatusCode,
		RetryAt:    lastRetryAt,
		Failure:    lastFailure,
	}, finalErr)
}

func writeImagesFailure(c *gin.Context, hb *earlyHeartbeat, result attemptResult, err error) {
	responseError := protocolErrorForAttempt(result, err)
	if responseError == nil {
		responseError = relayProtocolError(http.StatusBadGateway, CodeRelayUpstreamFailed, "all channels failed")
	}
	if hb != nil && hb.Handoff() {
		hb.WriteSSEError(responseError.StatusCode, responseError.Detail.Message)
		return
	}
	resp.ErrorWithCode(c, responseError.StatusCode, responseError.Detail.Code, responseError.Detail.Message)
}
