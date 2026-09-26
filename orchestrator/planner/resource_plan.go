// Package planner contains pure execution planning and resource admission
// calculations. It has no process, filesystem, network, or database effects.
package planner

import "math"

const (
	bytesPerSampleStereoFloat32 uint64 = 2 * 4
	defaultStemCount            uint64 = 7
	minAudioBufferBytes         uint64 = 1024 * 1024
	defaultFileExpansionRatio          = 3.5
	defaultPcmWorkingRatio             = 1.8
	defaultResidentRamBytes     uint64 = 1024 * 1024 * 1024
	diskModeRamBytes            uint64 = 2 * 1024 * 1024 * 1024
)

type StorageMode string

const (
	StorageModeSHM  StorageMode = "shm"
	StorageModeDisk StorageMode = "disk"
)

// TaskSpec is the resource-relevant subset of a dispatcher task.
type TaskSpec struct {
	FileSize    int64
	StartSample int64
	EndSample   int64
}

// StageProfile describes a Python stage or plugin branch. Resident memory is
// counted once, while working ratios compete by peak because the current
// worker daemon runs Librosa, Tensor, and Essentia sequentially per stem.
type StageProfile struct {
	Name                 string
	CPULane              bool
	ResidentRamBytes     uint64
	WorkingRamPerStemPCM float64
	ResidentVramBytes    uint64
	WorkingVramBytes     uint64
}

// ResourceProfile is the Go-side contract for the Python execution plan.
// Adding a plugin branch should add a profile entry, not hidden constants in
// the dispatcher.
type ResourceProfile struct {
	StemCount          uint64
	CPUParallelism     uint64
	FileExpansionRatio float64
	PcmWorkingRatio    float64
	DiskBytesPerStem   float64
	DiskModeRamBytes   uint64
	Stages             []StageProfile
}

type ResourceEstimate struct {
	StorageBufferBytes uint64
	StemBufferBytes    uint64
	CPUWorkingRamBytes uint64
	ShmRamBytes        uint64
	DiskBytes          uint64
	DiskModeRamBytes   uint64
	ResidentVramBytes  uint64
	WorkingVramBytes   uint64
	Overflow           bool
}

// WaveformOwner identifies where the CPU-visible master waveform resides. It
// deliberately excludes dedicated VRAM: CPU stages must retain an explicit
// HostRAM or DiskMmap owner.
type WaveformOwner uint8

const (
	WaveformOwnerHostRAM WaveformOwner = iota + 1
	WaveformOwnerDiskMmap
)

// GPUWorkPlacement identifies whether a bounded GPU transfer is safe. NoGPUWork
// means no CUDA allocation or launch; it is not a WDDM shared-memory fallback.
type GPUWorkPlacement uint8

const (
	GPUWorkNoGPUWork GPUWorkPlacement = iota
	GPUWorkDedicatedVRAM
)

// PlacementSnapshot is an immutable value-only observation for pure placement
// planning. Dedicated availability is valid only when it exactly agrees with
// total-used; shared GPU memory intentionally is not an input.
type PlacementSnapshot struct {
	AvailableHostRAMBytes   uint64
	AvailableDiskBytes      uint64
	ActiveTaskCount         uint32
	DedicatedTotalBytes     uint64
	DedicatedUsedBytes      uint64
	DedicatedAvailableBytes uint64
	DedicatedVRAMValid      bool
}

// PlacementPolicy is supplied by the caller. Enter thresholds are inclusive,
// exit thresholds are inclusive, and values strictly between retain the prior
// plan. No runtime configuration is read here.
type PlacementPolicy struct {
	HostRAMEnterBytes              uint64
	HostRAMExitBytes               uint64
	DedicatedVRAMEnterBytes        uint64
	DedicatedVRAMExitBytes         uint64
	HostRAMSafetyMarginBytes       uint64
	DedicatedVRAMSafetyMarginBytes uint64
	SingleTaskGPUChunkBytes        uint64
	ConcurrentTaskGPUChunkBytes    uint64
}

