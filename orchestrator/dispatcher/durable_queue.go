package dispatcher

import (
	"encoding/json"
	"errors"
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
	readyWorkers := 0

	for {
		if d.taskFeederCtx.Err() != nil {
			return
		}
		select {
		case <-d.taskFeederCtx.Done():
			return
		case <-d.taskWakeCh:
			readyWorkers = d.fillTaskQueue(readyWorkers)
		case <-d.workerReadyCh:
			readyWorkers++
			readyWorkers = d.fillTaskQueue(readyWorkers)
		case <-ticker.C:
			readyWorkers = d.fillTaskQueue(readyWorkers)
		}
	}
}

func (d *Dispatcher) fillTaskQueue(readyWorkers int) int {
	if readyWorkers <= 0 {
		return 0
	}
	cfg := d.GetConfig()
	retryDelay := cfg.GatekeeperRetryDelaySec
	if retryDelay <= 0 {
		retryDelay = 20
	}
	for readyWorkers > 0 {
		if d.taskFeederCtx.Err() != nil {
			break
		}
		// Claim only when a worker is waiting. This keeps every unstarted item
		// PENDING in SQLite so each intake can affect the next size comparison.
		if _, err := d.db.RequeueRetryableTasks(1, retryDelay); err != nil {
			d.LogError("[TaskFeeder] Failed to release retryable tasks: %v", err)
			break
		}
		queued, err := d.db.ClaimPendingTasksInOrder(1, state.PendingTaskOrderSizeAscending)
		if err != nil {
			d.LogError("[TaskFeeder] Failed to claim durable task: %v", err)
			break
		}
		if len(queued) == 0 {
			break
		}
		task, err := decodeQueuedTask(queued[0])
		if err != nil {
			_ = d.db.UpdateStatus(queued[0].FilePath, queued[0].TrackNumber, state.StatusFailedMaybeRetry, err.Error())
			continue
		}
		planned, err := d.prepareAnalysisTasks(d.taskFeederCtx, []TaskPayload{task})
		if err != nil {
			_ = d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusFailedMaybeRetry, err.Error())
			d.LogError("[TaskFeeder] Analysis preflight failed closed: %v", err)
			break
		}
		if len(planned) == 0 {
			_ = d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusFailedMaybeRetry, "analysis preflight returned no task")
			break
		}
		task = planned[0]
		if task.AnalysisDecision == Skip {
			_ = d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusCompleted, "analysis preflight: complete")
			metrics.AnalyzerTasksTotal.WithLabelValues("success").Inc()
			continue
		}
		lease, err := d.tryReserveTaskAdmission(task)
		if err != nil {
			if errors.Is(err, ErrPermanentBudgetExcess) {
				_ = d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusFailed, err.Error())
				metrics.AnalyzerTasksTotal.WithLabelValues("failed").Inc()
			} else {
				d.parkTaskForAdmission(task, err)
			}
			continue
		}
		if d.reserveTaskFn == nil {
			if err := d.recheckTaskAdmissionBeforeDispatch(task, lease); err != nil {
				d.releaseUnstartedTaskAdmission(task)
				d.parkTaskForAdmission(task, err)
				continue
			}
		}
		select {
		case d.taskQueue <- task:
			metrics.AnalyzerQueueLength.Inc()
			readyWorkers--
		case <-d.taskFeederCtx.Done():
			d.releaseUnstartedTaskAdmission(task)
			_ = d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusPending, "feeder stopped before worker handoff")
			return readyWorkers
		}
	}
	if d.statsTracker != nil {
		if waiting, err := d.db.CountWaitingTasks(); err == nil {
			queued := int(waiting) - int(atomic.LoadInt32(&d.activeTaskCount))
			if queued < 0 {
				queued = 0
			}
			d.statsTracker.SetQueueLength(queued)
		}
	}
	if len(d.taskQueue) == 0 && atomic.LoadInt32(&d.activeTaskCount) == 0 && d.cpuDaemonPool != nil {
		d.cpuDaemonPool.TrimIdle(1)
	}
	return readyWorkers
}

func (d *Dispatcher) releaseUnstartedTaskAdmission(task TaskPayload) {
	key := admissionKey(task)
	d.inFlightMutex.Lock()
	ramLease, hasRAMLease := d.ramAdmissions[key]
	d.inFlightMutex.Unlock()
	if hasRAMLease {
		d.releaseRamAdmission(task, ramLease)
	}
	if lease, ok := d.takeTaskAdmission(task); ok {
		d.releaseTaskAdmission(task, lease)
	}
}

func (d *Dispatcher) parkTaskForAdmission(task TaskPayload, cause error) {
	reason := fmt.Sprintf("admission deferred: %v", cause)
	delay := d.GetConfig().GatekeeperRetryDelaySec
	if delay <= 0 {
		delay = 20
	}
	if err := d.db.ParkTaskForRetry(task.FlacPath, task.TrackNumber, reason, delay); err != nil {
		d.LogError("[TaskFeeder] Failed to park task %s track %d: %v", task.FlacPath, task.TrackNumber, err)
		return
	}
	metrics.AnalyzerTasksTotal.WithLabelValues("retry_pending").Inc()
	if d.shouldLogParkReason(reason, time.Now()) {
		d.LogWarn("[TaskFeeder] task parked without occupying a worker: %s (repeated reasons suppressed for 1m)", reason)
	}
}

func (d *Dispatcher) shouldLogParkReason(reason string, now time.Time) bool {
	d.parkLogMu.Lock()
	defer d.parkLogMu.Unlock()
	if d.parkLogReasons == nil {
		d.parkLogReasons = make(map[string]time.Time)
	}
	if last, ok := d.parkLogReasons[reason]; ok && now.Sub(last) < time.Minute {
		return false
	}
	d.parkLogReasons[reason] = now
	return true
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
