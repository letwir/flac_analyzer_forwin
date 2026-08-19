package dispatcher

import (
	"testing"
	"time"
)

func TestEvaluateGoNoGoPure_Approved(t *testing.T) {
	availPhys := uint64(32 * 1024 * 1024 * 1024)   // 32 GB
	inFlight := uint64(2 * 1024 * 1024 * 1024)     // 2 GB
	estimatedRam := uint64(3 * 1024 * 1024 * 1024) // 3 GB
	minAvailRam := uint64(3 * 1024 * 1024 * 1024)  // 3 GB
	memLoad := uint32(50)                          // 50%
	retryDelay := 20 * time.Second
	availDisk := uint64(50 * 1024 * 1024 * 1024)   // 50 GB
	minAvailDisk := uint64(5 * 1024 * 1024 * 1024) // 5 GB

	input := GatekeeperInput{
		AvailPhys:         availPhys,
		InFlightRam:       inFlight,
		EstimatedTaskRam:  estimatedRam,
		MinAvailRam:       minAvailRam,
		MemoryLoad:        memLoad,
		AvailDisk:         availDisk,
		MinAvailDisk:      minAvailDisk,
		GpuUtilization:    45.0,
		AvailVram:         4 * 1024 * 1024 * 1024,
		MinAvailVram:      512 * 1024 * 1024,
		EstimatedTaskVram: 1024 * 1024 * 1024,
		MaxGpuUtilization: 0.85,
		EnableGpuThrottle: true,
		RetryDelay:        retryDelay,
	}

	decision := EvaluateGoNoGoPure(input)

	if !decision.IsGo {
		t.Fatalf("Expected IsGo=true, got false (reason: %s)", decision.Reason)
	}
	if decision.WaitDuration != 0 {
		t.Fatalf("Expected WaitDuration=0, got %v", decision.WaitDuration)
	}
	if decision.EffectiveAvailBytes != availPhys-inFlight {
		t.Fatalf("Expected EffectiveAvailBytes=%d, got %d", availPhys-inFlight, decision.EffectiveAvailBytes)
	}
}

func TestEvaluateGoNoGoPure_DiskSpaceInsufficient(t *testing.T) {
	input := GatekeeperInput{
		AvailPhys:         32 * 1024 * 1024 * 1024,
		InFlightRam:       0,
		EstimatedTaskRam:  1 * 1024 * 1024 * 1024,
		MinAvailRam:       1 * 1024 * 1024 * 1024,
		MemoryLoad:        30,
		AvailDisk:         3 * 1024 * 1024 * 1024, // 3 GB (< 5 GB MinAvailDisk)
		MinAvailDisk:      5 * 1024 * 1024 * 1024,
		GpuUtilization:    20.0,
		AvailVram:         4 * 1024 * 1024 * 1024,
		MinAvailVram:      512 * 1024 * 1024,
		EstimatedTaskVram: 1024 * 1024 * 1024,
		MaxGpuUtilization: 0.85,
		EnableGpuThrottle: true,
		RetryDelay:        20 * time.Second,
	}

	decision := EvaluateGoNoGoPure(input)

	if decision.IsGo {
		t.Fatalf("Expected IsGo=false for insufficient disk space, got true")
	}
	if decision.WaitDuration != 20*time.Second {
		t.Fatalf("Expected WaitDuration=20s, got %v", decision.WaitDuration)
	}
	if decision.AvailDiskBytes != 3*1024*1024*1024 {
		t.Fatalf("Expected AvailDiskBytes=3GB, got %d", decision.AvailDiskBytes)
	}
}

