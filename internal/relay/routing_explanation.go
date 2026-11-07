package relay

import (
	"context"
	"errors"
	"time"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
)

func (e *relayExecution) routingSummary(success bool, err error) *dbmodel.RoutingSummary {
	if e == nil || e.budget == nil {
		return nil
	}
	summary := &dbmodel.RoutingSummary{
		AttemptsUsed: e.budget.totalAttempts, AttemptLimit: e.budget.maxTotalAttempts,
		ChannelsUsed: len(e.budget.visitedChannels), ChannelLimit: e.budget.maxChannelAttempts,
		RemainingMillis: max(0, time.Until(e.deadline()).Milliseconds()), Committed: e.committed.Load(),
	}
	switch {
	case success:
		summary.StopReason = "success"
	case isLocalRelayBudgetError(err):
		summary.StopReason = "budget_exceeded"
	case errors.Is(err, context.Canceled):
		summary.StopReason = "client_canceled"
	case err != nil && needsConversationRestart(relayErrorMessage(err)):
		summary.StopReason = "continuation_unavailable"
	case e.committed.Load():
		summary.StopReason = "response_committed"
	case err != nil:
		summary.StopReason = "candidates_exhausted"
	}
	return summary
}

func explainRouting(attempts []dbmodel.ChannelAttempt, e *relayExecution, affinity balancer.AffinityOptions, success bool, err error) []dbmodel.ChannelAttempt {
	if len(attempts) == 0 || e == nil {
		return attempts
	}
	summary := e.routingSummary(success, err)
	if summary == nil {
		return attempts
	}
	result := append([]dbmodel.ChannelAttempt(nil), attempts...)
	summary.AffinityMode, summary.AffinitySource = affinity.Mode, affinity.Source
	if !success && summary.StopReason == "candidates_exhausted" && e.strictAffinity {
		summary.StopReason = "strict_affinity_unavailable"
	}
	if summary.StopReason == "" {
		summary.StopReason = "candidates_exhausted"
	}
	if !success && result[len(result)-1].FailureClass == string(FailureClientCanceled) {
		summary.StopReason = "client_canceled"
	}
	result[len(result)-1].Routing = summary
	return result
}
