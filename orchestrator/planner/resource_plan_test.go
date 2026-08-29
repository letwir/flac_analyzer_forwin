package planner

import "testing"

func TestEstimateTaskResourcesIncludesStagePlan(t *testing.T) {
	estimate := EstimateTaskResources(TaskSpec{StartSample: 0, EndSample: 44100 * 60}, DefaultResourceProfile())
	if estimate.StemBufferBytes == 0 || estimate.ShmRamBytes <= estimate.StemBufferBytes {
		t.Fatalf("expected stage-aware RAM estimate above stem buffers: %#v", estimate)
	}
	if estimate.DiskBytes == 0 || estimate.WorkingVramBytes == 0 {
		t.Fatalf("expected disk and tensor VRAM estimates: %#v", estimate)
	}
}

func TestSelectStorageModeSeparatesDiskRamFloor(t *testing.T) {
	estimate := ResourceEstimate{
		ShmRamBytes:      8 * 1024 * 1024 * 1024,
		DiskModeRamBytes: 2 * 1024 * 1024 * 1024,
		DiskBytes:        10 * 1024 * 1024 * 1024,
	}
	mode, ram, disk := SelectStorageMode(estimate, 4*1024*1024*1024, 0, 2*1024*1024*1024, 0.8, true)
	if mode != StorageModeDisk || ram != estimate.DiskModeRamBytes || disk != estimate.DiskBytes {
		t.Fatalf("expected Disk Mode fallback, got mode=%s ram=%d disk=%d", mode, ram, disk)
	}
}
