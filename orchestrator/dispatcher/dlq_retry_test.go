package dispatcher

import (
	"context"
	"testing"
	"time"
)

func TestDlqRetryScheduler_Disabled(t *testing.T) {
	cfg := Config{
		EnableDlqRetry:      false,
		DlqRetryIntervalSec: 1,
	}
	disp := NewDispatcher(cfg, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Should return immediately and not panic
	disp.StartDlqRetryScheduler(ctx)
}

func TestDlqRetryScheduler_ContextCancel(t *testing.T) {
	cfg := Config{
		EnableDlqRetry:      true,
		DlqRetryIntervalSec: 10,
	}
	disp := NewDispatcher(cfg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	disp.StartDlqRetryScheduler(ctx)
	time.Sleep(100 * time.Millisecond)
}

func TestDlqRetry_ConfigUpdate(t *testing.T) {
	cfg := Config{
		EnableDlqRetry:      false,
		DlqRetryIntervalSec: 600,
	}
	disp := NewDispatcher(cfg, nil)

	newCfg := cfg
	newCfg.EnableDlqRetry = true
	newCfg.DlqRetryIntervalSec = 300

	diff := disp.UpdateConfig(newCfg)

	if diff["enable_dlq_retry"] != "false -> true" {
		t.Fatalf("Expected enable_dlq_retry diff, got %s", diff["enable_dlq_retry"])
	}
	if diff["dlq_retry_interval_sec"] != "600 -> 300" {
		t.Fatalf("Expected dlq_retry_interval_sec diff, got %s", diff["dlq_retry_interval_sec"])
	}
	if !disp.GetConfig().EnableDlqRetry {
		t.Fatalf("Expected EnableDlqRetry=true after update")
	}
}