// PlacementPlan is immutable and comparable. Admissible=false is fail-closed;
// it always disables GPU work and exposes a zero transfer chunk.
type PlacementPlan struct {
	Admissible            bool
	CPUOwner              WaveformOwner
	GPUWork               GPUWorkPlacement
	GPUTransferChunkBytes uint64
}

// PlanWaveformPlacement creates a deterministic placement plan without
// reserving resources. ActiveTaskCount only selects the bounded GPU chunk.
func PlanWaveformPlacement(snapshot PlacementSnapshot, policy PlacementPolicy, prior PlacementPlan) PlacementPlan {
	if !validPlacementPolicy(policy) {
		return failClosedPlacement()
	}

	hostAvailable, hostOK := subtractSafetyMargin(snapshot.AvailableHostRAMBytes, policy.HostRAMSafetyMarginBytes)
	if !hostOK {
		return failClosedPlacement()
	}
	cpuOwner := selectWaveformOwner(hostAvailable, policy.HostRAMEnterBytes, policy.HostRAMExitBytes, prior.CPUOwner)
	if cpuOwner == WaveformOwnerDiskMmap && snapshot.AvailableDiskBytes == 0 {
		return failClosedPlacement()
	}

	plan := PlacementPlan{Admissible: true, CPUOwner: cpuOwner, GPUWork: GPUWorkNoGPUWork}
	if !snapshot.DedicatedVRAMValid || snapshot.DedicatedTotalBytes == ^uint64(0) || snapshot.DedicatedUsedBytes > snapshot.DedicatedTotalBytes ||
		snapshot.DedicatedAvailableBytes != snapshot.DedicatedTotalBytes-snapshot.DedicatedUsedBytes {
		return plan
	}
	dedicatedAvailable, dedicatedOK := subtractSafetyMargin(snapshot.DedicatedAvailableBytes, policy.DedicatedVRAMSafetyMarginBytes)
	if !dedicatedOK {
		return plan
	}

	chunk := policy.SingleTaskGPUChunkBytes
	if snapshot.ActiveTaskCount > 0 {
		chunk = policy.ConcurrentTaskGPUChunkBytes
	}
	if selectGPUWork(dedicatedAvailable, policy.DedicatedVRAMEnterBytes, policy.DedicatedVRAMExitBytes, prior.GPUWork) == GPUWorkDedicatedVRAM && dedicatedAvailable >= chunk {
		plan.GPUWork = GPUWorkDedicatedVRAM
		plan.GPUTransferChunkBytes = chunk
	}
	return plan
}

func validPlacementPolicy(policy PlacementPolicy) bool {
	return policy.HostRAMEnterBytes > policy.HostRAMExitBytes &&
		policy.DedicatedVRAMEnterBytes > policy.DedicatedVRAMExitBytes &&
		policy.SingleTaskGPUChunkBytes > 0 &&
		policy.ConcurrentTaskGPUChunkBytes > 0 &&
		policy.ConcurrentTaskGPUChunkBytes <= policy.SingleTaskGPUChunkBytes &&
		policy.SingleTaskGPUChunkBytes <= policy.DedicatedVRAMExitBytes
}

func subtractSafetyMargin(available, margin uint64) (uint64, bool) {
	if available < margin {
		return 0, false
	}
	return available - margin, true
}

func selectWaveformOwner(available, enter, exit uint64, prior WaveformOwner) WaveformOwner {
	if available >= enter {
		return WaveformOwnerHostRAM
	}
	if available <= exit {
		return WaveformOwnerDiskMmap
	}
	if prior == WaveformOwnerHostRAM || prior == WaveformOwnerDiskMmap {
		return prior
	}
	return WaveformOwnerDiskMmap
}

