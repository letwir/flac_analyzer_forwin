package sysinfo

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// Mor: UnitTesting -> ProofOfCorrectness
// Functor: f_test ∘ g_gpu
// Semantics: P1-I2 GPU sysinfo source contract deterministic validation:
// valid zero vs unknown, inconsistency, no shared substitution, failure/unknown behavior, staleness/ambiguity,
// and prohibition of permissive MaxUint64 and AdapterRAM/used*2 capacity inference.

func TestGetLatestGpuMetrics_DefaultSafe(t *testing.T) {
	metrics := GetLatestGpuMetrics()
	if metrics == nil {
		t.Fatalf("GetLatestGpuMetrics must never return nil")
	}
	// Permissive MaxUint64 is replaced with fail-safe zero and explicit validity flag
	if metrics.AvailableVramBytes != 0 {
		t.Errorf("Expected default uninitialized AvailableVramBytes to be 0, got %d", metrics.AvailableVramBytes)
	}
	if metrics.DedicatedAvailableValid {
		t.Errorf("Expected DedicatedAvailableValid to be false in uninitialized state")
	}
	if metrics.AvailableVramValid {
		t.Errorf("Expected AvailableVramValid to be false in uninitialized state")
	}
	if metrics.DedicatedCapacityValid {
		t.Errorf("Expected DedicatedCapacityValid to be false in uninitialized state")
	}
	if metrics.DedicatedUsageValid {
		t.Errorf("Expected DedicatedUsageValid to be false in uninitialized state")
	}
}

func TestGpuMetrics_CalculationIntegrity(t *testing.T) {
	// 正常系：Dedicated 4GB, 使用中 1.5GB
	total := uint64(4 * 1024 * 1024 * 1024)
	used := uint64(1536 * 1024 * 1024)
	expectedAvail := total - used

	m := &GpuMetrics{
		UtilizationPercent:           45.5,
		DedicatedTotalBytes:          total,
		DedicatedUsedBytes:           used,
		AvailableVramBytes:           expectedAvail,
		DedicatedCapacityValid:       true,
		DedicatedTotalValid:          true,
		DedicatedUsageValid:          true,
		DedicatedUsedValid:           true,
		DedicatedAvailableValid:      true,
		AvailableVramValid:           true,
		DedicatedCapacityProvenance:  ProvenanceManualOverride,
		DedicatedTotalSource:         GpuTotalSourceConfig,
		DedicatedUsageProvenance:     ProvenancePerfCounter,
		DedicatedAvailableProvenance: ProvenanceCalculated,
	}

	if m.AvailableVramBytes != expectedAvail {
		t.Errorf("Expected avail VRAM %d, got %d", expectedAvail, m.AvailableVramBytes)
	}
	if !m.IsDedicatedAvailable() {
		t.Errorf("Expected IsDedicatedAvailable() to be true for valid metric")
	}

	// 境界系：VRAM 満杯時 (Valid Zero)
	mFull := &GpuMetrics{
		DedicatedTotalBytes:          total,
		DedicatedUsedBytes:           total,
		AvailableVramBytes:           0,
		DedicatedCapacityValid:       true,
		DedicatedTotalValid:          true,
		DedicatedUsageValid:          true,
		DedicatedUsedValid:           true,
		DedicatedAvailableValid:      true,
		AvailableVramValid:           true,
		DedicatedAvailableProvenance: ProvenanceCalculated,
	}
	if mFull.AvailableVramBytes != 0 {
		t.Errorf("Expected 0 avail VRAM for full GPU, got %d", mFull.AvailableVramBytes)
	}
	if !mFull.DedicatedAvailableValid {
		t.Errorf("Full GPU with valid metrics must have DedicatedAvailableValid=true (valid zero)")
	}

	// フォールバック系：GPU 情報が取れない環境（未知値は 0 かつ Valid=false、MaxUint64 ではない）
	mFallback := NewUnknownGpuMetrics("fallback uninitialized", time.Time{})
	if mFallback.AvailableVramBytes != 0 {
		t.Errorf("Expected 0 AvailableVramBytes for fallback, got %d", mFallback.AvailableVramBytes)
	}
	if mFallback.DedicatedAvailableValid {
		t.Errorf("Expected DedicatedAvailableValid=false for fallback")
	}
	if mFallback.AvailableVramBytes == math.MaxUint64 {
		t.Errorf("Permissive MaxUint64 fallback is strictly prohibited")
	}
}

