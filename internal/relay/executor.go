package relay

import (
	"fmt"
	"net/http"
	"time"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
	"github.com/suprelory/octopus-plus/internal/transformer"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

type relayExecutor struct {
	request *relayRequest
}

type relayOutcome struct {
	result  attemptResult
	attempt *relayRequest
	channel *dbmodel.Channel
	key     dbmodel.ChannelKey
}

type retryAction uint8

const (
	retryStop retryAction = iota
	retrySameCandidate
	retryNextCandidate
)

func decideRetry(result attemptResult, sameCandidateAvailable, fallbackAvailable bool) retryAction {
	if result.Success || result.Written || result.Canceled || result.ResetConversation || result.Failure.Class == FailureBudgetExceeded || isCostBudgetError(result.Err) {
		return retryStop
	}
	if result.FirstTokenTimeout || (result.Failure.Class == FailureRateLimit && fallbackAvailable) ||
		!result.Failure.Retryable || !sameCandidateAvailable {
		return retryNextCandidate
	}
	return retrySameCandidate
}

// run owns candidate selection and failure policy for both downstream protocols.
// Protocol writers and conversation recovery settle the returned outcome.
func (r *relayExecutor) run() relayOutcome {
	req := r.request
	iter, execution, ctx := req.iter, req.execution, req.requestContext()
	req.metrics.execution, req.metrics.affinity = execution, iter.Affinity()
	execution.strictAffinity = iter.StrictAffinity()
	defer iter.Close()
	var outcome relayOutcome
	var capabilityErr error
	var capabilityResult attemptResult
	var sawSupportedCapability bool
	for iter.Next() {
		if err := contextError(ctx); err != nil {
			outcome.result = attemptResult{Canceled: true, Err: err}
			return outcome
		}
		if err := execution.attemptError(time.Now()); err != nil {
			outcome.result = relayBudgetAttemptResult(err)
			return outcome
		}
		item := iter.Item()
		if _, limited := execution.rateLimitedChannels[item.ChannelID]; limited {
			iter.Skip(item.ChannelID, 0, fmt.Sprintf("channel_%d", item.ChannelID), "channel rate limited earlier in this request")
			continue
		}
		channel, err := req.candidateSnapshot.Channel(item.ChannelID)
		if err != nil {
			iter.Skip(item.ChannelID, 0, fmt.Sprintf("channel_%d", item.ChannelID), fmt.Sprintf("channel not found: %v", err))
			outcome.result.Err = err
			continue
		}
		if !channel.Enabled {
			iter.Skip(channel.ID, 0, channel.Name, "channel disabled")
			continue
		}
		adapter := transformer.Outbound(channel.Type)
		if adapter == nil {
			iter.Skip(channel.ID, 0, channel.Name, fmt.Sprintf("unsupported channel type: %d", channel.Type))
			continue
		}
		decision := planRelayCapability(req, channel, adapter, item.ModelName)
		logRelayCapability(channel, item.ModelName, decision, req.capabilityPolicy)
		if reject, code := evaluateCapabilityPolicy(decision, req.capabilityPolicy); reject {
			message := capabilityRejectionMessage(decision, channel.Type.String())
			iter.SkipWithCapability(channel.ID, 0, channel.Name, message, capabilityTrace(decision, req.capabilityPolicy, channel.Type.String()))
			candidateErr := fmt.Errorf("capability rejected: %s", message)
			candidateResult := attemptResult{Err: candidateErr, StatusCode: http.StatusBadRequest, ProtocolError: relayProtocolError(http.StatusBadRequest, code, message)}
			capabilityResult, capabilityErr = preferCapabilityRejection(capabilityErr, capabilityResult, candidateErr, candidateResult)
			continue
		}
		sawSupportedCapability = true
		if !execution.canAttemptChannel(channel.ID, time.Now()) {
			iter.Skip(channel.ID, 0, channel.Name, "candidate channel budget exhausted")
			outcome.result = relayBudgetAttemptResult(newRelayBudgetError("candidate channel budget exhausted"))
			continue
		}
		excludedKeys := make(map[int]struct{})
		for _, key := range channel.Keys {
			if execution.candidateAttempts[relayCandidate{channel.ID, key.ID, item.ModelName}] >= execution.maxSameChannelAttempts {
				excludedKeys[key.ID] = struct{}{}
			}
		}
		budgetExcluded := len(excludedKeys) > 0
		key, release := selectAndReserveRelayKey(iter, channel, excludedKeys)
		if key.ChannelKey == "" {
			if budgetExcluded {
				iter.Skip(channel.ID, 0, channel.Name, "candidate attempt budget exhausted")
				outcome.result = relayBudgetAttemptResult(newRelayBudgetError("candidate attempt budget exhausted"))
			} else if len(excludedKeys) == 0 {
				iter.Skip(channel.ID, 0, channel.Name, "no available key")
			} else {
				iter.InvalidateCurrentPreference()
			}
			continue
		}
		for {
			result, attempt := r.runChannelAttempts(channel, key, release, item.ModelName, decision)
			outcome.result, outcome.channel, outcome.key = result, channel, key
			if attempt != nil {
				outcome.attempt = attempt
			}
			// Health accounting is independent of whether a response can be retried.
			if !result.Success && !result.Canceled && !result.ResetConversation && result.Failure.Record {
				result.RetryAt = recordFailureAndResolveRetryAt(channel.ID, key.ID, item.ModelName, result.Failure, result.RetryAt)
				result.Failure.RetryAt = result.RetryAt
				outcome.result = result
				if failureCircuitKind(result.Failure) == balancer.FailureTransient {
					maybeLearnManagedRoute(ctx, channel.ID, item.ModelName, req.inboundType, result.Err)
				}
			}
			if mayRotateKey(result, currentPreviousResponseID(req.internalRequest) != "") {
				excludedKeys[key.ID] = struct{}{}
				if err := execution.attemptError(time.Now()); err != nil {
					outcome.result = relayBudgetAttemptResult(err)
					return outcome
				}
				nextKey, nextRelease := selectAndReserveRelayKey(iter, channel, excludedKeys)
				if nextKey.ChannelKey != "" {
					key, release = nextKey, nextRelease
					continue
				}
			}
			if decideRetry(result, false, false) == retryStop {
				return outcome
			}
			// Native response IDs belong to their upstream session. Only transport or
			// session failures may request replay after same-candidate recovery stops.
			if requiresUpstreamWSContinuation(req.internalRequest) {
				if isContinuationTransportFailure(result.Err) {
					outcome.result.ResetConversation = true
					outcome.result.StatusCode = http.StatusConflict
					outcome.result.Err = fmt.Errorf("upstream continuation transport unavailable; please restart the conversation: %w", result.Err)
				}
				return outcome
			}
			iter.InvalidateCurrentPreference()
			break
		}
	}
	outcome.result, _ = resolveFinalAttemptResult(sawSupportedCapability, outcome.result.Err, outcome.result, capabilityErr, capabilityResult)
	return outcome
}

func (r *relayExecutor) runChannelAttempts(channel *dbmodel.Channel, key dbmodel.ChannelKey, release func(), modelName string, decision outbound.CapabilityDecision) (result attemptResult, lastAttempt *relayRequest) {
	defer release()
	req, execution := r.request, r.request.execution
	ctx := req.requestContext()
	candidate := relayCandidate{channel.ID, key.ID, modelName}
	isStream := req.internalRequest.Stream != nil && *req.internalRequest.Stream
	recovery := upstreamRecoveryNone
	for attemptNum := 0; attemptNum < execution.maxSameChannelAttempts; attemptNum++ {
		if err := execution.attemptError(time.Now()); err != nil {
			return relayBudgetAttemptResult(err), lastAttempt
		}
		if attemptNum > 0 && recovery == upstreamRecoveryNone {
			delay := computeBackoffUntil(attemptNum, result.RetryAt)
			if err := execution.wait(ctx, delay); err != nil {
				if isLocalRelayBudgetError(err) {
					return relayBudgetAttemptResult(err), lastAttempt
				}
				return attemptResult{Canceled: true, Err: err}, lastAttempt
			}
		}
		attemptCtx, cancel := ctx, func() {}
		if !isStream {
			attemptCtx, cancel = execution.budget.attemptContext(ctx)
		}
		attemptRequest, err := newAttemptRelayRequest(req, attemptCtx, modelName)
		if err != nil {
			cancel()
			classified := classifyLocalRelayError(FailureConfiguration, fmt.Errorf("failed to prepare relay attempt: %w", err))
			return attemptResult{
				Err:           classified,
				StatusCode:    http.StatusInternalServerError,
				Failure:       FailureClassification{Class: FailureConfiguration, StatusCode: http.StatusInternalServerError},
				ProtocolError: relayProtocolError(http.StatusInternalServerError, CodeRelayConfiguration, classified.Error()),
			}, lastAttempt
		}
		attempt := &relayAttempt{
			relayRequest:           attemptRequest,
			outAdapter:             transformer.Outbound(channel.Type),
			channel:                channel,
			usedKey:                key,
			firstTokenTimeOutSec:   execution.firstTokenTimeout,
			failoverDeadline:       execution.deadline(),
			emptyResponseDetection: execution.emptyResponseDetection,
			capabilityDecision:     decision,
			preparedRequest:        req.capabilityPlanner.preparedFor(channel, modelName, decision),
			transportRecovery:      recovery,
		}
		result = attempt.attempt()
		cancel()
		if !result.Success && !result.Canceled && !result.Written {
			// Closing a failed stream cancels its attempt budget. The shared
			// request heartbeat must resume using the still-live inbound context.
			req.heartbeat.Resume()
		}
		lastAttempt = attemptRequest
		fallback := !requiresUpstreamWSContinuation(req.internalRequest) &&
			!result.Written && result.Failure.Class == FailureRateLimit &&
			req.iter.HasRemainingDifferentCandidateMatching(channel.ID, execution.rateLimitedChannels, r.candidateAvailable)
		action := decideRetry(result, result.Failure.Scope != "key" && execution.candidateAttempts[candidate] < execution.maxSameChannelAttempts, fallback)
		if action != retrySameCandidate {
			if fallback && action == retryNextCandidate {
				execution.rateLimitedChannels[channel.ID] = struct{}{}
				log.Infof("channel %s rate limited; switching to a remaining channel", channel.Name)
			}
			return result, lastAttempt
		}
		recovery = result.Recovery
	}
	return result, lastAttempt
}

func (r *relayExecutor) candidateAvailable(item dbmodel.GroupItem) bool {
	req := r.request
	if !req.execution.canAttemptChannel(item.ChannelID, time.Now()) {
		return false
	}
	channel, err := req.candidateSnapshot.Channel(item.ChannelID)
	if err != nil || !channel.Enabled {
		return false
	}
	adapter := transformer.Outbound(channel.Type)
	if adapter == nil {
		return false
	}
	decision := planRelayCapability(req, channel, adapter, item.ModelName)
	if reject, _ := evaluateCapabilityPolicy(decision, req.capabilityPolicy); reject {
		return false
	}
	for _, key := range channel.Keys {
		candidate := relayCandidate{channel.ID, key.ID, item.ModelName}
		if key.Enabled && key.ChannelKey != "" && req.execution.candidateAttempts[candidate] < req.execution.maxSameChannelAttempts &&
			balancer.CanAttempt(channel.ID, key.ID, item.ModelName) {
			return true
		}
	}
	return false
}
