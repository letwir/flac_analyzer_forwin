// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// PureMorph: GatekeeperInput -> GatekeeperDecision
// SideEffectFn: EvaluateGoNoGo (Observe System -> GatekeeperDecision)
package dispatcher

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"flac_analyzer/orchestrator/sysinfo"
)

// GatekeeperInput encapsulates all parameters required for pure pre-flight dispatch decisions.
type GatekeeperInput struct {
	StorageMode       StorageMode
	EstimatedTaskDisk uint64
	AvailPhys         uint64
	InFlightRam       uint64
	EstimatedTaskRam  uint64
	MinAvailRam       uint64
	MemoryLoad        uint32
	AvailDisk         uint64
	MinAvailDisk      uint64
	GpuUtilization    float64
	AvailVram         uint64
	MinAvailVram      uint64
	EstimatedTaskVram uint64
	MaxGpuUtilization float64
	EnableGpuThrottle bool
	RetryDelay        time.Duration
}

// GatekeeperDecision encapsulates the decision result of EvaluateGoNoGoPure.
type GatekeeperDecision struct {
	IsGo                bool
	WaitDuration        time.Duration
	Reason              string
	StorageMode         StorageMode
	EstimatedRamBytes   uint64
	EffectiveAvailBytes uint64
	RequiredBytes       uint64
	MemoryLoad          uint32
	AvailDiskBytes      uint64
	MinAvailDiskBytes   uint64
	GpuUtilization      float64
	AvailVramBytes      uint64
	RequiredVramBytes   uint64
	IsGpuBlock          bool
}

