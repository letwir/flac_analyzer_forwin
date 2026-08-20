// Package config provides pure data models, TOML parsing, and dynamic hardware scaling functors.
// Functor: (FilePath, HardwareSpecs) -> (RawConfig, DomainConfig, Error)
package config

import (
	"fmt"
	"math"
	"os"

	"flac_analyzer/orchestrator/logger"
	"github.com/pelletier/go-toml/v2"
)

// LoadFromFile reads and parses the TOML configuration file, applying dynamic hardware scaling.
// SideEffectFn: LoadFromFile (IO Monad)
func LoadFromFile(
	configPath string,
	totalRamGB float64,
	numCPU int,
	explicitLogLevel string,
	elog logger.EventLogger,
) (*RawConfig, *Config, error) {
	cfgBytes, err := os.ReadFile(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read config file (%s): %w", configPath, err)
	}

	var raw RawConfig
	if err := toml.Unmarshal(cfgBytes, &raw); err != nil {
		return nil, nil, fmt.Errorf("failed to parse TOML syntax (%s): %w", configPath, err)
	}

	domainCfg := NormalizeConfig(&raw, totalRamGB, numCPU, explicitLogLevel, elog)
	return &raw, domainCfg, nil
}

// NormalizeConfig transforms a RawConfig and hardware specs into a validated Config domain object.
// PureMorph: NormalizeConfig
func NormalizeConfig(
	raw *RawConfig,
	totalRamGB float64,
	numCPU int,
	explicitLogLevel string,
	elog logger.EventLogger,
) *Config {
	// 1. Defaults for dynamic scaling
	if raw.Orchestrator.MaxRamRatio <= 0 {
		raw.Orchestrator.MaxRamRatio = 0.625
	}
	if raw.Orchestrator.CpuWorkerRatio <= 0 {
		raw.Orchestrator.CpuWorkerRatio = 0.80
	}
	if raw.Orchestrator.EstimatedWorkerRamGB <= 0 {
		raw.Orchestrator.EstimatedWorkerRamGB = 1.75
	}
	if raw.Orchestrator.MinAvailRamGB <= 0 {
		raw.Orchestrator.MinAvailRamGB = 1.75
	}
	if raw.Orchestrator.MinAvailDiskGB <= 0 {
		raw.Orchestrator.MinAvailDiskGB = 5.0
	}
	if raw.Orchestrator.DemucsConcurrentLimit <= 0 {
		raw.Orchestrator.DemucsConcurrentLimit = 1
	}
	if raw.Orchestrator.ShmExpansionRatio <= 0 {
		raw.Orchestrator.ShmExpansionRatio = 3.5
	}
	if raw.Orchestrator.ShmRetryCount <= 0 {
		raw.Orchestrator.ShmRetryCount = 5
	}
	if raw.Orchestrator.ShmRetryDelaySec <= 0 {
		raw.Orchestrator.ShmRetryDelaySec = 8
	}

	effectiveWorkers, effectiveRamRatio := ComputeDynamicWorkers(raw, totalRamGB, numCPU)

	targetLogLevelStr := "info"
	if explicitLogLevel != "" {
		targetLogLevelStr = explicitLogLevel
	} else if raw.Orchestrator.LogLevel != "" {
		targetLogLevelStr = raw.Orchestrator.LogLevel
	}
	logLevel := logger.ParseLogLevel(targetLogLevelStr)

	enableVirtualLock := true
	if raw.Orchestrator.EnableVirtualLock != nil {
		enableVirtualLock = *raw.Orchestrator.EnableVirtualLock
	}

	skipDup := true
	if raw.Orchestrator.SkipDupByHash != nil {
		skipDup = *raw.Orchestrator.SkipDupByHash
	}

	gatekeeperRetryDelay := raw.Orchestrator.GatekeeperRetryDelaySec
	if gatekeeperRetryDelay <= 0 {
		gatekeeperRetryDelay = 20
	}

	configWatchInterval := raw.Orchestrator.ConfigWatchIntervalSec
	if configWatchInterval <= 0 {
		configWatchInterval = 600
	}

	enableDlqRetry := true
	if raw.Orchestrator.EnableDlqRetry != nil {
		enableDlqRetry = *raw.Orchestrator.EnableDlqRetry
	}

	dlqRetryInterval := raw.Orchestrator.DlqRetryIntervalSec
	if dlqRetryInterval < 0 {
		dlqRetryInterval = 600
	} else if raw.Orchestrator.DlqRetryIntervalSec == 0 && raw.Orchestrator.EnableDlqRetry == nil {
		dlqRetryInterval = 600
	}

	maxGpuUtilRatio := raw.Orchestrator.MaxGpuUtilizationRatio
	if maxGpuUtilRatio <= 0 {
		maxGpuUtilRatio = 0.85
	}

	minAvailVramGB := raw.Orchestrator.MinAvailVramGB
	if minAvailVramGB <= 0 {
		minAvailVramGB = 0.5
	}

	estimatedDemucsVramGB := raw.Orchestrator.EstimatedDemucsVramGB
	if estimatedDemucsVramGB <= 0 {
		estimatedDemucsVramGB = 1.0
	}

	enableGpuThrottle := true
	if raw.Orchestrator.EnableGpuThrottle != nil {
		enableGpuThrottle = *raw.Orchestrator.EnableGpuThrottle
	}

	demucsDaemonCap := raw.Orchestrator.DemucsDaemonCapacity
	if demucsDaemonCap <= 0 {
		demucsDaemonCap = 2
	}

	demucsDualUtilThreshold := raw.Orchestrator.DemucsDualGpuUtilThreshold
	if demucsDualUtilThreshold <= 0 {
		demucsDualUtilThreshold = 0.50
	}

	demucsDualMinVramGB := raw.Orchestrator.DemucsDualMinVramGB
	if demucsDualMinVramGB <= 0 {
		demucsDualMinVramGB = 4.0
	}

	dbTimeoutSec := raw.Database.TimeoutSec
	if dbTimeoutSec <= 0 {
		dbTimeoutSec = raw.Orchestrator.DbTimeoutSec
	}
	if dbTimeoutSec <= 0 {
		dbTimeoutSec = 20
	}

	enableDiskFallback := true
	if raw.Orchestrator.EnableDiskModeFallback != nil {
		enableDiskFallback = *raw.Orchestrator.EnableDiskModeFallback
	}

	diskModeRamRatio := raw.Orchestrator.DiskModeRamThresholdRatio
	if diskModeRamRatio <= 0 || diskModeRamRatio > 1.0 {
		diskModeRamRatio = 0.8
	}

	resolvedPythonEnv := ResolvePythonEnv(raw.PythonEnv, numCPU, effectiveWorkers)

	return &Config{
		NumWorkers:                 effectiveWorkers,
		MaxRamRatio:                effectiveRamRatio,
		EstimatedWorkerRamGB:       raw.Orchestrator.EstimatedWorkerRamGB,
		MinAvailRamGB:              raw.Orchestrator.MinAvailRamGB,
		MinAvailDiskGB:             raw.Orchestrator.MinAvailDiskGB,
		DemucsConcurrentLimit:      raw.Orchestrator.DemucsConcurrentLimit,
		DemucsDaemonCapacity:       demucsDaemonCap,
		DemucsDualGpuUtilThreshold: demucsDualUtilThreshold,
		DemucsDualMinVramGB:        demucsDualMinVramGB,
		ShmAllocationDelaySec:      raw.Orchestrator.ShmAllocationDelaySec,
		ShmExpansionRatio:          raw.Orchestrator.ShmExpansionRatio,
		ShmRetryCount:              raw.Orchestrator.ShmRetryCount,
		ShmRetryDelaySec:           raw.Orchestrator.ShmRetryDelaySec,
		QueueDir:                   raw.Orchestrator.QueueDir,
		DatabaseURL:                raw.Database.URL,
		PythonEnv:                  resolvedPythonEnv,
		LogLevel:                   logLevel,
		EventLog:                   elog,
		SkipDupByHash:              skipDup,
		EnableVirtualLock:          enableVirtualLock,
		MinWorkingSetMB:            raw.Orchestrator.MinWorkingSetMB,
		MaxWorkingSetMB:            raw.Orchestrator.MaxWorkingSetMB,
		GatekeeperRetryDelaySec:    gatekeeperRetryDelay,
		ConfigWatchIntervalSec:     configWatchInterval,
		EnableDlqRetry:             enableDlqRetry,
		DlqRetryIntervalSec:        dlqRetryInterval,
		MaxGpuUtilizationRatio:     maxGpuUtilRatio,
		MinAvailVramGB:             minAvailVramGB,
		EstimatedDemucsVramGB:      estimatedDemucsVramGB,
		EnableGpuThrottle:          enableGpuThrottle,
		DBTimeoutSec:               dbTimeoutSec,
		EnableDiskModeFallback:     enableDiskFallback,
		DiskModeRamThresholdRatio:  diskModeRamRatio,
	}
}

