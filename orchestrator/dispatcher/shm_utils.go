package dispatcher

// EstimateShmSize calculates the required shared memory size for a single stem
// based on the original FLAC file size and default expansion ratio (3.5).
func EstimateShmSize(fileSize int64) uint32 {
	return EstimateShmSizeWithRatio(fileSize, 3.5)
}

// EstimateShmSizeWithRatio calculates the required shared memory size using a configurable expansion ratio.
func EstimateShmSizeWithRatio(fileSize int64, ratio float64) uint32 {
	if ratio <= 0 {
		ratio = 3.5
	}
	estimated := int64(float64(fileSize) * ratio)
	
	if estimated < 1024*1024 {
		estimated = 1024 * 1024
	}
	
	return uint32(estimated)
}

// EstimateShmSizeForTask calculates the required shared memory size for a single stem using default ratio.
func EstimateShmSizeForTask(task TaskPayload) uint32 {
	return EstimateShmSizeForTaskWithRatio(task, 3.5)
}

// EstimateShmSizeForTaskWithRatio calculates the required shared memory size using configurable ratio.
func EstimateShmSizeForTaskWithRatio(task TaskPayload, ratio float64) uint32 {
	if task.StartSample >= 0 && task.EndSample > task.StartSample {
		// CUE track calculation: numSamples * 2 (channels) * 4 (float32 bytes) * 1.5 (safety margin)
		numSamples := uint64(task.EndSample - task.StartSample)
		neededBytes := numSamples * 2 * 4
		estimated := uint64(float64(neededBytes) * 1.5)
		if estimated < 1024*1024 {
			estimated = 1024 * 1024
		}
		return uint32(estimated)
	}
	return EstimateShmSizeWithRatio(task.FileSize, ratio)
}


// EstimateDemucsTotalRamBytes estimates the total memory (stems + PyTorch buffer + processing margin)
// required for Demucs and feature extraction based on CUE track samples or FLAC file size.
func EstimateDemucsTotalRamBytes(task TaskPayload) uint64 {
	stemsCount := uint64(7) // mix, bass, drums, vocals, other, guitar, piano
	var basePcmBytes uint64

	if task.StartSample >= 0 && task.EndSample > task.StartSample {
		// CUE track calculation: samples * 2 (channels) * 4 (float32 bytes)
		numSamples := uint64(task.EndSample - task.StartSample)
		basePcmBytes = numSamples * 2 * 4
	} else {
		// Standalone FLAC fallback: estimated single stem size * stemsCount
		singleStemShm := uint64(EstimateShmSize(task.FileSize))
		basePcmBytes = singleStemShm
	}

	// Total PCM buffer for all stems
	totalStemsRam := basePcmBytes * stemsCount

	// Safety margin factor (1.8x for PyTorch intermediate tensors & Librosa buffers)
	// Plus 1.0 GB fixed footprint for PyTorch model weights & runtime
	fixedFootprintBytes := uint64(1024 * 1024 * 1024)
	estimatedTotal := uint64(float64(totalStemsRam)*1.8) + fixedFootprintBytes

	return estimatedTotal
}