// EvaluateGoNoGoPure evaluates whether a task can be dispatched without side-effects (Pure Domain Morphism).
// PureMorph: EvaluateGoNoGoPure
func EvaluateGoNoGoPure(in GatekeeperInput) GatekeeperDecision {
	retryDelay := in.RetryDelay
	if retryDelay <= 0 {
		retryDelay = 20 * time.Second
	}

	// 1. Disk Space Check (Storage Defense)
	requiredDisk := in.MinAvailDisk
	if in.StorageMode == StorageModeDisk {
		requiredDisk += in.EstimatedTaskDisk
	}
	if requiredDisk > 0 && in.AvailDisk < requiredDisk {
		return GatekeeperDecision{
			IsGo:              false,
			WaitDuration:      retryDelay,
			Reason:            fmt.Sprintf("Available Disk Space (%.2f GB) < Required (%.2f GB = Task %.2f GB + MinAvail %.2f GB)", float64(in.AvailDisk)/(1024*1024*1024), float64(requiredDisk)/(1024*1024*1024), float64(in.EstimatedTaskDisk)/(1024*1024*1024), float64(in.MinAvailDisk)/(1024*1024*1024)),
			StorageMode:       in.StorageMode,
			EstimatedRamBytes: in.EstimatedTaskRam,
			MemoryLoad:        in.MemoryLoad,
			AvailDiskBytes:    in.AvailDisk,
			MinAvailDiskBytes: in.MinAvailDisk,
		}
	}

	// 2. RAM Capacity & MemoryLoad Check
	var effectiveAvailBytes uint64
	if in.AvailPhys > in.InFlightRam {
		effectiveAvailBytes = in.AvailPhys - in.InFlightRam
	} else {
		effectiveAvailBytes = 0
	}

	// Disk Mode intentionally separates the task's working-set reservation
	// from the SHM safety floor: audio stems are spooled to disk, so requiring
	// the full SHM reserve here would defeat the fallback and starve the queue.
	requiredBytes := in.EstimatedTaskRam + in.MinAvailRam
	if in.StorageMode == StorageModeDisk {
		requiredBytes = in.EstimatedTaskRam
	}
	if effectiveAvailBytes < requiredBytes {
		return GatekeeperDecision{
			IsGo:                false,
			WaitDuration:        retryDelay,
			Reason:              fmt.Sprintf("Effective Avail RAM (%d MB = Avail %d MB - InFlight %d MB) < Required (%d MB = Task %d MB + MinAvail %d MB)", effectiveAvailBytes/1024/1024, in.AvailPhys/1024/1024, in.InFlightRam/1024/1024, requiredBytes/1024/1024, in.EstimatedTaskRam/1024/1024, in.MinAvailRam/1024/1024),
			StorageMode:         in.StorageMode,
			EstimatedRamBytes:   in.EstimatedTaskRam,
			EffectiveAvailBytes: effectiveAvailBytes,
			RequiredBytes:       requiredBytes,
			MemoryLoad:          in.MemoryLoad,
			AvailDiskBytes:      in.AvailDisk,
			MinAvailDiskBytes:   in.MinAvailDisk,
		}
	}

	if in.MemoryLoad >= 90 {
		return GatekeeperDecision{
			IsGo:                false,
			WaitDuration:        retryDelay,
			Reason:              fmt.Sprintf("System MemoryLoad too high (%d%% >= 90%%)", in.MemoryLoad),
			StorageMode:         in.StorageMode,
			EstimatedRamBytes:   in.EstimatedTaskRam,
			EffectiveAvailBytes: effectiveAvailBytes,
			RequiredBytes:       requiredBytes,
			MemoryLoad:          in.MemoryLoad,
			AvailDiskBytes:      in.AvailDisk,
			MinAvailDiskBytes:   in.MinAvailDisk,
		}
	}

	// 3. GPU & VRAM Throttle Defense (Only evaluated if enabled)
	if in.EnableGpuThrottle {
		// GPU 負荷率判定 (例: Max 85%)
		if in.MaxGpuUtilization > 0 && in.GpuUtilization >= (in.MaxGpuUtilization*100) {
			return GatekeeperDecision{
				IsGo:                false,
				WaitDuration:        retryDelay,
				Reason:              fmt.Sprintf("GPU Utilization too high (%.1f%% >= %.1f%% threshold)", in.GpuUtilization, in.MaxGpuUtilization*100),
				StorageMode:         in.StorageMode,
				EstimatedRamBytes:   in.EstimatedTaskRam,
				EffectiveAvailBytes: effectiveAvailBytes,
				RequiredBytes:       requiredBytes,
				MemoryLoad:          in.MemoryLoad,
				AvailDiskBytes:      in.AvailDisk,
				MinAvailDiskBytes:   in.MinAvailDisk,
				GpuUtilization:      in.GpuUtilization,
				AvailVramBytes:      in.AvailVram,
				IsGpuBlock:          true,
			}
		}

		// VRAM 空き容量判定
		if in.MinAvailVram > 0 && in.AvailVram != math.MaxUint64 {
			requiredVram := in.EstimatedTaskVram + in.MinAvailVram
			if in.AvailVram < requiredVram {
				return GatekeeperDecision{
					IsGo:                false,
					WaitDuration:        retryDelay,
					Reason:              fmt.Sprintf("Available VRAM (%.2f GB) < Required (%.2f GB = Task %.2f GB + MinAvail %.2f GB)", float64(in.AvailVram)/(1024*1024*1024), float64(requiredVram)/(1024*1024*1024), float64(in.EstimatedTaskVram)/(1024*1024*1024), float64(in.MinAvailVram)/(1024*1024*1024)),
					StorageMode:         in.StorageMode,
					EstimatedRamBytes:   in.EstimatedTaskRam,
					EffectiveAvailBytes: effectiveAvailBytes,
					RequiredBytes:       requiredBytes,
					MemoryLoad:          in.MemoryLoad,
					AvailDiskBytes:      in.AvailDisk,
					MinAvailDiskBytes:   in.MinAvailDisk,
					GpuUtilization:      in.GpuUtilization,
					AvailVramBytes:      in.AvailVram,
					RequiredVramBytes:   requiredVram,
					IsGpuBlock:          true,
				}
			}
		}
	}

	return GatekeeperDecision{
		IsGo:                true,
		WaitDuration:        0,
		Reason:              "Approved",
		StorageMode:         in.StorageMode,
		EstimatedRamBytes:   in.EstimatedTaskRam,
		EffectiveAvailBytes: effectiveAvailBytes,
		RequiredBytes:       requiredBytes,
		MemoryLoad:          in.MemoryLoad,
		AvailDiskBytes:      in.AvailDisk,
		MinAvailDiskBytes:   in.MinAvailDisk,
		GpuUtilization:      in.GpuUtilization,
		AvailVramBytes:      in.AvailVram,
	}
}

