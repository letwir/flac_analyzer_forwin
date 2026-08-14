package dispatcher

import (
	"sync"
)

// DynamicSemaphore provides a thread-safe, dynamically resizable semaphore.
// It allows adjusting the concurrency limit at runtime without deadlocks.
type DynamicSemaphore struct {
	mu    sync.Mutex
	cond  *sync.Cond
	limit int
	inUse int
}

// NewDynamicSemaphore creates a new DynamicSemaphore with the specified initial limit.
func NewDynamicSemaphore(initialLimit int) *DynamicSemaphore {
	if initialLimit <= 0 {
		initialLimit = 1
	}
	s := &DynamicSemaphore{
		limit: initialLimit,
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Acquire blocks until a slot is available under the current limit, then consumes one slot.
func (s *DynamicSemaphore) Acquire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.inUse >= s.limit {
		s.cond.Wait()
	}
	s.inUse++
}

// Release frees one slot and broadcasts to wake up any waiting goroutines.
func (s *DynamicSemaphore) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inUse > 0 {
		s.inUse--
	}
	s.cond.Broadcast()
}

// SetLimit updates the maximum allowed concurrent slots and notifies waiting goroutines.
// If limit is increased, waiting goroutines can acquire slots immediately.
// If limit is reduced, in-flight operations continue safely and subsequent acquires are throttled.
func (s *DynamicSemaphore) SetLimit(newLimit int) {
	if newLimit <= 0 {
		newLimit = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limit = newLimit
	s.cond.Broadcast()
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
