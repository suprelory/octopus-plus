package checkin

import (
	"context"
	"errors"
	"sync"
)

var ErrServicesStopped = errors.New("check-in services are stopping")

// Tracks detached session exchanges, batches and notification workers as well
// as foreground work. Closing admission and cancellation are one transition;
// persistence that deliberately outlives cancellation still holds its slot.
type workLifecycle struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	active int
	done   chan struct{}
}

func newWorkLifecycle() *workLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &workLifecycle{ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

func (w *workLifecycle) begin(parent context.Context) (context.Context, func(), error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil, nil, ErrServicesStopped
	}
	w.active++
	w.mu.Unlock()
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(w.ctx, cancel)
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			stop()
			cancel()
			w.mu.Lock()
			defer w.mu.Unlock()
			w.active--
			if w.closed && w.active == 0 {
				close(w.done)
			}
		})
	}, nil
}

func (w *workLifecycle) close(ctx context.Context) error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		w.cancel()
		if w.active == 0 {
			close(w.done)
		}
	}
	w.mu.Unlock()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
