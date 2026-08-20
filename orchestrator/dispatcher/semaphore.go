// Mor: (Limit × State) -> SemaphoreAcquisition
// Functor: f_sem ∘ g_ctx
// Semantics: Category: Resizable Lock-free Channel Notification Semaphore
package dispatcher

import (
	"context"
	"sync"
)

// DynamicSemaphore provides a thread-safe, dynamically resizable semaphore.
// It allows adjusting the concurrency limit at runtime without deadlocks,
// and supports deterministic context-aware cancellation.
type DynamicSemaphore struct {
	mu       sync.Mutex
	notifyCh chan struct{}
	limit    int
	inUse    int
}

// NewDynamicSemaphore creates a new DynamicSemaphore with the specified initial limit.
func NewDynamicSemaphore(initialLimit int) *DynamicSemaphore {
	if initialLimit <= 0 {
		initialLimit = 1
	}
	return &DynamicSemaphore{
		notifyCh: make(chan struct{}),
		limit:    initialLimit,
		inUse:    0,
	}
}

func (s *DynamicSemaphore) notifyAllLocked() {
	close(s.notifyCh)
	s.notifyCh = make(chan struct{})
}

// Acquire blocks until a slot is available under the current limit, then consumes one slot.
func (s *DynamicSemaphore) Acquire() {
	for {
		s.mu.Lock()
		if s.inUse < s.limit {
			s.inUse++
			s.mu.Unlock()
			return
		}
		ch := s.notifyCh
		s.mu.Unlock()
		<-ch
	}
}

// AcquireWithContext blocks until a slot is available or the context is cancelled.
func (s *DynamicSemaphore) AcquireWithContext(ctx context.Context) error {
	for {
		s.mu.Lock()
		if s.inUse < s.limit {
			s.inUse++
			s.mu.Unlock()
			return nil
		}
		ch := s.notifyCh
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

// Release frees one slot and notifies waiting goroutines.
func (s *DynamicSemaphore) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inUse > 0 {
		s.inUse--
	}
	s.notifyAllLocked()
}

// SetLimit updates the maximum allowed concurrent slots and notifies waiting goroutines.
func (s *DynamicSemaphore) SetLimit(newLimit int) {
	if newLimit <= 0 {
		newLimit = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limit = newLimit
	s.notifyAllLocked()
}

// GetLimit returns the current concurrency limit.
func (s *DynamicSemaphore) GetLimit() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.limit
}

// GetInUse returns the number of currently active slots.
func (s *DynamicSemaphore) GetInUse() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inUse
}
