package relay

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

const replayRecoveryTimeout = 15 * time.Second

type relayCandidate struct {
	channelID int
	keyID     int
	model     string
}

// relayExecution survives transport changes and conversation recovery. Each
// HTTP request or WS response.create owns one instance, never the WS session.
type relayExecution struct {
	budget                 *relayFailoverBudget
	replayBudget           *relayFailoverBudget
	committed              atomic.Bool
	maxSameChannelAttempts int
	firstTokenTimeout      int
	emptyResponseDetection bool
	candidateAttempts      map[relayCandidate]int
	rateLimitedChannels    map[int]struct{}
	previousAttempts       []dbmodel.ChannelAttempt
	strictAffinity         bool
}

func newRelayExecution(group dbmodel.Group, emptyResponseDetection bool) *relayExecution {
	return &relayExecution{
		budget:                 newRelayFailoverBudget(time.Now()),
		maxSameChannelAttempts: sameChannelMaxAttempts(group.RetryEnabled, group.MaxRetries),
		firstTokenTimeout:      group.FirstTokenTimeOut,
		emptyResponseDetection: emptyResponseDetection,
		candidateAttempts:      make(map[relayCandidate]int),
		rateLimitedChannels:    make(map[int]struct{}),
	}
}

func (e *relayExecution) beginReplay(now time.Time) error {
	if e.committed.Load() {
		return fmt.Errorf("cannot replay a committed response")
	}
	if e.replayBudget != nil {
		return fmt.Errorf("conversation recovery already attempted")
	}
	if err := e.attemptError(now); err != nil {
		return err
	}
	e.replayBudget = &relayFailoverBudget{
		maxChannelAttempts: 3,
		maxTotalAttempts:   e.budget.maxTotalAttempts,
		visitedChannels:    make(map[int]struct{}),
		deadline:           now.Add(replayRecoveryTimeout),
	}
	return nil
}

func (e *relayExecution) attemptError(now time.Time) error {
	if err := e.budget.attemptError(now); err != nil {
		return err
	}
	return e.replayBudget.attemptError(now)
}

func (e *relayExecution) canAttemptChannel(channelID int, now time.Time) bool {
	return e.budget.canAttemptChannel(channelID, now) &&
		(e.replayBudget == nil || e.replayBudget.canAttemptChannel(channelID, now))
}

func (e *relayExecution) deadline() time.Time {
	deadline := e.budget.precommitDeadline()
	if e.replayBudget != nil && (deadline.IsZero() || e.replayBudget.deadline.Before(deadline)) {
		deadline = e.replayBudget.deadline
	}
	return deadline
}

func (e *relayExecution) wait(ctx context.Context, delay time.Duration) error {
	if err := e.attemptError(time.Now()); err != nil {
		return err
	}
	budget := relayFailoverBudget{deadline: e.deadline()}
	return budget.wait(ctx, delay)
}

// reserveSubmission is called immediately before Do/SendRaw. Dial-only failures
// do not consume generation quota; every send, including a failed send, does.
func (ra *relayAttempt) reserveSubmission(ctx context.Context) error {
	if err := ra.execution.reserveSubmission(ctx, relayCandidate{ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model}); err != nil {
		return err
	}
	reservation := op.APIKeyCostReservationFromContext(ctx)
	if reservation.Limited() {
		if !ra.costChecked {
			return newCostBudgetError("request cost was not checked before submission")
		}
		if err := reservation.ReserveAttempt(ra.estimatedCost); err != nil {
			return newCostBudgetError("request and retry costs exceed the remaining API key budget")
		}
	}
	return nil
}

// reserveSubmission is shared by text, Images and Compact. Only actual sends
// consume the budget; validation and skipped candidates do not.
func (e *relayExecution) reserveSubmission(ctx context.Context, candidate relayCandidate) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if e == nil {
		return nil
	}
	if e.committed.Load() {
		return fmt.Errorf("cannot resend a committed response")
	}
	now := time.Now()
	if err := e.attemptError(now); err != nil {
		return err
	}
	if !e.canAttemptChannel(candidate.channelID, now) {
		return newRelayBudgetError("candidate channel limit reached")
	}
	if e.candidateAttempts[candidate] >= e.maxSameChannelAttempts {
		return newRelayBudgetError("same-channel attempt limit reached")
	}
	// All checks precede mutation. Attempts for one logical request run serially.
	_ = e.budget.reserveChannel(candidate.channelID, now)
	_ = e.budget.reserveAttempt(now)
	if e.replayBudget != nil {
		_ = e.replayBudget.reserveChannel(candidate.channelID, now)
		_ = e.replayBudget.reserveAttempt(now)
	}
	e.candidateAttempts[candidate]++
	return nil
}

func (r *relayRequest) responseCommitted() bool {
	return r.streamPayloadWritten.Load() || (r.execution != nil && r.execution.committed.Load())
}

func (ra *relayAttempt) commitResponse() {
	if ra.metrics != nil {
		ra.metrics.capture.bindDownstream(ra.capture)
	}
	ra.streamPayloadWritten.Store(true)
	if ra.execution != nil {
		ra.execution.committed.Store(true)
	}
	ra.stopFirstTokenTimer()
}

func (r *relayRequest) attempts() []dbmodel.ChannelAttempt {
	if r.execution == nil || len(r.execution.previousAttempts) == 0 {
		return r.iter.Attempts()
	}
	attempts := append([]dbmodel.ChannelAttempt(nil), r.execution.previousAttempts...)
	attempts = append(attempts, r.iter.Attempts()...)
	for i := range attempts {
		attempts[i].AttemptNum = i + 1
	}
	return attempts
}