func TestEvaluateGpuMetricsPure_ValidZeroVsUnknown(t *testing.T) {
	now := time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC)
	threshold := 10 * time.Second
	total := uint64(16 * 1024 * 1024 * 1024)

	// Case 1: Valid Zero — 16GB total capacity, 16GB dedicated used.
	// Dedicated VRAM is 100% full. Availability is known and exactly 0.
	rawValidZero := RawGpuData{
		UtilizationPercent: 95.0,
		DedicatedUsage:     int64(total),
		SharedUsage:        512 * 1024 * 1024,
		TotalCommitted:     int64(total) + (512 * 1024 * 1024),
		AdapterCount:       1,
		AdapterName:        "NVIDIA GeForce RTX 5070 Ti",
		CollectedAt:        now,
	}
	mZero := EvaluateGpuMetricsPure(rawValidZero, total, now, threshold)

	if mZero.DedicatedTotalBytes != total {
		t.Fatalf("Expected DedicatedTotalBytes %d, got %d", total, mZero.DedicatedTotalBytes)
	}
	if mZero.DedicatedUsedBytes != total {
		t.Fatalf("Expected DedicatedUsedBytes %d, got %d", total, mZero.DedicatedUsedBytes)
	}
	if mZero.AvailableVramBytes != 0 {
		t.Fatalf("Expected AvailableVramBytes to be 0 for full GPU, got %d", mZero.AvailableVramBytes)
	}
	if !mZero.DedicatedAvailableValid {
		t.Fatalf("Expected DedicatedAvailableValid to be true for valid zero")
	}
	if !mZero.AvailableVramValid {
		t.Fatalf("Expected AvailableVramValid to be true for valid zero")
	}
	if mZero.DedicatedAvailableProvenance != ProvenanceCalculated {
		t.Fatalf("Expected ProvenanceCalculated, got %s", mZero.DedicatedAvailableProvenance)
	}
	if mZero.Inconsistent {
		t.Fatalf("Valid zero should not be marked inconsistent")
	}

	// Case 2: Unknown Availability — Unknown capacity, unverified WMI AdapterRAM (0).
	// Availability cannot be computed.
	rawUnknown := RawGpuData{
		UtilizationPercent: 0.0,
		DedicatedUsage:     0,
		SharedUsage:        0,
		TotalCommitted:     0,
		AdapterRAM:         0,
		AdapterCount:       1,
		AdapterName:        "Generic GPU",
		CollectedAt:        now,
	}
	mUnknown := EvaluateGpuMetricsPure(rawUnknown, 0, now, threshold)

	if mUnknown.AvailableVramBytes != 0 {
		t.Fatalf("Expected AvailableVramBytes to be 0 for unknown availability, got %d", mUnknown.AvailableVramBytes)
	}
	if mUnknown.DedicatedAvailableValid {
		t.Fatalf("Expected DedicatedAvailableValid to be false when capacity is unknown")
	}
	if mUnknown.DedicatedCapacityValid {
		t.Fatalf("Expected DedicatedCapacityValid to be false when capacity is unknown")
	}
	if mUnknown.DedicatedAvailableProvenance != ProvenanceUnknown {
		t.Fatalf("Expected ProvenanceUnknown, got %s", mUnknown.DedicatedAvailableProvenance)
	}

	// Discriminator test: Both have AvailableVramBytes == 0, but valid zero has DedicatedAvailableValid == true
	// and unknown has DedicatedAvailableValid == false.
	if mZero.DedicatedAvailableValid == mUnknown.DedicatedAvailableValid {
		t.Fatalf("Contract violation: Valid zero and unknown must be distinguishable via validity flag")
	}
}

