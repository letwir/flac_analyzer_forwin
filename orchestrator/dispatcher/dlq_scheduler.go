// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// SideEffectFn: DLQ Recovery & Async Background Scheduler
package dispatcher

import (
	"context"
	"strings"
	"time"

	"flac_analyzer/orchestrator/logger"
)

// TriggerDlqRetry executes retry_ingest.py to process any queued failed payloads in send_failed.db.
// SideEffectFn: TriggerDlqRetry (IO Monad)
func (d *Dispatcher) TriggerDlqRetry(ctx context.Context) error {
	d.LogInfo("[DLQ] Triggering retry_ingest.py execution...")
	out, err := d.runPythonScript("retry_ingest.py", nil, 0, "DLQRetry", logger.ColorYellow, true)
	if err != nil {
		d.LogWarn("[DLQ] retry_ingest.py execution note: %v (output: %s)", err, out)
		return err
	}
	cleanOut := strings.TrimSpace(out)
	if cleanOut != "" {
		d.LogInfo("[DLQ] retry_ingest.py output: %s", cleanOut)
	}
	return nil
}

// StartDlqRetryScheduler starts background periodic execution of retry_ingest.py according to config.
// SideEffectFn: StartDlqRetryScheduler
func (d *Dispatcher) StartDlqRetryScheduler(ctx context.Context) {
	go func() {
		cfg := d.GetConfig()
		if !cfg.EnableDlqRetry {
			d.LogInfo("[DLQ] Auto retry disabled via config (enable_dlq_retry = false)")
			return
		}

		// 1. Run immediately on startup in a separate goroutine
		go func() {
			_ = d.TriggerDlqRetry(ctx)
		}()

		// 2. Periodic ticker if interval > 0
		intervalSec := cfg.DlqRetryIntervalSec
		if intervalSec <= 0 {
			d.LogInfo("[DLQ] Periodic retry disabled (dlq_retry_interval_sec <= 0)")
			return
		}

		ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				currentCfg := d.GetConfig()
				if !currentCfg.EnableDlqRetry {
					continue
				}
				_ = d.TriggerDlqRetry(ctx)
			}
		}
	}()
}
