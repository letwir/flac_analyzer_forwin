// Package planner contains pure execution planning and resource admission
// calculations. It has no process, filesystem, network, or database effects.
package planner

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
	FileExpansionRatio float64
	PcmWorkingRatio    float64
	DiskBytesPerStem   float64
	DiskModeRamBytes   uint64
	Stages             []StageProfile
}

type ResourceEstimate struct {
	StorageBufferBytes uint64
	StemBufferBytes    uint64
	ShmRamBytes        uint64
	DiskBytes          uint64
	DiskModeRamBytes   uint64
	ResidentVramBytes  uint64
	WorkingVramBytes   uint64
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
			{Name: "librosa", WorkingRamPerStemPCM: 0.4},
			{Name: "tensor", WorkingRamPerStemPCM: 0.6, WorkingVramBytes: 512 * 1024 * 1024},
			{Name: "essentia", WorkingRamPerStemPCM: 0.25},
		},
	}
}

func EstimateTaskResources(task TaskSpec, profile ResourceProfile) ResourceEstimate {
	if profile.StemCount == 0 {
		profile.StemCount = defaultStemCount
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

	baseAudioBytes := estimateAudioBufferBytes(task, profile.FileExpansionRatio)
	stemBytes := baseAudioBytes * profile.StemCount
	residentRam := uint64(0)
	peakWorkingRatio := profile.PcmWorkingRatio
	residentVram := uint64(0)
	workingVram := uint64(0)
	for _, stage := range profile.Stages {
		residentRam += stage.ResidentRamBytes
		if stage.WorkingRamPerStemPCM > peakWorkingRatio {
			peakWorkingRatio = stage.WorkingRamPerStemPCM
		}
		residentVram += stage.ResidentVramBytes
		if stage.WorkingVramBytes > workingVram {
			workingVram = stage.WorkingVramBytes
		}
	}

	return ResourceEstimate{
		StorageBufferBytes: baseAudioBytes,
		StemBufferBytes:    stemBytes,
		ShmRamBytes:        uint64(float64(stemBytes)*peakWorkingRatio) + residentRam,
		DiskBytes:          uint64(float64(stemBytes) * profile.DiskBytesPerStem),
		DiskModeRamBytes:   profile.DiskModeRamBytes,
		ResidentVramBytes:  residentVram,
		WorkingVramBytes:   workingVram,
	}
}

func SelectStorageMode(estimate ResourceEstimate, availPhys, inFlightRam, minAvailRam uint64, thresholdRatio float64, enableDiskFallback bool) (StorageMode, uint64, uint64) {
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
	requiredWithMin := estimate.ShmRamBytes + minAvailRam
	safeThreshold := uint64(float64(effectiveAvail) * thresholdRatio)
	if requiredWithMin > safeThreshold || effectiveAvail < requiredWithMin {
		return StorageModeDisk, estimate.DiskModeRamBytes, estimate.DiskBytes
	}
	return StorageModeSHM, estimate.ShmRamBytes, 0
}

func estimateAudioBufferBytes(task TaskSpec, expansionRatio float64) uint64 {
	if task.StartSample >= 0 && task.EndSample > task.StartSample {
		numSamples := uint64(task.EndSample - task.StartSample)
		estimated := uint64(float64(numSamples*bytesPerSampleStereoFloat32) * 1.5)
		if estimated < minAudioBufferBytes {
			return minAudioBufferBytes
		}
		return estimated
	}
	estimated := int64(float64(task.FileSize) * expansionRatio)
	if estimated < int64(minAudioBufferBytes) {
		return minAudioBufferBytes
	}
	return uint64(estimated)
}