func TestEvaluateGpuMetricsPure_Inconsistency(t *testing.T) {
	now := time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC)
	threshold := 10 * time.Second
	total := uint64(8 * 1024 * 1024 * 1024)

	// Case 1: Dedicated usage exceeds total capacity (used 10GB > total 8GB)
	rawExceeds := RawGpuData{
		UtilizationPercent: 80.0,
		DedicatedUsage:     int64(10 * 1024 * 1024 * 1024),
		SharedUsage:        0,
		TotalCommitted:     int64(10 * 1024 * 1024 * 1024),
		AdapterCount:       1,
		CollectedAt:        now,
	}
	mExceeds := EvaluateGpuMetricsPure(rawExceeds, total, now, threshold)

	if !mExceeds.Inconsistent {
		t.Errorf("Expected Inconsistent=true when used exceeds total")
	}
	if mExceeds.DedicatedAvailableValid {
		t.Errorf("Inconsistent metrics must produce DedicatedAvailableValid=false")
	}
	if mExceeds.AvailableVramBytes != 0 {
		t.Errorf("Inconsistent metrics must produce AvailableVramBytes=0, got %d", mExceeds.AvailableVramBytes)
	}
	if mExceeds.DedicatedAvailableProvenance != ProvenanceUnknown {
		t.Errorf("Expected ProvenanceUnknown on inconsistency, got %s", mExceeds.DedicatedAvailableProvenance)
	}
	if !strings.Contains(mExceeds.StatusDetail, "inconsistent") {
		t.Errorf("Expected status detail to explain inconsistency, got: %s", mExceeds.StatusDetail)
	}

	// Case 2: Negative dedicated usage from counter corruption
	rawNegative := RawGpuData{
		UtilizationPercent: 10.0,
		DedicatedUsage:     -512,
		SharedUsage:        0,
		TotalCommitted:     0,
		AdapterCount:       1,
		CollectedAt:        now,
	}
	mNegative := EvaluateGpuMetricsPure(rawNegative, total, now, threshold)
	if !mNegative.Inconsistent {
		t.Errorf("Negative usage must be marked inconsistent")
	}
	if mNegative.DedicatedUsageValid {
		t.Errorf("Negative usage must have DedicatedUsageValid=false")
	}
	if mNegative.DedicatedAvailableValid {
		t.Errorf("Negative usage must produce DedicatedAvailableValid=false")
	}

	// Case 3: Committed video memory is less than dedicated usage
	rawCommittedLower := RawGpuData{
		UtilizationPercent: 30.0,
		DedicatedUsage:     int64(6 * 1024 * 1024 * 1024),
		SharedUsage:        0,
		TotalCommitted:     int64(4 * 1024 * 1024 * 1024),
		AdapterCount:       1,
		CollectedAt:        now,
	}
	mCommittedLower := EvaluateGpuMetricsPure(rawCommittedLower, total, now, threshold)
	if !mCommittedLower.Inconsistent {
		t.Errorf("Committed < DedicatedUsed must be marked inconsistent")
	}
	if mCommittedLower.DedicatedAvailableValid {
		t.Errorf("Committed inconsistency must produce DedicatedAvailableValid=false")
	}

	// Case 4: Negative shared usage
	rawNegativeShared := RawGpuData{
		UtilizationPercent: 20.0,
		DedicatedUsage:     int64(2 * 1024 * 1024 * 1024),
		SharedUsage:        -1024,
		TotalCommitted:     int64(2 * 1024 * 1024 * 1024),
		AdapterCount:       1,
		CollectedAt:        now,
	}
	mNegativeShared := EvaluateGpuMetricsPure(rawNegativeShared, total, now, threshold)
	if !mNegativeShared.Inconsistent {
		t.Errorf("Negative shared usage must be marked inconsistent")
	}
	if mNegativeShared.SharedUsageValid {
		t.Errorf("Negative shared usage must have SharedUsageValid=false")
	}

	// Case 5: Explicit override smaller than dedicated usage (used 10GB > override 8GB)
	rawOverrideUndersized := RawGpuData{
		UtilizationPercent: 50.0,
		DedicatedUsage:     int64(10 * 1024 * 1024 * 1024),
		SharedUsage:        0,
		TotalCommitted:     int64(10 * 1024 * 1024 * 1024),
		AdapterCount:       1,
		CollectedAt:        now,
	}
	mUndersized := EvaluateGpuMetricsPure(rawOverrideUndersized, total, now, threshold)
	if !mUndersized.Inconsistent {
		t.Errorf("Used > override must be marked inconsistent")
	}
	if mUndersized.DedicatedAvailableValid {
		t.Errorf("Inconsistent override metrics must produce DedicatedAvailableValid=false")
	}
	if mUndersized.AvailableVramBytes != 0 {
		t.Errorf("Inconsistent override metrics must produce AvailableVramBytes=0, got %d", mUndersized.AvailableVramBytes)
	}
}

