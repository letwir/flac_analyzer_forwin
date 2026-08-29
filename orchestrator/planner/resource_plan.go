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
