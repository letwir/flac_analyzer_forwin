package dispatcher

import (
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"flac_analyzer/orchestrator/metrics"
	"flac_analyzer/orchestrator/state"
)

func (d *Dispatcher) notifyTaskFeeder() {
	select {
	case d.taskWakeCh <- struct{}{}:
	default:
	}
}

func (d *Dispatcher) waitForGatekeeperRetry(delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-d.taskFeederCtx.Done():
		return false
	}
}

func (d *Dispatcher) taskFeeder() {
	defer d.taskFeederWg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-d.taskFeederCtx.Done():
			return
		case <-d.taskWakeCh:
			d.fillTaskQueue()
		case <-ticker.C:
			d.fillTaskQueue()
		}
	}
}

func (d *Dispatcher) fillTaskQueue() {
	availableSlots := cap(d.taskQueue) - len(d.taskQueue)
	if availableSlots <= 0 {
		return
	}

	tasks, err := d.db.ClaimPendingTasks(availableSlots)
	if err != nil {
		d.LogError("[TaskFeeder] Failed to claim durable tasks: %v", err)
		return
	}
	for _, queued := range tasks {
		task, err := decodeQueuedTask(queued)
		if err != nil {
			d.db.UpdateStatus(queued.FilePath, queued.TrackNumber, state.StatusFailedMaybeRetry, err.Error())
			continue
		}
		d.taskQueue <- task
		metrics.AnalyzerQueueLength.Inc()
	}
	if len(tasks) > 0 && d.statsTracker != nil {
		d.statsTracker.SetQueueLength(len(d.taskQueue))
		return
	}

	// Retryable failures are deliberately parked until the ordinary queue has
	// drained and a cooldown has elapsed. This prevents low-RAM retry storms.
	if len(d.taskQueue) == 0 && atomic.LoadInt32(&d.activeTaskCount) == 0 {
		cfg := d.GetConfig()
		maxRetries := cfg.GatekeeperMaxRetries
		if maxRetries <= 0 {
			maxRetries = 5
		}
		minAgeSec := cfg.GatekeeperRetryDelaySec * maxRetries
		if minAgeSec < 60 {
			minAgeSec = 60
		}
		if _, err := d.db.RequeueRetryableTasks(cap(d.taskQueue), minAgeSec); err != nil {
			d.LogError("[TaskFeeder] Failed to release retryable tasks: %v", err)
			return
		}
		// The next ticker/wakeup claims the newly released PENDING rows.
	}
}

func decodeQueuedTask(queued state.QueuedTask) (TaskPayload, error) {
	if queued.PayloadJSON == "" {
		// Rows created by older binaries have no payload. Recover the path and
		// track number, while retaining a conservative file-size estimate.
		task := TaskPayload{FlacPath: queued.FilePath, TrackNumber: queued.TrackNumber}
		if info, err := os.Stat(queued.FilePath); err == nil {
			task.FileSize = info.Size()
		}
		return task, nil
	}

	var task TaskPayload
	if err := json.Unmarshal([]byte(queued.PayloadJSON), &task); err != nil {
		return TaskPayload{}, fmt.Errorf("invalid durable task payload for %s track %d: %w", queued.FilePath, queued.TrackNumber, err)
	}
	if task.FlacPath == "" {
		task.FlacPath = queued.FilePath
	}
	if task.TrackNumber == 0 {
		task.TrackNumber = queued.TrackNumber
	}
	return task, nil
}

func (d *Dispatcher) markTaskMaybeRetry(workerID int, task TaskPayload, attempts int) {
	reason := fmt.Sprintf("Gatekeeper NOGO persisted after %d attempts; task parked for retry when resources recover", attempts)
	if err := d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusFailedMaybeRetry, reason); err != nil {
		d.LogError("[TaskFeeder] Failed to mark retryable task %s track %d: %v", task.FlacPath, task.TrackNumber, err)
	}
	metrics.AnalyzerQueueLength.Dec()
	metrics.AnalyzerTasksTotal.WithLabelValues("retry_pending").Inc()
	if d.statsTracker != nil {
		d.statsTracker.SetQueueLength(len(d.taskQueue))
	}
	d.LogWarn("[W-%d] [Gatekeeper] %s", workerID, reason)
}
