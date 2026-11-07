package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/utils/log"
)

type firstTokenBudget struct {
	ctx     context.Context
	timer   *time.Timer
	cancel  context.CancelCauseFunc
	mu      sync.Mutex
	stopped bool
	once    sync.Once
}

func (b *firstTokenBudget) stopTimer() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	b.stopped = true
	if b.timer == nil {
		return
	}
	b.timer.Stop()
}

func (b *firstTokenBudget) close() {
	if b == nil {
		return
	}
	b.once.Do(func() {
		b.stopTimer()
		if b.cancel != nil {
			b.cancel(context.Canceled)
		}
	})
}

func (ra *relayAttempt) attachFirstTokenBudget(req *http.Request) *http.Request {
	if req == nil {
		return req
	}
	ctx := ra.startFirstTokenBudget(req.Context())
	if ra == nil || ra.firstTokenBudget == nil {
		return req
	}
	return req.WithContext(ctx)
}

func (ra *relayAttempt) startFirstTokenBudget(parent context.Context) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	if ra != nil && ra.firstTokenBudget != nil {
		return ra.firstTokenBudget.ctx
	}
	timeout, cause, ok := ra.firstTokenBudgetSpec()
	if !ok {
		return parent
	}
	if timeout < 0 {
		timeout = 0
	}

	budget := newFirstTokenBudget(parent, timeout, cause)
	ra.firstTokenBudget = budget
	return budget.ctx
}

func newFirstTokenBudget(parent context.Context, timeout time.Duration, cause error) *firstTokenBudget {
	ctx, cancel := context.WithCancelCause(parent)
	budget := &firstTokenBudget{ctx: ctx, cancel: cancel}
	budget.timer = time.AfterFunc(timeout, func() {
		budget.mu.Lock()
		defer budget.mu.Unlock()
		if budget.stopped {
			return
		}
		cancel(cause)
	})
	return budget
}

func (ra *relayAttempt) firstTokenBudgetSpec() (time.Duration, error, bool) {
	if ra == nil || ra.internalRequest == nil || ra.internalRequest.Stream == nil || !*ra.internalRequest.Stream {
		return 0, nil, false
	}

	var timeout time.Duration
	cause := error(errFirstTokenTimeout)
	if ra.firstTokenTimeOutSec > 0 {
		timeout = time.Duration(ra.firstTokenTimeOutSec) * time.Second
	}
	if !ra.failoverDeadline.IsZero() {
		remaining := time.Until(ra.failoverDeadline)
		if remaining <= 0 || timeout <= 0 || remaining < timeout {
			timeout = remaining
			cause = errLocalRelayBudgetExceeded
		}
	}
	if timeout <= 0 && !errors.Is(cause, errLocalRelayBudgetExceeded) {
		return 0, nil, false
	}
	return timeout, cause, true
}

func (ra *relayAttempt) stopFirstTokenTimer() {
	if ra == nil || ra.firstTokenBudget == nil {
		return
	}
	ra.firstTokenBudget.stopTimer()
}

func (ra *relayAttempt) closeFirstTokenBudget() {
	if ra == nil || ra.firstTokenBudget == nil {
		return
	}
	ra.firstTokenBudget.close()
}

func (ra *relayAttempt) firstTokenTimeoutError() error {
	if ra == nil || ra.firstTokenTimeOutSec <= 0 {
		return errFirstTokenTimeout
	}
	return fmt.Errorf("%w (%ds)", errFirstTokenTimeout, ra.firstTokenTimeOutSec)
}

func (ra *relayAttempt) firstTokenTimeoutIfNeeded(ctx context.Context, err error) error {
	budgetCtx := context.Context(nil)
	if ra != nil && ra.firstTokenBudget != nil {
		budgetCtx = ra.firstTokenBudget.ctx
	}
	if isLocalRelayBudgetExceeded(ctx, err) || isLocalRelayBudgetExceeded(budgetCtx, err) {
		if ra != nil && ra.internalRequest != nil && ra.internalRequest.Stream != nil && *ra.internalRequest.Stream {
			log.Warnf("relay failover budget exceeded before the first semantic stream event")
			return newRelayBudgetError("failover timeout exceeded before first semantic stream event")
		}
		log.Warnf("relay failover budget exceeded before the upstream response completed")
		return newRelayBudgetError("failover timeout exceeded before upstream response completed")
	}
	if isFirstTokenTimeout(ctx, err) || isFirstTokenTimeout(budgetCtx, err) {
		if ra != nil && ra.firstTokenTimeOutSec > 0 {
			log.Warnf("first token timeout (%ds), switching channel", ra.firstTokenTimeOutSec)
		}
		return ra.firstTokenTimeoutError()
	}
	return nil
}

type closeWithFuncReadCloser struct {
	io.ReadCloser
	onClose func()
}

func (c *closeWithFuncReadCloser) Close() error {
	err := c.ReadCloser.Close()
	if c.onClose != nil {
		c.onClose()
	}
	return err
}
