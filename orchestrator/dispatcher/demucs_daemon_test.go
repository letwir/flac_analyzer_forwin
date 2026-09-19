package dispatcher

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flac_analyzer/orchestrator/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// nopWriteCloser wraps a no-op write closer for fake stdin.
type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

// fakeClient creates a DemucsDaemonClient that is alive but has no real process.
func fakeClient(id int) *DemucsDaemonClient {
	return &DemucsDaemonClient{
		id:         id,
		cmd:        nil, // no real process
		stdin:      nopWriteCloser{},
		stdout:     bufio.NewReader(strings.NewReader("")),
		loggerFunc: func(format string, v ...interface{}) {},
		isAlive:    true,
	}
}

func alwaysSucceedFactory() (clientFactory, *atomic.Int32) {
	count := &atomic.Int32{}
	return func(id int, pythonPath, workingDir string, envVars []string, loggerFunc func(format string, v ...interface{})) (*DemucsDaemonClient, error) {
		count.Add(1)
		return fakeClient(id), nil
	}, count
}

func failNTimesFactory(n int) (clientFactory, *atomic.Int32) {
	remaining := &atomic.Int32{}
	remaining.Store(int32(n))
	count := &atomic.Int32{}
	return func(id int, pythonPath, workingDir string, envVars []string, loggerFunc func(format string, v ...interface{})) (*DemucsDaemonClient, error) {
		count.Add(1)
		if remaining.Add(-1) >= 0 {
			return nil, fmt.Errorf("injected spawn failure")
		}
		return fakeClient(id), nil
	}, count
}

func silentLogger(format string, v ...interface{}) {}

func TestPoolPrewarmAndGauge(t *testing.T) {
	metrics.AnalyzerDemucsDaemonPoolSize.Set(0)
	factory, _ := alwaysSucceedFactory()
	pool := NewDemucsDaemonPoolWithFactory(1, "python", ".", nil, silentLogger, factory)

	ctx := context.Background()
	if err := pool.Prewarm(ctx, 1); err != nil {
		t.Fatalf("Prewarm failed: %v", err)
	}

	val := testutil.ToFloat64(metrics.AnalyzerDemucsDaemonPoolSize)
	if val != 1 {
		t.Errorf("expected pool size gauge 1 after prewarm, got %v", val)
	}

	client, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	val = testutil.ToFloat64(metrics.AnalyzerDemucsDaemonPoolSize)
	if val != 1 {
		t.Errorf("expected pool size gauge 1 after acquire, got %v", val)
	}

	pool.Release(client)

	val = testutil.ToFloat64(metrics.AnalyzerDemucsDaemonPoolSize)
	if val != 1 {
		t.Errorf("expected pool size gauge 1 after release, got %v", val)
	}

	pool.Close()
	val = testutil.ToFloat64(metrics.AnalyzerDemucsDaemonPoolSize)
	if val != 0 {
		t.Errorf("expected pool size gauge 0 after close, got %v", val)
	}
}

func TestPoolRestartFailureThenRecovery(t *testing.T) {
	// First factory call succeeds (prewarm), second fails (restart), third succeeds (recovery)
	factory, spawnCount := failNTimesFactory(0) // all succeed initially
	pool := NewDemucsDaemonPoolWithFactory(1, "python", ".", nil, silentLogger, factory)

	ctx := context.Background()
	if err := pool.Prewarm(ctx, 1); err != nil {
		t.Fatalf("Prewarm failed: %v", err)
	}

	client, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("First Acquire failed: %v", err)
	}

	// Mark client as dead before releasing
	client.isAlive = false
	pool.Release(client)

	// Now swap factory to fail once then succeed
	failOnce, _ := failNTimesFactory(1)
	pool.mu.Lock()
	pool.factory = failOnce
	pool.mu.Unlock()

	// Next Acquire should find dead client, attempt restart (fails), free capacity
	_, err = pool.Acquire(ctx)
	if err == nil {
		t.Fatal("Expected Acquire to fail on restart failure")
	}

	// Pool capacity is freed. Next Acquire with working factory should succeed.
	pool.mu.Lock()
	pool.factory, _ = alwaysSucceedFactory()
	pool.mu.Unlock()

	client2, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Recovery Acquire failed: %v, spawnCount=%d", err, spawnCount.Load())
	}
	if client2 == nil {
		t.Fatal("Expected non-nil client from recovery")
	}

	pool.Release(client2)
	pool.Close()
}