func TestEvaluateGoNoGoPure_MemoryInsufficient(t *testing.T) {
	input := GatekeeperInput{
		AvailPhys:         6 * 1024 * 1024 * 1024,  // 6 GB
		InFlightRam:       2 * 1024 * 1024 * 1024,  // 2 GB (Effective Avail = 4 GB)
		EstimatedTaskRam:  3 * 1024 * 1024 * 1024,  // 3 GB
		MinAvailRam:       2 * 1024 * 1024 * 1024,  // 2 GB (Required = 5 GB)
		MemoryLoad:        60,
		AvailDisk:         50 * 1024 * 1024 * 1024,
		MinAvailDisk:      5 * 1024 * 1024 * 1024,
		GpuUtilization:    10.0,
		AvailVram:         4 * 1024 * 1024 * 1024,
		MinAvailVram:      512 * 1024 * 1024,
		EstimatedTaskVram: 1024 * 1024 * 1024,
		MaxGpuUtilization: 0.85,
		EnableGpuThrottle: true,
		RetryDelay:        20 * time.Second,
	}

	decision := EvaluateGoNoGoPure(input)

	if decision.IsGo {
		t.Fatalf("Expected IsGo=false for insufficient RAM, got true")
	}
	if decision.WaitDuration != 20*time.Second {
		t.Fatalf("Expected WaitDuration=20s, got %v", decision.WaitDuration)
	}
	if decision.EffectiveAvailBytes != 4*1024*1024*1024 {
		t.Fatalf("Expected EffectiveAvailBytes=4GB, got %d", decision.EffectiveAvailBytes)
	}
	if decision.RequiredBytes != 5*1024*1024*1024 {
		t.Fatalf("Expected RequiredBytes=5GB, got %d", decision.RequiredBytes)
	}
}

func TestEvaluateGoNoGoPure_InFlightUnderflowGuard(t *testing.T) {
	input := GatekeeperInput{
		AvailPhys:         2 * 1024 * 1024 * 1024, // 2 GB
		InFlightRam:       5 * 1024 * 1024 * 1024, // 5 GB (InFlight > AvailPhys)
		EstimatedTaskRam:  1 * 1024 * 1024 * 1024,
		MinAvailRam:       1 * 1024 * 1024 * 1024,
		MemoryLoad:        85,
		AvailDisk:         50 * 1024 * 1024 * 1024,
		MinAvailDisk:      5 * 1024 * 1024 * 1024,
		GpuUtilization:    10.0,
		AvailVram:         4 * 1024 * 1024 * 1024,
		MinAvailVram:      512 * 1024 * 1024,
		EstimatedTaskVram: 1024 * 1024 * 1024,
		MaxGpuUtilization: 0.85,
		EnableGpuThrottle: true,
		RetryDelay:        20 * time.Second,
	}

	decision := EvaluateGoNoGoPure(input)

	if decision.IsGo {
		t.Fatalf("Expected IsGo=false when InFlight > AvailPhys, got true")
	}
	if decision.EffectiveAvailBytes != 0 {
		t.Fatalf("Expected EffectiveAvailBytes=0 (underflow guard), got %d", decision.EffectiveAvailBytes)
	}
	if decision.WaitDuration != 20*time.Second {
		t.Fatalf("Expected WaitDuration=20s, got %v", decision.WaitDuration)
	}
}

func TestEvaluateGoNoGoPure_HighMemoryLoad(t *testing.T) {
	input := GatekeeperInput{
		AvailPhys:         32 * 1024 * 1024 * 1024,
		InFlightRam:       0,
		EstimatedTaskRam:  1 * 1024 * 1024 * 1024,
		MinAvailRam:       1 * 1024 * 1024 * 1024,
		MemoryLoad:        93, // 93% (>= 90%)
		AvailDisk:         50 * 1024 * 1024 * 1024,
		MinAvailDisk:      5 * 1024 * 1024 * 1024,
		GpuUtilization:    10.0,
		AvailVram:         4 * 1024 * 1024 * 1024,
		MinAvailVram:      512 * 1024 * 1024,
		EstimatedTaskVram: 1024 * 1024 * 1024,
		MaxGpuUtilization: 0.85,
		EnableGpuThrottle: true,
		RetryDelay:        25 * time.Second,
	}

	decision := EvaluateGoNoGoPure(input)

	if decision.IsGo {
		t.Fatalf("Expected IsGo=false for high memory load (93%%), got true")
	}
	if decision.WaitDuration != 25*time.Second {
		t.Fatalf("Expected WaitDuration=25s, got %v", decision.WaitDuration)
	}
}

