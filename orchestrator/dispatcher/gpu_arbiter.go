package dispatcher

import (
	"context"
	"sync"
)

type gpuArbiterRole uint8

const (
	gpuRoleGeneric gpuArbiterRole = iota
	gpuRoleDemucs
	gpuRoleFeatureBatch
)

type gpuArbiterWaiter struct {
	ch         chan struct{}
	role       gpuArbiterRole
	usedCredit bool
}

type gpuArbiterQueuedHookKey struct{}

func withGPUArbiterQueuedHook(ctx context.Context, onQueued func()) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if onQueued == nil {
		return ctx
	}
	return context.WithValue(ctx, gpuArbiterQueuedHookKey{}, onQueued)
}

func invokeGPUArbiterQueuedHook(ctx context.Context) {
	if ctx == nil {
		return
	}
	if fn, ok := ctx.Value(gpuArbiterQueuedHookKey{}).(func()); ok && fn != nil {
		fn()
	}
}

// GPUArbiter provides a cancellation-aware GPU queue with bounded post-Demucs
// FeatureBatch turn priority (at most one FeatureBatch turn ahead of the oldest
// waiting Demucs task to prevent self-deadlock without starving Demucs).
type GPUArbiter struct {
	mu                      sync.Mutex
	waiters                 []*gpuArbiterWaiter
	owner                   bool
	postDemucsFeatureCredit bool
}

// NewGPUArbiter creates a new bounded-turn GPU arbiter.
func NewGPUArbiter() *GPUArbiter {
	return &GPUArbiter{
		waiters: make([]*gpuArbiterWaiter, 0),
		owner:   false,
	}
}

// WaiterCount returns the number of currently queued waiters.
func (a *GPUArbiter) WaiterCount() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.waiters)
}

// Acquire requests generic GPU ownership. It returns nil if acquired, or ctx.Err() if canceled.
func (a *GPUArbiter) Acquire(ctx context.Context) error {
	return a.acquireWithRole(ctx, gpuRoleGeneric)
}

// AcquireDemucs requests GPU ownership for a Demucs separation stage.
func (a *GPUArbiter) AcquireDemucs(ctx context.Context) error {
	return a.acquireWithRole(ctx, gpuRoleDemucs)
}

// AcquireFeatureBatch requests GPU ownership for post-Demucs GPU feature extraction.
func (a *GPUArbiter) AcquireFeatureBatch(ctx context.Context) error {
	return a.acquireWithRole(ctx, gpuRoleFeatureBatch)
}

// RegisterPostDemucsFeatureTurn grants at most one FeatureBatch waiter a bounded
// turn ahead of waiting Demucs tasks on the next Release.
func (a *GPUArbiter) RegisterPostDemucsFeatureTurn() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.owner {
		a.postDemucsFeatureCredit = true
	}
	a.mu.Unlock()
}

func (a *GPUArbiter) acquireWithRole(ctx context.Context, role gpuArbiterRole) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.Lock()
	if !a.owner {
		a.owner = true
		if role == gpuRoleDemucs {
			a.postDemucsFeatureCredit = false
		}
		a.mu.Unlock()
		invokeGPUArbiterQueuedHook(ctx)
		return nil
	}

	w := &gpuArbiterWaiter{ch: make(chan struct{}), role: role}
	a.waiters = append(a.waiters, w)
	a.mu.Unlock()
	invokeGPUArbiterQueuedHook(ctx)

	select {
	case <-w.ch:
		if err := ctx.Err(); err != nil {
			// Carry unused credit into the next handoff if the promoted FeatureBatch canceled at handoff.
			a.mu.Lock()
			if w.usedCredit {
				a.postDemucsFeatureCredit = true
			}
			a.handOffLocked()
			a.mu.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		a.mu.Lock()
		defer a.mu.Unlock()
		// Attempt to remove this waiter from the queue.
		for i, queued := range a.waiters {
			if queued == w {
				a.waiters = append(a.waiters[:i], a.waiters[i+1:]...)
				return ctx.Err()
			}
		}
		// If the waiter is not in the queue, ownership was already transferred to it.
		// Carry unused credit into the next handoff if this waiter was selected by credit.
		if w.usedCredit {
			a.postDemucsFeatureCredit = true
		}
		a.handOffLocked()
		return ctx.Err()
	}
}

// Release yields GPU ownership to the next eligible waiter, or frees the GPU if empty.
func (a *GPUArbiter) Release() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.handOffLocked()
}

func (a *GPUArbiter) handOffLocked() {
	if len(a.waiters) == 0 {
		a.owner = false
		a.postDemucsFeatureCredit = false
		return
	}

	if a.postDemucsFeatureCredit {
		for i, w := range a.waiters {
			if w.role == gpuRoleFeatureBatch {
				w.usedCredit = true
				a.postDemucsFeatureCredit = false
				a.waiters = append(a.waiters[:i], a.waiters[i+1:]...)
				close(w.ch)
				return
			}
		}
	}

	next := a.waiters[0]
	a.waiters = a.waiters[1:]
	if next.role == gpuRoleDemucs {
		a.postDemucsFeatureCredit = false
	}
	close(next.ch)
}
