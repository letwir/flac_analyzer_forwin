package dispatcher

import (
	"context"
	"os"
	"path/filepath"
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