// EvaluateGoNoGo queries live system memory, disk, and GPU status and delegates the preflight decision to EvaluateGoNoGoPure.
// SideEffectFn: EvaluateGoNoGo (IO Monad)
func (d *Dispatcher) EvaluateGoNoGo(workerID int, task TaskPayload) (bool, time.Duration) {
	memInfo, err := sysinfo.GetMemoryInfo()
	if err != nil || memInfo == nil {
		return true, 0
	}

	d.inFlightMutex.Lock()
	inFlight := d.activeInFlightRamBytes
	d.inFlightMutex.Unlock()

	currentCfg := d.GetConfig()
	minAvailBytes := uint64(currentCfg.MinAvailRamGB * 1024 * 1024 * 1024)
	minAvailDiskBytes := uint64(currentCfg.MinAvailDiskGB * 1024 * 1024 * 1024)
	minAvailVramBytes := uint64(currentCfg.MinAvailVramGB * 1024 * 1024 * 1024)
	estimatedVramBytes := uint64(currentCfg.EstimatedDemucsVramGB * 1024 * 1024 * 1024)
	if estimatedVramBytes == 0 {
		estimatedVramBytes = uint64(1.0 * 1024 * 1024 * 1024)
	}

	storageMode, effectiveTaskRam, estimatedDiskBytes := DetermineStorageModePure(
		task,
		memInfo.AvailPhys,
		inFlight,
		minAvailBytes,
		currentCfg.DiskModeRamThresholdRatio,
		currentCfg.EnableDiskModeFallback,
	)

	retryDelay := time.Duration(currentCfg.GatekeeperRetryDelaySec) * time.Second
	if retryDelay <= 0 {
		retryDelay = 20 * time.Second
	}

	// Disk space check: inspect queue_dir, temp dir, and source file dir
	var availDisk uint64 = math.MaxUint64
	if minAvailDiskBytes > 0 || storageMode == StorageModeDisk {
		checkPaths := []string{currentCfg.QueueDir, os.TempDir()}
		if task.FlacPath != "" {
			checkPaths = append(checkPaths, filepath.Dir(task.FlacPath))
		}
		for _, p := range checkPaths {
			if p == "" {
				continue
			}
			if dInfo, dErr := sysinfo.GetDiskFreeSpace(p); dErr == nil && dInfo != nil {
				if dInfo.FreeBytesAvailable < availDisk {
					availDisk = dInfo.FreeBytesAvailable
				}
			}
		}
	}

	// GPU metrics lookup
	gpuMetrics := sysinfo.GetLatestGpuMetrics()
	var gpuUtil float64 = 0.0
	var availVram uint64 = math.MaxUint64
	if gpuMetrics != nil {
		gpuUtil = gpuMetrics.UtilizationPercent
		availVram = gpuMetrics.AvailableVramBytes
	}

	input := GatekeeperInput{
		StorageMode:       storageMode,
		EstimatedTaskDisk: estimatedDiskBytes,
		AvailPhys:         memInfo.AvailPhys,
		InFlightRam:       inFlight,
		EstimatedTaskRam:  effectiveTaskRam,
		MinAvailRam:       minAvailBytes,
		MemoryLoad:        memInfo.MemoryLoad,
		AvailDisk:         availDisk,
		MinAvailDisk:      minAvailDiskBytes,
		GpuUtilization:    gpuUtil,
		AvailVram:         availVram,
		MinAvailVram:      minAvailVramBytes,
		EstimatedTaskVram: estimatedVramBytes,
		MaxGpuUtilization: currentCfg.MaxGpuUtilizationRatio,
		EnableGpuThrottle: currentCfg.EnableGpuThrottle,
		RetryDelay:        retryDelay,
	}

	decision := EvaluateGoNoGoPure(input)
	if !decision.IsGo {
		if decision.IsGpuBlock && d.statsTracker != nil {
			d.statsTracker.RecordGpuWait(decision.WaitDuration)
		}
		d.LogWarn("[W-%d] [Gatekeeper: NOGO] %s. Delaying dispatch for %v...", workerID, decision.Reason, decision.WaitDuration)
		return false, decision.WaitDuration
	}

	if storageMode == StorageModeDisk {
		d.LogInfo("[W-%d] [Gatekeeper: GO] Dispatch Approved [Disk Mode Fallback] (Task RAM Clamped: %d MB, Disk Needed: %.2f GB, Effective Avail RAM: %d MB [Avail: %d MB, InFlight: %d MB], GPU Util: %.1f%%, Avail Disk: %.2f GB)",
			workerID, decision.EstimatedRamBytes/1024/1024, float64(estimatedDiskBytes)/(1024*1024*1024), decision.EffectiveAvailBytes/1024/1024, memInfo.AvailPhys/1024/1024, inFlight/1024/1024, gpuUtil, float64(availDisk)/(1024*1024*1024))
	} else if minAvailDiskBytes > 0 {
		d.LogInfo("[W-%d] [Gatekeeper: GO] Dispatch Approved [SHM Mode] (Task RAM: %d MB, Effective Avail RAM: %d MB [Avail: %d MB, InFlight: %d MB], GPU Util: %.1f%%, Min Avail Disk: %.2f GB)",
			workerID, decision.EstimatedRamBytes/1024/1024, decision.EffectiveAvailBytes/1024/1024, memInfo.AvailPhys/1024/1024, inFlight/1024/1024, gpuUtil, float64(availDisk)/(1024*1024*1024))
	} else {
		d.LogInfo("[W-%d] [Gatekeeper: GO] Dispatch Approved [SHM Mode] (Task RAM: %d MB, Effective Avail RAM: %d MB [Avail: %d MB, InFlight: %d MB], GPU Util: %.1f%%)",
			workerID, decision.EstimatedRamBytes/1024/1024, decision.EffectiveAvailBytes/1024/1024, memInfo.AvailPhys/1024/1024, inFlight/1024/1024, gpuUtil)
	}
	return true, 0
}