func TestPoolWaitersWakeOnFreedCapacity(t *testing.T) {
	factory, _ := alwaysSucceedFactory()
	pool := NewDemucsDaemonPoolWithFactory(1, "python", ".", nil, silentLogger, factory)

	ctx := context.Background()
	if err := pool.Prewarm(ctx, 1); err != nil {
		t.Fatalf("Prewarm failed: %v", err)
	}

	// Acquire the only slot
	client, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	// Start a waiter in background
	acquired := make(chan *DemucsDaemonClient, 1)
	acquireErr := make(chan error, 1)
	go func() {
		c, err := pool.Acquire(ctx)
		if err != nil {
			acquireErr <- err
			return
		}
		acquired <- c
	}()

	// Give the goroutine time to start waiting
	time.Sleep(50 * time.Millisecond)

	// Release should wake the waiter
	pool.Release(client)

	select {
	case c := <-acquired:
		if c == nil {
			t.Error("Expected non-nil client from woken waiter")
		}
		pool.Release(c)
	case err := <-acquireErr:
		t.Fatalf("Waiter Acquire failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Waiter did not wake within 3s after Release")
	}

	pool.Close()
}

func TestPoolConcurrentAcquireCapacityBounded(t *testing.T) {
	var spawnMu sync.Mutex
	spawnIDs := make(map[int]bool)
	factory := func(id int, pythonPath, workingDir string, envVars []string, loggerFunc func(format string, v ...interface{})) (*DemucsDaemonClient, error) {
		spawnMu.Lock()
		spawnIDs[id] = true
		spawnMu.Unlock()
		return fakeClient(id), nil
	}
	pool := NewDemucsDaemonPoolWithFactory(1, "python", ".", nil, silentLogger, factory)

	ctx := context.Background()

	// Launch 5 concurrent Acquires
	var wg sync.WaitGroup
	results := make(chan *DemucsDaemonClient, 5)
	errors := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := pool.Acquire(ctx)
			if err != nil {
				errors <- err
				return
			}
			results <- c
		}()
	}

	// Wait a bit for them to compete, then release each as we get it
	for i := 0; i < 5; i++ {
		select {
		case c := <-results:
			time.Sleep(10 * time.Millisecond) // simulate work
			pool.Release(c)
		case err := <-errors:
			t.Errorf("Concurrent Acquire error: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("Concurrent Acquire timed out")
		}
	}
	wg.Wait()

	// Verify: only 1 daemon should have been spawned (capacity=1)
	pool.mu.Lock()
	clientCount := len(pool.clients)
	pool.mu.Unlock()
	if clientCount != 1 {
		t.Errorf("Expected exactly 1 client in registry, got %d", clientCount)
	}

	pool.Close()
}

func TestPoolCloseWakesWaiters(t *testing.T) {
	factory, _ := alwaysSucceedFactory()
	pool := NewDemucsDaemonPoolWithFactory(1, "python", ".", nil, silentLogger, factory)

	ctx := context.Background()
	if err := pool.Prewarm(ctx, 1); err != nil {
		t.Fatalf("Prewarm failed: %v", err)
	}

	// Acquire the only slot
	_, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	// Start a waiter
	waiterDone := make(chan error, 1)
	go func() {
		_, err := pool.Acquire(ctx)
		waiterDone <- err
	}()

	time.Sleep(50 * time.Millisecond)

	// Close should wake the waiter with an error
	pool.Close()

	select {
	case err := <-waiterDone:
		if err == nil {
			t.Error("Expected error from waiter after Close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Waiter not woken by Close within 3s")
	}
}

func TestPoolContextCancel(t *testing.T) {
	factory, _ := alwaysSucceedFactory()
	pool := NewDemucsDaemonPoolWithFactory(1, "python", ".", nil, silentLogger, factory)

	ctx := context.Background()
	if err := pool.Prewarm(ctx, 1); err != nil {
		t.Fatalf("Prewarm failed: %v", err)
	}

	// Acquire the only slot
	_, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	// Start a waiter with cancellable context
	cancelCtx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := pool.Acquire(cancelCtx)
		waiterDone <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-waiterDone:
		if err == nil {
			t.Error("Expected context cancellation error from waiter")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Waiter not woken by context cancel within 3s")
	}

	pool.Close()
}

func TestPoolRecycleFailureFreesCapacity(t *testing.T) {
	factory, _ := alwaysSucceedFactory()
	pool := NewDemucsDaemonPoolWithFactory(1, "python", ".", nil, silentLogger, factory)

	ctx := context.Background()
	if err := pool.Prewarm(ctx, 1); err != nil {
		t.Fatalf("Prewarm failed: %v", err)
	}

	client, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	// Force recycle threshold
	client.taskCount = 50

	// Swap factory to fail (simulate recycle failure)
	failFactory, _ := failNTimesFactory(100) // always fail
	pool.mu.Lock()
	pool.factory = failFactory
	pool.mu.Unlock()

	// Release triggers recycle which fails -> capacity should be freed
	pool.Release(client)

	// Restore working factory
	pool.mu.Lock()
	pool.factory, _ = alwaysSucceedFactory()
	pool.mu.Unlock()

	// Next Acquire should succeed by spawning a new client
	client2, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire after recycle failure should succeed: %v", err)
	}
	pool.Release(client2)
	pool.Close()
}
