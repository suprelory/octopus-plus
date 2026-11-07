package relay

import (
	"context"
	"fmt"
	"time"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

const (
	defaultRelayMaxChannelAttempts     = 4
	defaultRelayMaxTotalAttempts       = 12
	defaultRelayFailoverTimeoutSeconds = 300
	defaultSameChannelMaxAttempts      = 3
)

// relayFailoverBudget bounds the pre-commit work performed for one logical relay
// request. A completed streaming response is intentionally not tied to this
// deadline: the deadline only applies until the first semantic stream event.
type relayFailoverBudget struct {
	maxChannelAttempts int
	maxTotalAttempts   int
	totalAttempts      int
	visitedChannels    map[int]struct{}
	deadline           time.Time
}

func newRelayFailoverBudget(now time.Time) *relayFailoverBudget {
	maxChannels := relayBudgetSetting(
		dbmodel.SettingKeyRelayMaxChannelAttempts,
		defaultRelayMaxChannelAttempts,
		1,
		64,
	)
	maxTotal := relayBudgetSetting(
		dbmodel.SettingKeyRelayMaxTotalAttempts,
		defaultRelayMaxTotalAttempts,
		1,
		256,
	)
	timeoutSeconds := relayBudgetSetting(
		dbmodel.SettingKeyRelayFailoverTimeoutSeconds,
		defaultRelayFailoverTimeoutSeconds,
		1,
		3600,
	)

	return &relayFailoverBudget{
		maxChannelAttempts: maxChannels,
		maxTotalAttempts:   maxTotal,
		visitedChannels:    make(map[int]struct{}, maxChannels),
		deadline:           now.Add(time.Duration(timeoutSeconds) * time.Second),
	}
}

// Operation overrides inherit the global limits when zero or absent.
func newOperationExecution(group dbmodel.Group, operation string) *relayExecution {
	e := newRelayExecution(group, true)
	prefix := "relay_" + operation + "_"
	e.budget.maxChannelAttempts = relayBudgetSetting(dbmodel.SettingKey(prefix+"max_channel_attempts"), e.budget.maxChannelAttempts, 1, 64)
	e.budget.maxTotalAttempts = relayBudgetSetting(dbmodel.SettingKey(prefix+"max_total_attempts"), e.budget.maxTotalAttempts, 1, 256)
	if seconds := relayBudgetSetting(dbmodel.SettingKey(prefix+"timeout_seconds"), 0, 1, 3600); seconds > 0 {
		e.budget.deadline = time.Now().Add(time.Duration(seconds) * time.Second)
	}
	return e
}

func relayBudgetSetting(key dbmodel.SettingKey, fallback, minValue, maxValue int) int {
	value, err := op.SettingGetInt(key)
	if err != nil || value < minValue || value > maxValue {
		return fallback
	}
	return value
}

func sameChannelMaxAttempts(retryEnabled bool, configured int) int {
	if !retryEnabled {
		return 1
	}
	if configured <= 0 {
		return defaultSameChannelMaxAttempts
	}
	return configured
}

func (b *relayFailoverBudget) reserveChannel(channelID int, now time.Time) error {
	if err := b.timeError(now); err != nil {
		return err
	}
	if _, visited := b.visitedChannels[channelID]; visited {
		return nil
	}
	if len(b.visitedChannels) >= b.maxChannelAttempts {
		return newRelayBudgetError(fmt.Sprintf("candidate channel limit reached (%d)", b.maxChannelAttempts))
	}
	if b.visitedChannels == nil {
		b.visitedChannels = make(map[int]struct{}, b.maxChannelAttempts)
	}
	b.visitedChannels[channelID] = struct{}{}
	return nil
}

func (b *relayFailoverBudget) reserveAttempt(now time.Time) error {
	if err := b.attemptError(now); err != nil {
		return err
	}
	b.totalAttempts++
	return nil
}

func (b *relayFailoverBudget) attemptError(now time.Time) error {
	if err := b.timeError(now); err != nil {
		return err
	}
	if b == nil {
		return nil
	}
	if b.totalAttempts >= b.maxTotalAttempts {
		return newRelayBudgetError(fmt.Sprintf("upstream attempt limit reached (%d)", b.maxTotalAttempts))
	}
	return nil
}

func (b *relayFailoverBudget) canAttemptChannel(channelID int, now time.Time) bool {
	if b == nil || b.timeError(now) != nil {
		return false
	}
	if b.totalAttempts >= b.maxTotalAttempts {
		return false
	}
	if _, visited := b.visitedChannels[channelID]; visited {
		return true
	}
	return len(b.visitedChannels) < b.maxChannelAttempts
}

func (b *relayFailoverBudget) timeError(now time.Time) error {
	if b == nil || b.deadline.IsZero() || now.Before(b.deadline) {
		return nil
	}
	return newRelayBudgetError("failover timeout exceeded")
}

func (b *relayFailoverBudget) wait(ctx context.Context, delay time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := b.timeError(time.Now()); err != nil {
		return err
	}
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return contextError(ctx)
		default:
			return nil
		}
	}

	wakeAt := time.Now().Add(delay)
	budgetLimited := b != nil && !b.deadline.IsZero() && wakeAt.After(b.deadline)
	if budgetLimited {
		delay = time.Until(b.deadline)
		if delay <= 0 {
			return newRelayBudgetError("failover timeout exceeded during retry backoff")
		}
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return contextError(ctx)
	case <-timer.C:
		if budgetLimited {
			return newRelayBudgetError("failover timeout exceeded during retry backoff")
		}
		return b.timeError(time.Now())
	}
}

func (b *relayFailoverBudget) attemptContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if b == nil || b.deadline.IsZero() {
		return parent, func() {}
	}
	return context.WithDeadlineCause(parent, b.deadline, errLocalRelayBudgetExceeded)
}

func (b *relayFailoverBudget) precommitDeadline() time.Time {
	if b == nil {
		return time.Time{}
	}
	return b.deadline
}

func newRelayBudgetError(reason string) error {
	if reason == "" {
		return errLocalRelayBudgetExceeded
	}
	return fmt.Errorf("%w: %s", errLocalRelayBudgetExceeded, reason)
}

func relayBudgetAttemptResult(err error) attemptResult {
	if err == nil {
		err = errLocalRelayBudgetExceeded
	}
	return attemptResult{
		Err:        err,
		StatusCode: 0,
		Failure: FailureClassification{
			Class:      FailureBudgetExceeded,
			StatusCode: 0,
		},
		ProtocolError: relayProtocolError(
			504,
			CodeRelayTimeout,
			"relay failover budget exceeded",
		),
	}
}