func TestEvaluateGpuMetricsPure_NoSharedSubstitution(t *testing.T) {
	now := time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC)
	threshold := 10 * time.Second

	// Dedicated and shared must remain separate.
	// Case 1: Dedicated 16GB total, 4GB used. Shared 8GB used.
	// Available dedicated VRAM must be exactly 16GB - 4GB = 12GB.
	// Shared usage of 8GB must NOT reduce or substitute dedicated availability.
	totalDedicated := uint64(16 * 1024 * 1024 * 1024)
	usedDedicated := uint64(4 * 1024 * 1024 * 1024)
	usedShared := int64(8 * 1024 * 1024 * 1024)

	raw := RawGpuData{
		UtilizationPercent: 50.0,
		DedicatedUsage:     int64(usedDedicated),
		SharedUsage:        usedShared,
		TotalCommitted:     int64(usedDedicated + uint64(usedShared)),
		AdapterCount:       1,
		CollectedAt:        now,
	}
	m := EvaluateGpuMetricsPure(raw, totalDedicated, now, threshold)

	if m.DedicatedUsedBytes != usedDedicated {
		t.Errorf("Expected DedicatedUsedBytes %d, got %d", usedDedicated, m.DedicatedUsedBytes)
	}
	if m.SharedUsedBytes != uint64(usedShared) {
		t.Errorf("Expected SharedUsedBytes %d, got %d", usedShared, m.SharedUsedBytes)
	}
	expectedDedicatedAvail := totalDedicated - usedDedicated
	if m.AvailableVramBytes != expectedDedicatedAvail {
		t.Errorf("Expected AvailableVramBytes %d, got %d (shared memory must not pollute dedicated calculation)",
			expectedDedicatedAvail, m.AvailableVramBytes)
	}
	if !m.DedicatedAvailableValid {
		t.Errorf("Dedicated availability should be valid")
	}

	// Case 2: Dedicated capacity is unknown, but host shared memory usage is large (16GB).
	// Shared memory must NEVER be substituted to provide dedicated availability.
	mNoCap := EvaluateGpuMetricsPure(raw, 0, now, threshold)
	if mNoCap.DedicatedAvailableValid {
		t.Errorf("Shared memory must not substitute for unknown dedicated capacity")
	}
	if mNoCap.AvailableVramBytes != 0 {
		t.Errorf("Expected AvailableVramBytes=0 when dedicated capacity is unknown, got %d", mNoCap.AvailableVramBytes)
	}
	if mNoCap.SharedUsedBytes != uint64(usedShared) {
		t.Errorf("Shared usage should still be recorded separately, got %d", mNoCap.SharedUsedBytes)
	}

	// Case 3: Dedicated VRAM is full (8GB / 8GB), but shared memory is completely free.
	// Dedicated availability must be 0 (valid zero), NOT substituted by free shared memory.
	rawFullDed := RawGpuData{
		UtilizationPercent: 90.0,
		DedicatedUsage:     int64(8 * 1024 * 1024 * 1024),
		SharedUsage:        0,
		TotalCommitted:     int64(8 * 1024 * 1024 * 1024),
		AdapterCount:       1,
		CollectedAt:        now,
	}
	mFullDed := EvaluateGpuMetricsPure(rawFullDed, 8*1024*1024*1024, now, threshold)
	if mFullDed.AvailableVramBytes != 0 {
		t.Errorf("Dedicated VRAM full must have AvailableVramBytes=0, got %d", mFullDed.AvailableVramBytes)
	}
	if !mFullDed.DedicatedAvailableValid {
		t.Errorf("Full dedicated VRAM must have valid availability flag (valid zero)")
	}
}

func TestEvaluateGpuMetricsPure_FailureBehavior(t *testing.T) {
	now := time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC)
	threshold := 10 * time.Second

	// Query failure must produce unknown dedicated availability with diagnostic detail
	queryErr := errors.New("timeout querying CIM Win32_PerfFormattedData_GPUPerformanceCounters_GPUEngine")
	rawErr := RawGpuData{
		QueryErr:    queryErr,
		CollectedAt: now,
	}
	m := EvaluateGpuMetricsPure(rawErr, 16*1024*1024*1024, now, threshold)

	if m.DedicatedAvailableValid {
		t.Errorf("Failure must produce DedicatedAvailableValid=false")
	}
	if m.DedicatedCapacityValid {
		t.Errorf("Failure must produce DedicatedCapacityValid=false")
	}
	if m.DedicatedUsageValid {
		t.Errorf("Failure must produce DedicatedUsageValid=false")
	}
	if m.AvailableVramBytes != 0 {
		t.Errorf("Failure must produce AvailableVramBytes=0, got %d", m.AvailableVramBytes)
	}
	if m.DedicatedAvailableProvenance != ProvenanceUnknown {
		t.Errorf("Failure must produce ProvenanceUnknown, got %s", m.DedicatedAvailableProvenance)
	}
	if !strings.Contains(m.StatusDetail, "timeout querying CIM") {
		t.Errorf("Expected status detail to contain query failure message, got: %s", m.StatusDetail)
	}
	if m.CollectionError == nil {
		t.Errorf("Expected CollectionError to be non-nil on query failure")
	}
}

