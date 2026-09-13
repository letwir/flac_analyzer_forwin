package planner

import (
	"reflect"
	"sync"
	"testing"
)

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

const gib = uint64(1024 * 1024 * 1024)

func defaultPlacementPolicy() PlacementPolicy {
	return PlacementPolicy{
		HostRAMEnterBytes: 8 * gib, HostRAMExitBytes: 4 * gib,
		DedicatedVRAMEnterBytes: 6 * gib, DedicatedVRAMExitBytes: 3 * gib,
		HostRAMSafetyMarginBytes: gib, DedicatedVRAMSafetyMarginBytes: gib,
		SingleTaskGPUChunkBytes: 2 * gib, ConcurrentTaskGPUChunkBytes: gib,
	}
}

func validPlacementSnapshot() PlacementSnapshot {
	return PlacementSnapshot{
		AvailableHostRAMBytes: 10 * gib, AvailableDiskBytes: 20 * gib,
		DedicatedTotalBytes: 16 * gib, DedicatedUsedBytes: 4 * gib,
		DedicatedAvailableBytes: 12 * gib, DedicatedVRAMValid: true,
	}
}

func TestPlanWaveformPlacementContract(t *testing.T) {
	policy := defaultPlacementPolicy()
	prior := PlacementPlan{CPUOwner: WaveformOwnerHostRAM, GPUWork: GPUWorkDedicatedVRAM}
	cases := []struct {
		name     string
		snapshot PlacementSnapshot
		prior    PlacementPlan
		want     PlacementPlan
	}{
		{"sufficient", validPlacementSnapshot(), prior, PlacementPlan{true, WaveformOwnerHostRAM, GPUWorkDedicatedVRAM, 2 * gib}},
		{"active-task-bounds-chunk", func() PlacementSnapshot { s := validPlacementSnapshot(); s.ActiveTaskCount = 1; return s }(), prior, PlacementPlan{true, WaveformOwnerHostRAM, GPUWorkDedicatedVRAM, gib}},
		{"low-host-uses-disk", func() PlacementSnapshot { s := validPlacementSnapshot(); s.AvailableHostRAMBytes = 4 * gib; return s }(), prior, PlacementPlan{true, WaveformOwnerDiskMmap, GPUWorkDedicatedVRAM, 2 * gib}},
		{"unknown-vram-disables-gpu", func() PlacementSnapshot { s := validPlacementSnapshot(); s.DedicatedVRAMValid = false; return s }(), prior, PlacementPlan{true, WaveformOwnerHostRAM, GPUWorkNoGPUWork, 0}},
		{"inconsistent-vram-disables-gpu", func() PlacementSnapshot { s := validPlacementSnapshot(); s.DedicatedAvailableBytes++; return s }(), prior, PlacementPlan{true, WaveformOwnerHostRAM, GPUWorkNoGPUWork, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlanWaveformPlacement(tc.snapshot, policy, tc.prior); got != tc.want {
				t.Fatalf("plan = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestPlanWaveformPlacementHysteresisBoundaries(t *testing.T) {
	policy := defaultPlacementPolicy()
	snapshot := validPlacementSnapshot()
	prior := PlacementPlan{CPUOwner: WaveformOwnerDiskMmap, GPUWork: GPUWorkNoGPUWork}
	snapshot.AvailableHostRAMBytes = policy.HostRAMEnterBytes + policy.HostRAMSafetyMarginBytes
	snapshot.DedicatedAvailableBytes = policy.DedicatedVRAMEnterBytes + policy.DedicatedVRAMSafetyMarginBytes
	snapshot.DedicatedTotalBytes = snapshot.DedicatedUsedBytes + snapshot.DedicatedAvailableBytes
	if got := PlanWaveformPlacement(snapshot, policy, prior); got.CPUOwner != WaveformOwnerHostRAM || got.GPUWork != GPUWorkDedicatedVRAM {
		t.Fatalf("enter boundary did not promote: %#v", got)
	}
	snapshot.AvailableHostRAMBytes = policy.HostRAMExitBytes + policy.HostRAMSafetyMarginBytes
	snapshot.DedicatedAvailableBytes = policy.DedicatedVRAMExitBytes + policy.DedicatedVRAMSafetyMarginBytes
	snapshot.DedicatedTotalBytes = snapshot.DedicatedUsedBytes + snapshot.DedicatedAvailableBytes
	prior = PlacementPlan{CPUOwner: WaveformOwnerHostRAM, GPUWork: GPUWorkDedicatedVRAM}
	if got := PlanWaveformPlacement(snapshot, policy, prior); got.CPUOwner != WaveformOwnerDiskMmap || got.GPUWork != GPUWorkNoGPUWork || got.GPUTransferChunkBytes != 0 {
		t.Fatalf("exit boundary did not demote: %#v", got)
	}
}

func TestPlanWaveformPlacementRetainsPriorBetweenThresholds(t *testing.T) {
	policy := defaultPlacementPolicy()
	snapshot := validPlacementSnapshot()
	snapshot.AvailableHostRAMBytes = 6*gib + policy.HostRAMSafetyMarginBytes
	snapshot.DedicatedAvailableBytes = 4*gib + policy.DedicatedVRAMSafetyMarginBytes
	snapshot.DedicatedTotalBytes = snapshot.DedicatedUsedBytes + snapshot.DedicatedAvailableBytes
	prior := PlacementPlan{CPUOwner: WaveformOwnerHostRAM, GPUWork: GPUWorkDedicatedVRAM}
	if got := PlanWaveformPlacement(snapshot, policy, prior); got.CPUOwner != WaveformOwnerHostRAM || got.GPUWork != GPUWorkDedicatedVRAM {
		t.Fatalf("middle band did not retain prior: %#v", got)
	}
}

func TestPlanWaveformPlacementFailsClosed(t *testing.T) {
	policy := defaultPlacementPolicy()
	snapshot := validPlacementSnapshot()
	policy.HostRAMEnterBytes = policy.HostRAMExitBytes
	if got := PlanWaveformPlacement(snapshot, policy, PlacementPlan{}); got.Admissible || got.GPUWork != GPUWorkNoGPUWork || got.GPUTransferChunkBytes != 0 {
		t.Fatalf("invalid policy must fail closed: %#v", got)
	}
	policy = defaultPlacementPolicy()
	policy.SingleTaskGPUChunkBytes = policy.DedicatedVRAMExitBytes + 1
	if got := PlanWaveformPlacement(snapshot, policy, PlacementPlan{}); got.Admissible || got.GPUWork != GPUWorkNoGPUWork || got.GPUTransferChunkBytes != 0 {
		t.Fatalf("chunk larger than the exit threshold must fail closed: %#v", got)
	}
	policy = defaultPlacementPolicy()
	snapshot.AvailableHostRAMBytes = 0
	if got := PlanWaveformPlacement(snapshot, policy, PlacementPlan{}); got.Admissible || got.GPUWork != GPUWorkNoGPUWork {
		t.Fatalf("margin underflow must fail closed: %#v", got)
	}
	snapshot = validPlacementSnapshot()
	snapshot.DedicatedTotalBytes = ^uint64(0)
	snapshot.DedicatedUsedBytes = 1
	snapshot.DedicatedAvailableBytes = ^uint64(0) - 1
	if got := PlanWaveformPlacement(snapshot, policy, PlacementPlan{}); got.GPUWork != GPUWorkNoGPUWork {
		t.Fatalf("overflow-risk totals must disable GPU work: %#v", got)
	}
}

func TestPlacementTypesAreValueOnlyAndPure(t *testing.T) {
	types := []reflect.Type{reflect.TypeOf(PlacementSnapshot{}), reflect.TypeOf(PlacementPolicy{}), reflect.TypeOf(PlacementPlan{})}
	for _, typ := range types {
		if !typ.Comparable() {
			t.Fatalf("%s must be comparable", typ)
		}
		for i := 0; i < typ.NumField(); i++ {
			kind := typ.Field(i).Type.Kind()
			if kind == reflect.Map || kind == reflect.Slice || kind == reflect.Pointer {
				t.Fatalf("%s.%s must be value-only", typ, typ.Field(i).Name)
			}
		}
	}
	policy, snapshot := defaultPlacementPolicy(), validPlacementSnapshot()
	beforePolicy, beforeSnapshot := policy, snapshot
	want := PlanWaveformPlacement(snapshot, policy, PlacementPlan{})
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := PlanWaveformPlacement(snapshot, policy, PlacementPlan{}); got != want {
				t.Errorf("non-deterministic plan: %#v want %#v", got, want)
			}
		}()
	}
	wg.Wait()
	if policy != beforePolicy || snapshot != beforeSnapshot {
		t.Fatal("pure planner mutated its input")
	}
}
