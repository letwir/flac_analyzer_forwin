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

	decision := EvaluateGoNoGoPure(availPhys, inFlight, estimatedRam, minAvailRam, memLoad, retryDelay, availDisk, minAvailDisk)

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
	availPhys := uint64(32 * 1024 * 1024 * 1024)   // 32 GB
	inFlight := uint64(0)
	estimatedRam := uint64(1 * 1024 * 1024 * 1024) // 1 GB
	minAvailRam := uint64(1 * 1024 * 1024 * 1024)  // 1 GB
	memLoad := uint32(30)
	retryDelay := 20 * time.Second
	availDisk := uint64(3 * 1024 * 1024 * 1024)    // 3 GB (< 5 GB MinAvailDisk)
	minAvailDisk := uint64(5 * 1024 * 1024 * 1024) // 5 GB

	decision := EvaluateGoNoGoPure(availPhys, inFlight, estimatedRam, minAvailRam, memLoad, retryDelay, availDisk, minAvailDisk)

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
	availPhys := uint64(6 * 1024 * 1024 * 1024)    // 6 GB
	inFlight := uint64(2 * 1024 * 1024 * 1024)     // 2 GB (Effective Avail = 4 GB)
	estimatedRam := uint64(3 * 1024 * 1024 * 1024) // 3 GB
	minAvailRam := uint64(2 * 1024 * 1024 * 1024)  // 2 GB (Required = 5 GB)
	memLoad := uint32(60)                          // 60%
	retryDelay := 20 * time.Second
	availDisk := uint64(50 * 1024 * 1024 * 1024)
	minAvailDisk := uint64(5 * 1024 * 1024 * 1024)

	decision := EvaluateGoNoGoPure(availPhys, inFlight, estimatedRam, minAvailRam, memLoad, retryDelay, availDisk, minAvailDisk)

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
	availPhys := uint64(2 * 1024 * 1024 * 1024)    // 2 GB
	inFlight := uint64(5 * 1024 * 1024 * 1024)     // 5 GB (InFlight > AvailPhys)
	estimatedRam := uint64(1 * 1024 * 1024 * 1024) // 1 GB
	minAvailRam := uint64(1 * 1024 * 1024 * 1024)  // 1 GB
	memLoad := uint32(85)
	retryDelay := 20 * time.Second
	availDisk := uint64(50 * 1024 * 1024 * 1024)
	minAvailDisk := uint64(5 * 1024 * 1024 * 1024)

	decision := EvaluateGoNoGoPure(availPhys, inFlight, estimatedRam, minAvailRam, memLoad, retryDelay, availDisk, minAvailDisk)

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
	availPhys := uint64(32 * 1024 * 1024 * 1024) // 32 GB
	inFlight := uint64(0)
	estimatedRam := uint64(1 * 1024 * 1024 * 1024) // 1 GB
	minAvailRam := uint64(1 * 1024 * 1024 * 1024)  // 1 GB
	memLoad := uint32(93)                          // 93% (>= 90%)
	retryDelay := 25 * time.Second
	availDisk := uint64(50 * 1024 * 1024 * 1024)
	minAvailDisk := uint64(5 * 1024 * 1024 * 1024)

	decision := EvaluateGoNoGoPure(availPhys, inFlight, estimatedRam, minAvailRam, memLoad, retryDelay, availDisk, minAvailDisk)

	if decision.IsGo {
		t.Fatalf("Expected IsGo=false for high memory load (93%%), got true")
	}
	if decision.WaitDuration != 25*time.Second {
		t.Fatalf("Expected WaitDuration=25s, got %v", decision.WaitDuration)
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