func TestEvaluateGpuMetricsPure_StaleValue(t *testing.T) {
	sampleTime := time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC)
	threshold := 10 * time.Second
	// Current evaluation time is 15 seconds after sample was collected (exceeds 10s threshold)
	staleNow := sampleTime.Add(15 * time.Second)

	raw := RawGpuData{
		UtilizationPercent: 25.0,
		DedicatedUsage:     int64(2 * 1024 * 1024 * 1024),
		SharedUsage:        0,
		TotalCommitted:     int64(2 * 1024 * 1024 * 1024),
		AdapterCount:       1,
		CollectedAt:        sampleTime,
	}

	m := EvaluateGpuMetricsPure(raw, 16*1024*1024*1024, staleNow, threshold)

	// "a failure, stale value, or multi-adapter ambiguity must produce unknown dedicated availability"
	if m.DedicatedAvailableValid {
		t.Errorf("Stale metric must produce DedicatedAvailableValid=false")
	}
	if m.AvailableVramBytes != 0 {
		t.Errorf("Stale metric must produce AvailableVramBytes=0, got %d", m.AvailableVramBytes)
	}
	if m.DedicatedAvailableProvenance != ProvenanceUnknown {
		t.Errorf("Stale metric must produce DedicatedAvailableProvenance=ProvenanceUnknown, got %s",
			m.DedicatedAvailableProvenance)
	}
	if !strings.Contains(m.StatusDetail, "stale") {
		t.Errorf("Expected status detail to mention stale, got: %s", m.StatusDetail)
	}

	// Zero timestamp also treated as stale/uncollected
	rawZeroTime := RawGpuData{
		UtilizationPercent: 25.0,
		DedicatedUsage:     int64(2 * 1024 * 1024 * 1024),
		AdapterCount:       1,
		CollectedAt:        time.Time{},
	}
	mZero := EvaluateGpuMetricsPure(rawZeroTime, 16*1024*1024*1024, staleNow, threshold)
	if mZero.DedicatedAvailableValid {
		t.Errorf("Zero timestamp must produce DedicatedAvailableValid=false")
	}
}

func TestEvaluateGpuMetricsPure_MultiAdapterAmbiguity(t *testing.T) {
	now := time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC)
	threshold := 10 * time.Second

	// Multi-adapter system (e.g. Intel iGPU + NVIDIA dGPU)
	// Counters summed across all adapters cannot be unambiguously assigned to the dedicated GPU.
	rawMulti := RawGpuData{
		UtilizationPercent: 40.0,
		DedicatedUsage:     int64(4 * 1024 * 1024 * 1024),
		SharedUsage:        int64(1 * 1024 * 1024 * 1024),
		TotalCommitted:     int64(5 * 1024 * 1024 * 1024),
		AdapterCount:       2,
		AdapterName:        "Intel(R) UHD Graphics 770; NVIDIA GeForce RTX 5070 Ti",
		CollectedAt:        now,
	}

	m := EvaluateGpuMetricsPure(rawMulti, 16*1024*1024*1024, now, threshold)

	// "a failure, stale value, or multi-adapter ambiguity must produce unknown dedicated availability"
	if !m.MultiAdapterAmbiguity {
		t.Errorf("Expected MultiAdapterAmbiguity=true when AdapterCount > 1")
	}
	if m.DedicatedAvailableValid {
		t.Errorf("Multi-adapter ambiguity must produce DedicatedAvailableValid=false")
	}
	if m.AvailableVramBytes != 0 {
		t.Errorf("Multi-adapter ambiguity must produce AvailableVramBytes=0, got %d", m.AvailableVramBytes)
	}
	if m.DedicatedAvailableProvenance != ProvenanceUnknown {
		t.Errorf("Multi-adapter ambiguity must produce ProvenanceUnknown, got %s", m.DedicatedAvailableProvenance)
	}
	if !strings.Contains(m.StatusDetail, "multi-adapter ambiguity") {
		t.Errorf("Expected status detail to explain multi-adapter ambiguity, got: %s", m.StatusDetail)
	}
}

