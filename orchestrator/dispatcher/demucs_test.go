package dispatcher

import (
	"math"
	"testing"
)

// Mor: UnitTesting -> ProofOfCorrectness
// Functor: f_test ∘ g_scheduler
// Semantics: アダプティブ GPU スケジューラの純粋性・境界値・ヒステリシス健全性検証

func TestDetermineDemucsSlotLimitPure_SingleDefault(t *testing.T) {
	// GPU 負荷が高い場合 (60% >= 50%) -> スロット 1
	limit := DetermineDemucsSlotLimitPure(60.0, 8*1024*1024*1024, 0.50, 4*1024*1024*1024, 2)
	if limit != 1 {
		t.Fatalf("Expected slot limit 1 for high GPU (60%%), got %d", limit)
	}

	// VRAM 空き容量が少ない場合 (2GB < 4GB) -> スロット 1
	limitVram := DetermineDemucsSlotLimitPure(20.0, 2*1024*1024*1024, 0.50, 4*1024*1024*1024, 2)
	if limitVram != 1 {
		t.Fatalf("Expected slot limit 1 for low VRAM (2GB), got %d", limitVram)
	}

	// 最大キャパシティが 1 の場合 -> 常に 1
	limitCap1 := DetermineDemucsSlotLimitPure(10.0, 16*1024*1024*1024, 0.50, 4*1024*1024*1024, 1)
	if limitCap1 != 1 {
		t.Fatalf("Expected slot limit 1 for maxCapacity=1, got %d", limitCap1)
	}
}

func TestDetermineDemucsSlotLimitPure_DualBoost(t *testing.T) {
	// GPU 負荷が低く (25% < 50%)、VRAM に余裕がある場合 (8GB >= 4GB) -> スロット 2
	limit := DetermineDemucsSlotLimitPure(25.0, 8*1024*1024*1024, 0.50, 4*1024*1024*1024, 2)
	if limit != 2 {
		t.Fatalf("Expected slot limit 2 for idle GPU and abundant VRAM, got %d", limit)
	}

	// VRAM が MaxUint64 (無制限/取得不能環境) で GPU 負荷が低い場合 -> スロット 2
	limitUnbounded := DetermineDemucsSlotLimitPure(20.0, math.MaxUint64, 0.50, 4*1024*1024*1024, 2)
	if limitUnbounded != 2 {
		t.Fatalf("Expected slot limit 2 for unbounded VRAM environment, got %d", limitUnbounded)
	}
}

func TestAdaptiveDemucsScheduler_Basic(t *testing.T) {
	st := NewStatsTracker()
	sched := NewAdaptiveDemucsScheduler(1, 2, 0.50, 4*1024*1024*1024, st)
	if sched.GetLimit() != 1 {
		t.Fatalf("Expected initial limit 1, got %d", sched.GetLimit())
	}
}
