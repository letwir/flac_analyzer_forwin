// Package config provides pure data models, TOML parsing, and dynamic hardware scaling functors.
// Objects: RawConfig, Config, DatabaseConfig, OrchestratorConfig
package config

import (
	"flac_analyzer/orchestrator/logger"
)

// RawConfig represents the direct deserialized AST from config.toml.
type RawConfig struct {
	Database     DatabaseConfig     `toml:"database"`
	Orchestrator OrchestratorConfig `toml:"orchestrator"`
	PythonEnv    map[string]string  `toml:"python_env"`
}

// DatabaseConfig contains PostgreSQL connection parameters.
type DatabaseConfig struct {
	URL        string `toml:"url"`
	TimeoutSec int    `toml:"db_timeout_sec"`
}

// OrchestratorConfig contains execution thresholds and tuning parameters.
type OrchestratorConfig struct {
	NumWorkers                 int      `toml:"num_workers"`
	MaxRamRatio                float64  `toml:"max_ram_ratio"`
	CpuWorkerRatio             float64  `toml:"cpu_worker_ratio"`
	EstimatedWorkerRamGB       float64  `toml:"estimated_worker_ram_gb"`
	MinAvailRamGB              float64  `toml:"min_avail_ram_gb"`
	MinAvailDiskGB             float64  `toml:"min_avail_disk_gb"`
	DemucsConcurrentLimit      int      `toml:"demucs_concurrent_limit"`
	DemucsDaemonCapacity       int      `toml:"demucs_daemon_capacity"`
	DemucsDualGpuUtilThreshold float64  `toml:"demucs_dual_gpu_util_threshold"`
	DemucsDualMinVramGB        float64  `toml:"demucs_dual_min_vram_gb"`
	ShmAllocationDelaySec      int      `toml:"shm_allocation_delay_sec"`
	ShmExpansionRatio          float64  `toml:"shm_expansion_ratio"`
	ShmRetryCount              int      `toml:"shm_retry_count"`
	ShmRetryDelaySec           int      `toml:"shm_retry_delay_sec"`
	QueueDir                   string   `toml:"queue_dir"`
	LogLevel                   string   `toml:"log_level"`
	SkipDupByHash              *bool    `toml:"skip_dup_by_hash"`
	EnableVirtualLock          *bool    `toml:"enable_virtual_lock"`
	MinWorkingSetMB            int      `toml:"min_working_set_mb"`
	MaxWorkingSetMB            int      `toml:"max_working_set_mb"`
	GatekeeperRetryDelaySec    int      `toml:"gatekeeper_retry_delay_sec"`
	ConfigWatchIntervalSec     int      `toml:"config_watch_interval_sec"`
	EnableDlqRetry             *bool    `toml:"enable_dlq_retry"`
	DlqRetryIntervalSec        int      `toml:"dlq_retry_interval_sec"`
	MaxGpuUtilizationRatio     float64  `toml:"max_gpu_utilization_ratio"`
	MinAvailVramGB             float64  `toml:"min_avail_vram_gb"`
	EstimatedDemucsVramGB      float64  `toml:"estimated_demucs_vram_gb"`
	EnableGpuThrottle          *bool    `toml:"enable_gpu_throttle"`
	DbTimeoutSec               int      `toml:"db_timeout_sec"`
	EnableDiskModeFallback     *bool    `toml:"enable_disk_mode_fallback"`
	DiskModeRamThresholdRatio  float64  `toml:"disk_mode_ram_threshold_ratio"`
}

// Config represents the fully normalized, runtime-validated configuration domain object.
type Config struct {
	NumWorkers                 int
	MaxRamRatio                float64
	EstimatedWorkerRamGB       float64
	MinAvailRamGB              float64
	MinAvailDiskGB             float64
	DemucsConcurrentLimit      int
	DemucsDaemonCapacity       int
	DemucsDualGpuUtilThreshold float64
	DemucsDualMinVramGB        float64
	ShmAllocationDelaySec      int
	ShmExpansionRatio          float64
	ShmRetryCount              int
	ShmRetryDelaySec           int
	QueueDir                   string
	DatabaseURL                string
	PythonEnv                  map[string]string
	LogLevel                   logger.LogLevel
	EventLog                   logger.EventLogger
	SkipDupByHash              bool
	EnableVirtualLock          bool
	MinWorkingSetMB            int
	MaxWorkingSetMB            int
	GatekeeperRetryDelaySec    int
	ConfigWatchIntervalSec     int
	EnableDlqRetry             bool
	DlqRetryIntervalSec        int
	MaxGpuUtilizationRatio     float64
	MinAvailVramGB             float64
	EstimatedDemucsVramGB      float64
	EnableGpuThrottle          bool
	DBTimeoutSec               int
	EnableDiskModeFallback     bool
	DiskModeRamThresholdRatio  float64
}