func TestEvaluateGpuMetricsPure_NoAdapterDetected(t *testing.T) {
	now := time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC)
	threshold := 10 * time.Second

	// Zero adapters found
	rawZero := RawGpuData{
		UtilizationPercent: 0.0,
		DedicatedUsage:     0,
		SharedUsage:        0,
		TotalCommitted:     0,
		AdapterCount:       0,
		CollectedAt:        now,
	}

	m := EvaluateGpuMetricsPure(rawZero, 16*1024*1024*1024, now, threshold)
	if m.DedicatedAvailableValid {
		t.Errorf("Zero adapters must produce DedicatedAvailableValid=false")
	}
	if m.AvailableVramBytes != 0 {
		t.Errorf("Zero adapters must produce AvailableVramBytes=0, got %d", m.AvailableVramBytes)
	}
	if m.DedicatedAvailableProvenance != ProvenanceUnknown {
		t.Errorf("Zero adapters must produce ProvenanceUnknown, got %s", m.DedicatedAvailableProvenance)
	}
}

func TestEvaluateGpuMetricsPure_NoCapacityInference(t *testing.T) {
	now := time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC)
	threshold := 10 * time.Second

	// 1. Verify removal of used*2 capacity inference:
	// When AdapterRAM is 0 and no override is set, totalDedicated must NOT be inferred as used*2.
	used := int64(4 * 1024 * 1024 * 1024)
	rawNoCap := RawGpuData{
		DedicatedUsage: used,
		AdapterRAM:     0,
		AdapterCount:   1,
		CollectedAt:    now,
	}
	mNoCap := EvaluateGpuMetricsPure(rawNoCap, 0, now, threshold)
	if mNoCap.DedicatedTotalBytes != 0 {
		t.Errorf("Prohibited inference: DedicatedTotalBytes must be 0, got %d (must not be used*2)",
			mNoCap.DedicatedTotalBytes)
	}
	if mNoCap.DedicatedCapacityValid {
		t.Errorf("DedicatedCapacityValid must be false when capacity cannot be reliably retrieved")
	}
	if mNoCap.DedicatedAvailableValid {
		t.Errorf("DedicatedAvailableValid must be false when capacity is unknown")
	}

	// 2. Verify WMI 32-bit ceiling (4294967295) rejection:
	// On >= 4GB GPUs (like RTX 5070 Ti 16GB), Win32_VideoController.AdapterRAM overflows to 4294967295 (0xFFFFFFFF).
	// This must be recognized as an overflow/sentinel, NOT accepted as a genuine 4GB capacity.
	rawOverflow := RawGpuData{
		DedicatedUsage: used,
		AdapterRAM:     Wmi32BitOverflowSentinel,
		AdapterCount:   1,
		CollectedAt:    now,
	}
	mOverflow := EvaluateGpuMetricsPure(rawOverflow, 0, now, threshold)
	if mOverflow.DedicatedCapacityValid {
		t.Errorf("WMI 32-bit overflow sentinel (4294967295) must NOT be accepted as valid capacity")
	}
	if mOverflow.DedicatedTotalBytes != 0 {
		t.Errorf("Expected DedicatedTotalBytes=0 for WMI overflow, got %d", mOverflow.DedicatedTotalBytes)
	}
	if mOverflow.DedicatedAvailableValid {
		t.Errorf("DedicatedAvailableValid must be false on WMI overflow")
	}
	if !strings.Contains(mOverflow.StatusDetail, "32-bit overflow") {
		t.Errorf("Expected status detail to mention 32-bit overflow, got: %s", mOverflow.StatusDetail)
	}
}