// ComputeDynamicWorkers calculates the safe concurrent worker count based on RAM & CPU limits.
// PureMorph: ComputeDynamicWorkers
func ComputeDynamicWorkers(raw *RawConfig, totalRamGB float64, numCPU int) (int, float64) {
	effectiveRamRatio := raw.Orchestrator.MaxRamRatio
	if effectiveRamRatio > 0.95 {
		effectiveRamRatio = 0.95
	}
	targetRamGB := totalRamGB * effectiveRamRatio
	ramBasedWorkers := int(math.Floor(targetRamGB / raw.Orchestrator.EstimatedWorkerRamGB))
	if ramBasedWorkers < 1 {
		ramBasedWorkers = 1
	}

	hardCeilingRamGB := totalRamGB * 0.95
	hardCeilingWorkers := int(math.Floor(hardCeilingRamGB / raw.Orchestrator.EstimatedWorkerRamGB))

	cpuBasedWorkers := int(math.Floor(float64(numCPU) * raw.Orchestrator.CpuWorkerRatio))
	if cpuBasedWorkers < 1 {
		cpuBasedWorkers = 1
	}

	workers := raw.Orchestrator.NumWorkers
	if workers <= 0 {
		workers = ramBasedWorkers
		if cpuBasedWorkers < workers {
			workers = cpuBasedWorkers
		}
	} else {
		if workers > ramBasedWorkers {
			workers = ramBasedWorkers
		}
		if workers > hardCeilingWorkers {
			workers = hardCeilingWorkers
		}
	}
	return workers, effectiveRamRatio
}