func TestEvaluateGoNoGoPure_GpuOverload(t *testing.T) {
	input := GatekeeperInput{
		AvailPhys:         32 * 1024 * 1024 * 1024,
		InFlightRam:       0,
		EstimatedTaskRam:  1 * 1024 * 1024 * 1024,
		MinAvailRam:       1 * 1024 * 1024 * 1024,
		MemoryLoad:        40,
		AvailDisk:         50 * 1024 * 1024 * 1024,
		MinAvailDisk:      5 * 1024 * 1024 * 1024,
		GpuUtilization:    92.5, // 92.5% >= 85.0%
		AvailVram:         4 * 1024 * 1024 * 1024,
		MinAvailVram:      512 * 1024 * 1024,
		EstimatedTaskVram: 1024 * 1024 * 1024,
		MaxGpuUtilization: 0.85,
		EnableGpuThrottle: true,
		RetryDelay:        15 * time.Second,
	}

	decision := EvaluateGoNoGoPure(input)

	if decision.IsGo {
		t.Fatalf("Expected IsGo=false for GPU overload (92.5%%), got true")
	}
	if !decision.IsGpuBlock {
		t.Fatalf("Expected IsGpuBlock=true, got false")
	}
	if decision.WaitDuration != 15*time.Second {
		t.Fatalf("Expected WaitDuration=15s, got %v", decision.WaitDuration)
	}
}

func TestEvaluateGoNoGoPure_VramInsufficient(t *testing.T) {
	input := GatekeeperInput{
		AvailPhys:         32 * 1024 * 1024 * 1024,
		InFlightRam:       0,
		EstimatedTaskRam:  1 * 1024 * 1024 * 1024,
		MinAvailRam:       1 * 1024 * 1024 * 1024,
		MemoryLoad:        40,
		AvailDisk:         50 * 1024 * 1024 * 1024,
		MinAvailDisk:      5 * 1024 * 1024 * 1024,
		GpuUtilization:    30.0,
		AvailVram:         800 * 1024 * 1024,  // 800 MB Avail (< 1.5 GB Required)
		MinAvailVram:      512 * 1024 * 1024,  // 512 MB
		EstimatedTaskVram: 1024 * 1024 * 1024, // 1024 MB (Total required: 1536 MB)
		MaxGpuUtilization: 0.85,
		EnableGpuThrottle: true,
		RetryDelay:        15 * time.Second,
	}

	decision := EvaluateGoNoGoPure(input)

	if decision.IsGo {
		t.Fatalf("Expected IsGo=false for VRAM deficit, got true")
	}
	if !decision.IsGpuBlock {
		t.Fatalf("Expected IsGpuBlock=true for VRAM deficit, got false")
	}
	if decision.WaitDuration != 15*time.Second {
		t.Fatalf("Expected WaitDuration=15s, got %v", decision.WaitDuration)
	}
}

func TestEvaluateGoNoGoPure_GpuThrottleDisabled(t *testing.T) {
	input := GatekeeperInput{
		AvailPhys:         32 * 1024 * 1024 * 1024,
		InFlightRam:       0,
		EstimatedTaskRam:  1 * 1024 * 1024 * 1024,
		MinAvailRam:       1 * 1024 * 1024 * 1024,
		MemoryLoad:        40,
		AvailDisk:         50 * 1024 * 1024 * 1024,
		MinAvailDisk:      5 * 1024 * 1024 * 1024,
		GpuUtilization:    99.0, // High GPU
		AvailVram:         100 * 1024 * 1024, // Low VRAM
		MinAvailVram:      512 * 1024 * 1024,
		EstimatedTaskVram: 1024 * 1024 * 1024,
		MaxGpuUtilization: 0.85,
		EnableGpuThrottle: false, // Disabled
		RetryDelay:        15 * time.Second,
	}

	decision := EvaluateGoNoGoPure(input)

	if !decision.IsGo {
		t.Fatalf("Expected IsGo=true when GPU throttle is disabled, got false (reason: %s)", decision.Reason)
	}
}

func TestEstimateDemucsTotalRamBytes(t *testing.T) {
	// 1. Short task (e.g. 1 minute CUE slice: 44100 * 60 = 2,646,000 samples)
	shortTask := TaskPayload{
		StartSample: 0,
		EndSample:   2646000,
	}
	shortRam := EstimateDemucsTotalRamBytes(shortTask)
	if shortRam < 1024*1024*1024 {
		t.Fatalf("Expected shortRam to include baseline >= 1GB, got %d", shortRam)
	}

	// 2. Long task (e.g. 15 minutes: 44100 * 900 = 39,690,000 samples)
	longTask := TaskPayload{
		StartSample: 0,
		EndSample:   39690000,
	}
	longRam := EstimateDemucsTotalRamBytes(longTask)
	if longRam <= shortRam {
		t.Fatalf("Expected longRam (%d) > shortRam (%d)", longRam, shortRam)
	}
}
