package dispatcher

// EstimateShmSize calculates the required shared memory size for a single stem
// based on the original FLAC file size.
// It assumes the decoded float32 stereo PCM size will not exceed 6 times the FLAC file size.
func EstimateShmSize(fileSize int64) uint32 {
	// A typical FLAC compression ratio is ~60%.
	// A 16-bit 44.1kHz stereo file size is about ~10.5 MB/min.
	// Float32 (32-bit) PCM is exactly twice the size of 16-bit PCM.
	// So 32-bit PCM size is roughly 3x to 4x the FLAC size.
	// We multiply by 6 to ensure a safe margin.
	marginMultiplier := int64(6)
	estimated := fileSize * marginMultiplier
	
	// Set a reasonable minimum (e.g. 1MB) just in case
	if estimated < 1024*1024 {
		estimated = 1024 * 1024
	}
	
	// Cast to uint32. Our NewSharedMemory takes uint32.
	return uint32(estimated)
}