func selectGPUWork(available, enter, exit uint64, prior GPUWorkPlacement) GPUWorkPlacement {
	if available >= enter {
		return GPUWorkDedicatedVRAM
	}
	if available <= exit {
		return GPUWorkNoGPUWork
	}
	if prior == GPUWorkDedicatedVRAM {
		return GPUWorkDedicatedVRAM
	}
	return GPUWorkNoGPUWork
}

func failClosedPlacement() PlacementPlan {
	return PlacementPlan{CPUOwner: WaveformOwnerDiskMmap, GPUWork: GPUWorkNoGPUWork}
}

func DefaultResourceProfile() ResourceProfile {
	return ResourceProfile{
		StemCount:          defaultStemCount,
		FileExpansionRatio: defaultFileExpansionRatio,
		PcmWorkingRatio:    defaultPcmWorkingRatio,
		DiskBytesPerStem:   1.0,
		DiskModeRamBytes:   diskModeRamBytes,
		Stages: []StageProfile{
			{Name: "demucs", ResidentRamBytes: defaultResidentRamBytes, WorkingRamPerStemPCM: 1.8},
			{Name: "librosa", CPULane: true, WorkingRamPerStemPCM: 0.4},
			{Name: "tensor", WorkingRamPerStemPCM: 0.6, WorkingVramBytes: 512 * 1024 * 1024},
			{Name: "essentia", CPULane: true, WorkingRamPerStemPCM: 0.25},
		},
	}
}

func EstimateTaskResources(task TaskSpec, profile ResourceProfile) ResourceEstimate {
	if profile.StemCount == 0 {
		profile.StemCount = defaultStemCount
	}
	if profile.CPUParallelism == 0 {
		profile.CPUParallelism = 1
	}
	if profile.FileExpansionRatio <= 0 {
		profile.FileExpansionRatio = defaultFileExpansionRatio
	}
	if profile.PcmWorkingRatio <= 0 {
		profile.PcmWorkingRatio = defaultPcmWorkingRatio
	}
	if profile.DiskBytesPerStem <= 0 {
		profile.DiskBytesPerStem = 1.0
	}
	if profile.DiskModeRamBytes == 0 {
		profile.DiskModeRamBytes = diskModeRamBytes
	}

	baseAudioBytes, ok := estimateAudioBufferBytes(task, profile.FileExpansionRatio)
	if !ok {
		return overflowedResourceEstimate()
	}
	stemBytes, ok := checkedMul(baseAudioBytes, profile.StemCount)
	if !ok {
		return overflowedResourceEstimate()
	}
	residentRam := uint64(0)
	peakWorkingRatio := profile.PcmWorkingRatio
	peakCPUWorkingRatio := 0.0
	residentVram := uint64(0)
	workingVram := uint64(0)
	for _, stage := range profile.Stages {
		residentRam, ok = checkedAdd(residentRam, stage.ResidentRamBytes)
		if !ok {
			return overflowedResourceEstimate()
		}
		if stage.WorkingRamPerStemPCM > peakWorkingRatio {
			peakWorkingRatio = stage.WorkingRamPerStemPCM
		}
		if stage.CPULane && stage.WorkingRamPerStemPCM > peakCPUWorkingRatio {
			peakCPUWorkingRatio = stage.WorkingRamPerStemPCM
		}
		residentVram, ok = checkedAdd(residentVram, stage.ResidentVramBytes)
		if !ok {
			return overflowedResourceEstimate()
		}
		if stage.WorkingVramBytes > workingVram {
			workingVram = stage.WorkingVramBytes
		}
	}

	workingRam, ok := checkedScale(stemBytes, peakWorkingRatio)
	if !ok {
		return overflowedResourceEstimate()
	}
	cpuPerConsumer, ok := checkedScale(baseAudioBytes, peakCPUWorkingRatio)
	if !ok {
		return overflowedResourceEstimate()
	}
	cpuWorking, ok := checkedMul(cpuPerConsumer, profile.CPUParallelism)
	if !ok {
		return overflowedResourceEstimate()
	}
	if cpuWorking > workingRam {
		workingRam = cpuWorking
	}
	diskModeRam, ok := checkedAdd(cpuWorking, residentRam)
	if !ok {
		return overflowedResourceEstimate()
	}
	diskModeRam = max(diskModeRam, profile.DiskModeRamBytes)
	shmRam, ok := checkedAdd(workingRam, residentRam)
	if !ok {
		return overflowedResourceEstimate()
	}
	diskBytes, ok := checkedScale(stemBytes, profile.DiskBytesPerStem)
	if !ok {
		return overflowedResourceEstimate()
	}
	return ResourceEstimate{
		StorageBufferBytes: baseAudioBytes,
		StemBufferBytes:    stemBytes,
		CPUWorkingRamBytes: cpuWorking,
		ShmRamBytes:        shmRam,
		DiskBytes:          diskBytes,
		DiskModeRamBytes:   diskModeRam,
		ResidentVramBytes:  residentVram,
		WorkingVramBytes:   workingVram,
	}
}

