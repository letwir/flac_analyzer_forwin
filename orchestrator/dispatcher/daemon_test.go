package dispatcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDaemonPingPong(t *testing.T) {
	parentDir := findProjectRoot()
	pythonPath := "python.exe"
	venvPython := filepath.Join(parentDir, ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(venvPython); err == nil {
		pythonPath = venvPython
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := NewWorkerDaemonClient(999, pythonPath, parentDir, nil, func(format string, v ...interface{}) {
		t.Logf(format, v...)
	})
	if err != nil {
		t.Fatalf("Failed to create worker daemon client: %v", err)
	}
	defer client.Close()

	if !client.IsHealthy() {
		t.Errorf("Expected client to be healthy")
	}

	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
}

func TestDaemonPoolAcquireRelease(t *testing.T) {
	parentDir := findProjectRoot()
	pythonPath := "python.exe"
	venvPython := filepath.Join(parentDir, ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(venvPython); err == nil {
		pythonPath = venvPython
	}

	pool := NewWorkerDaemonPool(2, pythonPath, parentDir, nil, func(format string, v ...interface{}) {
		t.Logf(format, v...)
	})
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	daemon1, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire daemon1: %v", err)
	}

	if err := daemon1.Ping(ctx); err != nil {
		t.Errorf("daemon1 ping failed: %v", err)
	}

	pool.Release(daemon1)

	// Acquire again - should reuse daemon1
	daemon2, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire daemon2: %v", err)
	}
	if daemon2.id != daemon1.id {
		t.Logf("Reacquired daemon has id %d (first was %d)", daemon2.id, daemon1.id)
	}
	pool.Release(daemon2)
}

func TestDaemonPoolThunderingHerd(t *testing.T) {
	parentDir := findProjectRoot()
	pythonPath := "python.exe"
	venvPython := filepath.Join(parentDir, ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(venvPython); err == nil {
		pythonPath = venvPython
	}

	// Max 2 daemons in pool
	pool := NewWorkerDaemonPool(2, pythonPath, parentDir, nil, func(format string, v ...interface{}) {
		t.Logf(format, v...)
	})
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	const numCallers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, numCallers)

	for i := 0; i < numCallers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			daemon, err := pool.Acquire(ctx)
			if err != nil {
				errCh <- fmt.Errorf("worker %d acquire failed: %w", workerID, err)
				return
			}
			// Simulate small work
			time.Sleep(50 * time.Millisecond)
			pool.Release(daemon)
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("Thundering herd caller error: %v", err)
	}

	pool.mu.Lock()
	totalSpawned := len(pool.allDaemons)
	spawning := pool.spawningCount
	pool.mu.Unlock()

	if totalSpawned > 2 {
		t.Errorf("Thundering herd violation: spawned %d daemons (max was 2)", totalSpawned)
	}
	if spawning != 0 {
		t.Errorf("Leaked spawning count: %d", spawning)
	}
}

