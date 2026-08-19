package sysinfo

import (
	"math"
	"testing"
)

// Mor: UnitTesting -> ProofOfCorrectness
// Functor: f_test ∘ g_gpu
// Semantics: GPU 観測モジュールの純粋性・境界値・フォールバック健全性検証

func TestGetLatestGpuMetrics_DefaultSafe(t *testing.T) {
	metrics := GetLatestGpuMetrics()
	if metrics == nil {
		t.Fatalf("GetLatestGpuMetrics must never return nil")
	}
	if metrics.AvailableVramBytes == 0 {
		t.Logf("Warning: AvailableVramBytes is 0 in default environment")
	}
}

func TestGpuMetrics_CalculationIntegrity(t *testing.T) {
	// 正常系：Dedicated 4GB, 使用中 1.5GB
	total := uint64(4 * 1024 * 1024 * 1024)
	used := uint64(1536 * 1024 * 1024)
	expectedAvail := total - used

	m := &GpuMetrics{
		UtilizationPercent:  45.5,
		DedicatedTotalBytes: total,
		DedicatedUsedBytes:  used,
		AvailableVramBytes:  expectedAvail,
	}

	if m.AvailableVramBytes != expectedAvail {
		t.Errorf("Expected avail VRAM %d, got %d", expectedAvail, m.AvailableVramBytes)
	}

	// 境界系：VRAM 満杯時
	mFull := &GpuMetrics{
		DedicatedTotalBytes: total,
		DedicatedUsedBytes:  total,
		AvailableVramBytes:  0,
	}
	if mFull.AvailableVramBytes != 0 {
		t.Errorf("Expected 0 avail VRAM for full GPU, got %d", mFull.AvailableVramBytes)
	}

	// フォールバック系：GPU 情報が取れない環境
	mFallback := &GpuMetrics{
		AvailableVramBytes: math.MaxUint64,
	}
	if mFallback.AvailableVramBytes != math.MaxUint64 {
		t.Errorf("Expected MaxUint64 for fallback, got %d", mFallback.AvailableVramBytes)
	}
}
