package dispatcher

import (
	"time"

	"flac_analyzer/orchestrator/planner"
)

// StorageMode represents the waveform IPC transfer mechanism (Zero-copy SHM or Disk Spooling).
type StorageMode = planner.StorageMode

const (
	StorageModeSHM  = planner.StorageModeSHM
	StorageModeDisk = planner.StorageModeDisk
)

// EstimateShmSize calculates the required shared memory size for a single stem
// based on the original FLAC file size and default expansion ratio (3.5).
func EstimateShmSize(fileSize int64) uint64 {
	return EstimateShmSizeWithRatio(fileSize, 3.5)
}

// EstimateShmSizeWithRatio calculates the required shared memory size using a configurable expansion ratio.
func EstimateShmSizeWithRatio(fileSize int64, ratio float64) uint64 {
	if ratio <= 0 {
		ratio = 3.5
	}
	estimated := int64(float64(fileSize) * ratio)

	if estimated < 1024*1024 {
		estimated = 1024 * 1024
	}

	return uint64(estimated)
}

// EstimateShmSizeForTask calculates the required shared memory size for a single stem using default ratio.
func EstimateShmSizeForTask(task TaskPayload) uint64 {
	return EstimateShmSizeForTaskWithRatio(task, 3.5)
}

// EstimateShmSizeForTaskWithRatio calculates the required shared memory size using configurable ratio.
func EstimateShmSizeForTaskWithRatio(task TaskPayload, ratio float64) uint64 {
	if task.StartSample >= 0 && task.EndSample > task.StartSample {
		// CUE track calculation: numSamples * 2 (channels) * 4 (float32 bytes) * 1.5 (safety margin)
		numSamples := uint64(task.EndSample - task.StartSample)
		neededBytes := numSamples * 2 * 4
		estimated := uint64(float64(neededBytes) * 1.5)
		if estimated < 1024*1024 {
			estimated = 1024 * 1024
		}
		return estimated
	}
	return EstimateShmSizeWithRatio(task.FileSize, ratio)
}

// EstimateDemucsDiskBytes calculates the total SSD disk space required for all 7 stems in Disk Mode.
func EstimateDemucsDiskBytes(task TaskPayload) uint64 {
	estimate := planner.EstimateTaskResources(toPlannerTask(task), planner.DefaultResourceProfile())
	return estimate.DiskBytes
}

// EstimateDemucsTotalRamBytes estimates the total memory (stems + PyTorch buffer + processing margin)
// required for Demucs and feature extraction based on CUE track samples or FLAC file size.
func EstimateDemucsTotalRamBytes(task TaskPayload) uint64 {
	estimate := planner.EstimateTaskResources(toPlannerTask(task), planner.DefaultResourceProfile())
	return estimate.ShmRamBytes
}

// DetermineStorageModePure evaluates whether a task can be processed in Zero-copy SHM
// or should fall back to SSD Disk Spooling Mode (Disk Mode) without side-effects.
func DetermineStorageModePure(
	task TaskPayload,
	availPhys uint64,
	inFlightRam uint64,
	minAvailRam uint64,
	diskModeThresholdRatio float64,
	enableDiskFallback bool,
) (mode StorageMode, effectiveTaskRam uint64, estimatedDisk uint64) {
	estimate := planner.EstimateTaskResources(toPlannerTask(task), planner.DefaultResourceProfile())
	return planner.SelectStorageMode(estimate, availPhys, inFlightRam, minAvailRam, diskModeThresholdRatio, enableDiskFallback)
}

func toPlannerTask(task TaskPayload) planner.TaskSpec {
	return planner.TaskSpec{FileSize: task.FileSize, StartSample: task.StartSample, EndSample: task.EndSample}
}

// ComputeAdaptiveTimeoutPure calculates a dynamic, safe timeout duration scaled to track length.
// Mor: (TaskPayload, BaseTimeoutSec, Ratio, MaxTimeoutSec) -> time.Duration
// PureMorph: ComputeAdaptiveTimeoutPure
func ComputeAdaptiveTimeoutPure(
	task TaskPayload,
	baseTimeoutSec int,
	ratio float64,
	maxTimeoutSec int,
) time.Duration {
	if baseTimeoutSec <= 0 {
		baseTimeoutSec = 300
	}
	if ratio <= 0 {
		ratio = 1.5
	}
	if maxTimeoutSec <= 0 {
		maxTimeoutSec = 7200
	}
	if maxTimeoutSec < baseTimeoutSec {
		maxTimeoutSec = baseTimeoutSec
	}

	var trackSec float64
	sampleRate := task.SampleRate
	if sampleRate <= 0 {
		sampleRate = 44100
	}
	if task.EndSample > task.StartSample {
		// CUE/FLAC metadata supplies exact sample bounds and sample rate when available.
		trackSec = float64(task.EndSample-task.StartSample) / float64(sampleRate)
	} else if task.FileSize > 0 {
		// Single whole FLAC file. 16-bit 44.1kHz stereo PCM ≈ 176.4 KB/s.
		// For high-res/compressed FLAC, provide conservative lower-bound of 600s.
		estimatedSec := float64(task.FileSize) / 176400.0
		if estimatedSec < 600.0 {
			estimatedSec = 600.0
		}
		trackSec = estimatedSec
	} else {
		trackSec = 300.0
	}

	adaptiveSec := float64(baseTimeoutSec) + (trackSec * ratio)
	if adaptiveSec < float64(baseTimeoutSec) {
		adaptiveSec = float64(baseTimeoutSec)
	}
	if adaptiveSec > float64(maxTimeoutSec) {
		adaptiveSec = float64(maxTimeoutSec)
	}

	return time.Duration(adaptiveSec) * time.Second
}
