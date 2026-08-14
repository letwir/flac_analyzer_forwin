package dispatcher

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDynamicSemaphore_BasicAcquireRelease(t *testing.T) {
	sem := NewDynamicSemaphore(2)

	if sem.GetLimit() != 2 {
		t.Fatalf("expected limit 2, got %d", sem.GetLimit())
	}
	if sem.GetInUse() != 0 {
		t.Fatalf("expected inUse 0, got %d", sem.GetInUse())
	}

	sem.Acquire()
	if sem.GetInUse() != 1 {
		t.Fatalf("expected inUse 1, got %d", sem.GetInUse())
	}

	sem.Acquire()
	if sem.GetInUse() != 2 {
		t.Fatalf("expected inUse 2, got %d", sem.GetInUse())
	}

	sem.Release()
	if sem.GetInUse() != 1 {
		t.Fatalf("expected inUse 1, got %d", sem.GetInUse())
	}

	sem.Release()
	if sem.GetInUse() != 0 {
		t.Fatalf("expected inUse 0, got %d", sem.GetInUse())
	}
}

func TestDynamicSemaphore_DynamicResize(t *testing.T) {
	sem := NewDynamicSemaphore(1)

	sem.Acquire()

	var acquiredSecond int32
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		sem.Acquire()
		atomic.StoreInt32(&acquiredSecond, 1)
		sem.Release()
	}()

	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&acquiredSecond) != 0 {
		t.Fatalf("goroutine should be waiting for slot")
	}

	// Expand limit to 2 -> should allow goroutine to acquire
	sem.SetLimit(2)

	wg.Wait()
	if atomic.LoadInt32(&acquiredSecond) != 1 {
		t.Fatalf("goroutine should have acquired slot after limit expansion")
	}

	sem.Release()
	if sem.GetInUse() != 0 {
		t.Fatalf("expected inUse 0, got %d", sem.GetInUse())
	}
}
