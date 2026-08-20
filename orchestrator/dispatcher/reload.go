// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// PureMorph: Config x Config -> DiffMap
// SideEffectFn: UpdateConfig (Apply Dynamic Semaphores)
package dispatcher

import (
	"fmt"
)

// GetConfig returns a thread-safe copy of the current configuration.
func (d *Dispatcher) GetConfig() Config {
	d.configMu.RLock()
	defer d.configMu.RUnlock()

	copiedEnv := make(map[string]string, len(d.config.PythonEnv))
	for k, v := range d.config.PythonEnv {
		copiedEnv[k] = v
	}
	cfg := d.config
	cfg.PythonEnv = copiedEnv
	return cfg
}

// UpdateConfig dynamically updates the configuration at runtime, adjusting semaphores and logging.
// SideEffectFn: UpdateConfig
func (d *Dispatcher) UpdateConfig(newCfg Config) map[string]string {
	d.configMu.Lock()
	defer d.configMu.Unlock()

	oldCfg := d.config
	diff := make(map[string]string)

	if oldCfg.DemucsConcurrentLimit != newCfg.DemucsConcurrentLimit {
		diff["demucs_concurrent_limit"] = fmt.Sprintf("%d -> %d", oldCfg.DemucsConcurrentLimit, newCfg.DemucsConcurrentLimit)
		d.demucsSemaphore.SetLimit(newCfg.DemucsConcurrentLimit)
	}
	if oldCfg.LogLevel != newCfg.LogLevel {
		diff["log_level"] = fmt.Sprintf("%s -> %s", oldCfg.LogLevel, newCfg.LogLevel)
		d.logLevel = newCfg.LogLevel
	}
	if oldCfg.SkipDupByHash != newCfg.SkipDupByHash {
		diff["skip_dup_by_hash"] = fmt.Sprintf("%v -> %v", oldCfg.SkipDupByHash, newCfg.SkipDupByHash)
		d.skipDupByHash = newCfg.SkipDupByHash
	}
	if oldCfg.MaxRamRatio != newCfg.MaxRamRatio {
		diff["max_ram_ratio"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.MaxRamRatio, newCfg.MaxRamRatio)
	}
	if oldCfg.EstimatedWorkerRamGB != newCfg.EstimatedWorkerRamGB {
		diff["estimated_worker_ram_gb"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.EstimatedWorkerRamGB, newCfg.EstimatedWorkerRamGB)
	}
	if oldCfg.MinAvailRamGB != newCfg.MinAvailRamGB {
		diff["min_avail_ram_gb"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.MinAvailRamGB, newCfg.MinAvailRamGB)
	}
	if oldCfg.ShmAllocationDelaySec != newCfg.ShmAllocationDelaySec {
		diff["shm_allocation_delay_sec"] = fmt.Sprintf("%d -> %d", oldCfg.ShmAllocationDelaySec, newCfg.ShmAllocationDelaySec)
	}
	if oldCfg.ShmExpansionRatio != newCfg.ShmExpansionRatio {
		diff["shm_expansion_ratio"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.ShmExpansionRatio, newCfg.ShmExpansionRatio)
	}
	if oldCfg.ShmRetryCount != newCfg.ShmRetryCount {
		diff["shm_retry_count"] = fmt.Sprintf("%d -> %d", oldCfg.ShmRetryCount, newCfg.ShmRetryCount)
	}
	if oldCfg.ShmRetryDelaySec != newCfg.ShmRetryDelaySec {
		diff["shm_retry_delay_sec"] = fmt.Sprintf("%d -> %d", oldCfg.ShmRetryDelaySec, newCfg.ShmRetryDelaySec)
	}
	if oldCfg.QueueDir != newCfg.QueueDir {
		diff["queue_dir"] = fmt.Sprintf("%s -> %s", oldCfg.QueueDir, newCfg.QueueDir)
	}
	if oldCfg.GatekeeperRetryDelaySec != newCfg.GatekeeperRetryDelaySec {
		diff["gatekeeper_retry_delay_sec"] = fmt.Sprintf("%d -> %d", oldCfg.GatekeeperRetryDelaySec, newCfg.GatekeeperRetryDelaySec)
	}
	if oldCfg.ConfigWatchIntervalSec != newCfg.ConfigWatchIntervalSec {
		diff["config_watch_interval_sec"] = fmt.Sprintf("%d -> %d", oldCfg.ConfigWatchIntervalSec, newCfg.ConfigWatchIntervalSec)
	}
	if oldCfg.EnableDlqRetry != newCfg.EnableDlqRetry {
		diff["enable_dlq_retry"] = fmt.Sprintf("%v -> %v", oldCfg.EnableDlqRetry, newCfg.EnableDlqRetry)
	}
	if oldCfg.DlqRetryIntervalSec != newCfg.DlqRetryIntervalSec {
		diff["dlq_retry_interval_sec"] = fmt.Sprintf("%d -> %d", oldCfg.DlqRetryIntervalSec, newCfg.DlqRetryIntervalSec)
	}
	if oldCfg.DemucsDaemonCapacity != newCfg.DemucsDaemonCapacity {
		diff["demucs_daemon_capacity"] = fmt.Sprintf("%d -> %d", oldCfg.DemucsDaemonCapacity, newCfg.DemucsDaemonCapacity)
	}
	if oldCfg.DemucsDualGpuUtilThreshold != newCfg.DemucsDualGpuUtilThreshold {
		diff["demucs_dual_gpu_util_threshold"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.DemucsDualGpuUtilThreshold, newCfg.DemucsDualGpuUtilThreshold)
	}
	if oldCfg.DemucsDualMinVramGB != newCfg.DemucsDualMinVramGB {
		diff["demucs_dual_min_vram_gb"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.DemucsDualMinVramGB, newCfg.DemucsDualMinVramGB)
	}
	if oldCfg.MaxGpuUtilizationRatio != newCfg.MaxGpuUtilizationRatio {
		diff["max_gpu_utilization_ratio"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.MaxGpuUtilizationRatio, newCfg.MaxGpuUtilizationRatio)
	}
	if oldCfg.MinAvailVramGB != newCfg.MinAvailVramGB {
		diff["min_avail_vram_gb"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.MinAvailVramGB, newCfg.MinAvailVramGB)
	}
	if oldCfg.EstimatedDemucsVramGB != newCfg.EstimatedDemucsVramGB {
		diff["estimated_demucs_vram_gb"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.EstimatedDemucsVramGB, newCfg.EstimatedDemucsVramGB)
	}
	if oldCfg.EnableGpuThrottle != newCfg.EnableGpuThrottle {
		diff["enable_gpu_throttle"] = fmt.Sprintf("%v -> %v", oldCfg.EnableGpuThrottle, newCfg.EnableGpuThrottle)
	}
	if oldCfg.DBTimeoutSec != newCfg.DBTimeoutSec {
		diff["db_timeout_sec"] = fmt.Sprintf("%d -> %d", oldCfg.DBTimeoutSec, newCfg.DBTimeoutSec)
	}
	if oldCfg.EnableDiskModeFallback != newCfg.EnableDiskModeFallback {
		diff["enable_disk_mode_fallback"] = fmt.Sprintf("%v -> %v", oldCfg.EnableDiskModeFallback, newCfg.EnableDiskModeFallback)
	}
	if oldCfg.DiskModeRamThresholdRatio != newCfg.DiskModeRamThresholdRatio {
		diff["disk_mode_ram_threshold_ratio"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.DiskModeRamThresholdRatio, newCfg.DiskModeRamThresholdRatio)
	}

	d.config = newCfg
	return diff
}