func TestDedicatedVramOverride_Lifecycle(t *testing.T) {
	ClearDedicatedVramOverride()
	if override := GetDedicatedVramOverride(); override != 0 {
		t.Fatalf("Expected override to be 0 after clear, got %d", override)
	}

	targetBytes := uint64(16 * 1024 * 1024 * 1024)
	SetDedicatedVramOverride(targetBytes)
	if override := GetDedicatedVramOverride(); override != targetBytes {
		t.Fatalf("Expected override to be %d, got %d", targetBytes, override)
	}

	now := time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC)
	raw := RawGpuData{
		DedicatedUsage: int64(4 * 1024 * 1024 * 1024),
		TotalCommitted: int64(4 * 1024 * 1024 * 1024),
		AdapterRAM:     0,
		AdapterCount:   1,
		CollectedAt:    now,
	}
	m := EvaluateGpuMetricsPure(raw, GetDedicatedVramOverride(), now, 10*time.Second)
	if !m.DedicatedCapacityValid {
		t.Errorf("Expected DedicatedCapacityValid=true with explicit override")
	}
	if m.DedicatedCapacityProvenance != ProvenanceManualOverride {
		t.Errorf("Expected ProvenanceManualOverride, got %s", m.DedicatedCapacityProvenance)
	}
	if m.DedicatedTotalBytes != targetBytes {
		t.Errorf("Expected DedicatedTotalBytes %d, got %d", targetBytes, m.DedicatedTotalBytes)
	}
	if !m.DedicatedAvailableValid {
		t.Errorf("Expected DedicatedAvailableValid=true with override and valid usage")
	}
	if m.AvailableVramBytes != targetBytes-uint64(raw.DedicatedUsage) {
		t.Errorf("Expected AvailableVramBytes %d, got %d",
			targetBytes-uint64(raw.DedicatedUsage), m.AvailableVramBytes)
	}

	// Test GB-based helper
	SetDedicatedVramTotalGB(16.0)
	if gb := GetDedicatedVramTotalGB(); gb != 16.0 {
		t.Fatalf("Expected GetDedicatedVramTotalGB to return 16.0, got %f", gb)
	}

	ClearDedicatedVramOverride()
	if override := GetDedicatedVramOverride(); override != 0 {
		t.Fatalf("Expected override to be 0 after second clear, got %d", override)
	}
}

func TestDedicatedVramOverride_InvalidatesCachedAvailability(t *testing.T) {
	latestGpuMetrics.Store(&GpuMetrics{DedicatedAvailableValid: true, AvailableVramValid: true, AvailableVramBytes: 8 * 1024 * 1024 * 1024, CollectedAt: time.Now()})
	SetDedicatedVramTotalGB(16)
	metrics := GetLatestGpuMetrics()
	if metrics.DedicatedAvailableValid || metrics.AvailableVramBytes != 0 {
		t.Fatalf("override change must invalidate cached dedicated availability: %+v", metrics)
	}

	SetDedicatedVramOverride(maxDedicatedVramOverrideBytes + 1)
	if override := GetDedicatedVramOverride(); override != 0 {
		t.Fatalf("out-of-range byte override must be rejected, got %d", override)
	}
}

func TestComputeGpuMetricsPure_Compatibility(t *testing.T) {
	now := time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC)
	report := RawGpuReport{
		AdapterCount: 1,
		Util:         60.0,
		DedUsed:      4 * 1024 * 1024 * 1024,
		ShrUsed:      1 * 1024 * 1024 * 1024,
		TotCom:       5 * 1024 * 1024 * 1024,
		AdapterName:  "NVIDIA RTX 5070 Ti",
	}

	metrics := ComputeGpuMetricsPure(report, 16.0, now)
	if metrics == nil {
		t.Fatalf("ComputeGpuMetricsPure returned nil")
	}
	if !metrics.DedicatedAvailableValid {
		t.Errorf("Expected DedicatedAvailableValid=true")
	}
	expectedAvail := uint64(12 * 1024 * 1024 * 1024)
	if metrics.AvailableVramBytes != expectedAvail {
		t.Errorf("Expected AvailableVramBytes=%d, got %d", expectedAvail, metrics.AvailableVramBytes)
	}
}

func TestGpuMetrics_IsStaleAndMethods(t *testing.T) {
	now := time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC)
	m := &GpuMetrics{
		CollectedAt:             now,
		DedicatedAvailableValid: true,
	}

	if m.IsStale(now.Add(5*time.Second), 10*time.Second) {
		t.Errorf("Sample at +5s should not be stale under 10s threshold")
	}
	if !m.IsStale(now.Add(15*time.Second), 10*time.Second) {
		t.Errorf("Sample at +15s should be stale under 10s threshold")
	}
	var nilMetrics *GpuMetrics
	if !nilMetrics.IsStale(now, 10*time.Second) {
		t.Errorf("Nil metrics must report as stale")
	}
	if nilMetrics.IsDedicatedAvailable() {
		t.Errorf("Nil metrics must report IsDedicatedAvailable=false")
	}
}