func SelectStorageMode(estimate ResourceEstimate, availPhys, inFlightRam, minAvailRam uint64, thresholdRatio float64, enableDiskFallback bool) (StorageMode, uint64, uint64) {
	if estimate.Overflow {
		return StorageModeSHM, math.MaxUint64, math.MaxUint64
	}
	if !enableDiskFallback {
		return StorageModeSHM, estimate.ShmRamBytes, 0
	}
	if thresholdRatio <= 0 || thresholdRatio > 1 {
		thresholdRatio = 0.8
	}

	effectiveAvail := uint64(0)
	if availPhys > inFlightRam {
		effectiveAvail = availPhys - inFlightRam
	}
	requiredWithMin, ok := checkedAdd(estimate.ShmRamBytes, minAvailRam)
	if !ok {
		return StorageModeDisk, estimate.DiskModeRamBytes, math.MaxUint64
	}
	safeThreshold := uint64(float64(effectiveAvail) * thresholdRatio)
	if requiredWithMin > safeThreshold || effectiveAvail < requiredWithMin {
		return StorageModeDisk, estimate.DiskModeRamBytes, estimate.DiskBytes
	}
	return StorageModeSHM, estimate.ShmRamBytes, 0
}

func estimateAudioBufferBytes(task TaskSpec, expansionRatio float64) (uint64, bool) {
	if task.StartSample >= 0 && task.EndSample > task.StartSample {
		numSamples := uint64(task.EndSample - task.StartSample)
		pcmBytes, ok := checkedMul(numSamples, bytesPerSampleStereoFloat32)
		if !ok {
			return 0, false
		}
		estimated, ok := checkedScale(pcmBytes, 1.5)
		if !ok {
			return 0, false
		}
		if estimated < minAudioBufferBytes {
			return minAudioBufferBytes, true
		}
		return estimated, true
	}
	if task.FileSize < 0 {
		return 0, false
	}
	estimated, ok := checkedScale(uint64(task.FileSize), expansionRatio)
	if !ok {
		return 0, false
	}
	if estimated < minAudioBufferBytes {
		return minAudioBufferBytes, true
	}
	return estimated, true
}

func checkedAdd(a, b uint64) (uint64, bool) {
	if b > math.MaxUint64-a {
		return 0, false
	}
	return a + b, true
}

func checkedMul(a, b uint64) (uint64, bool) {
	if b != 0 && a > math.MaxUint64/b {
		return 0, false
	}
	return a * b, true
}

func checkedScale(value uint64, ratio float64) (uint64, bool) {
	if ratio < 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return 0, false
	}
	scaled := float64(value) * ratio
	if math.IsInf(scaled, 0) || scaled >= float64(math.MaxUint64) {
		return 0, false
	}
	return uint64(scaled), true
}

func overflowedResourceEstimate() ResourceEstimate {
	return ResourceEstimate{Overflow: true}
}
