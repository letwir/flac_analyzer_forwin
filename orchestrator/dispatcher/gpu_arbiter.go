package dispatcher

import (
	"context"
	"sync"
)

// GPUArbiter provides a FIFO, cancellation-aware queue for GPU access.
type GPUArbiter struct {
	mu      sync.Mutex
	waiters []chan struct{}
	owner   bool
}

// NewGPUArbiter creates a new FIFO GPU arbiter.
func NewGPUArbiter() *GPUArbiter {
	return &GPUArbiter{
		waiters: make([]chan struct{}, 0),
		owner:   false,
	}
}

// Acquire requests GPU ownership. It returns nil if acquired, or ctx.Err() if canceled.
func (a *GPUArbiter) Acquire(ctx context.Context) error {
	a.mu.Lock()
	if !a.owner {
		a.owner = true
		a.mu.Unlock()
		return nil
	}

	ch := make(chan struct{})
	a.waiters = append(a.waiters, ch)
	a.mu.Unlock()

	select {
	case <-ch:
		if err := ctx.Err(); err != nil {
			// Release a grant that raced with cancellation so the next waiter can proceed.
			a.Release()
			return err
		}
		return nil
	case <-ctx.Done():
		a.mu.Lock()
		defer a.mu.Unlock()
		// Attempt to remove this waiter from the queue.
		for i, w := range a.waiters {
			if w == ch {
				a.waiters = append(a.waiters[:i], a.waiters[i+1:]...)
				return ctx.Err()
			}
		}
		// If the waiter is not in the queue, ownership was already transferred to it.
		// Since we are canceling, we must pass ownership to the next waiter or release it.
		if len(a.waiters) > 0 {
			next := a.waiters[0]
			a.waiters = a.waiters[1:]
			close(next)
		} else {
			a.owner = false
		}
		return ctx.Err()
	}
}

// Release yields GPU ownership to the oldest live waiter, or frees the GPU if the queue is empty.
func (a *GPUArbiter) Release() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.waiters) > 0 {
		next := a.waiters[0]
		a.waiters = a.waiters[1:]
		close(next)
	} else {
		a.owner = false
	}
}
