package dispatcher

import (
	"context"
	"math"
	"sync"
	"time"

	"flac_analyzer/orchestrator/metrics"
	"flac_analyzer/orchestrator/sysinfo"
)

// Mor: (GpuState × SchedulerConfig) -> SlotLimit
// Functor: f_scheduler ∘ g_gpu
// Semantics: GPU 負荷および VRAM 空き容量に基づくアダプティブ Single/Dual スロット制御射

// DetermineDemucsSlotLimitPure decides dynamic slot limit without side-effects (Pure Domain Morphism).
func DetermineDemucsSlotLimitPure(gpuUtil float64, availVramBytes uint64, dualUtilThreshold float64, dualMinVramBytes uint64, maxCapacity int) int {
	if maxCapacity <= 1 {
		return 1
	}
	if dualUtilThreshold <= 0 {
		dualUtilThreshold = 0.50 // デフォルト 50%
	}
	if dualMinVramBytes == 0 {
		dualMinVramBytes = 4 * 1024 * 1024 * 1024 // デフォルト 4GB
	}

	// GPU 負荷率判定: 閾値以上なら即座にシングルタスクへ縮退
	if gpuUtil >= (dualUtilThreshold * 100.0) {
		return 1
	}

	// VRAM 空き容量判定: 最低必要容量未満ならシングルタスクへ縮退 (math.MaxUint64 は無制限環境)
	if availVramBytes != math.MaxUint64 && availVramBytes < dualMinVramBytes {
		return 1
	}

	// GPU・VRAM 共に余裕がある場合のみデュアルタスク (2) を許可
	if maxCapacity >= 2 {
		return 2
	}
	return 1
}

type AdaptiveDemucsScheduler struct {
	mu                  sync.Mutex
	semaphore           *DynamicSemaphore
	maxCapacity         int
	dualUtilThreshold   float64
	dualMinVramBytes    uint64
	currentLimit        int
	consecutiveLowCount int // Advisory 2: スケールアップ用ヒステリシスカウンタ
	statsTracker        *StatsTracker
}

func NewAdaptiveDemucsScheduler(initialLimit, maxCapacity int, dualUtilThreshold float64, dualMinVramBytes uint64, statsTracker *StatsTracker) *AdaptiveDemucsScheduler {
	if initialLimit <= 0 {
		initialLimit = 1
	}
	if maxCapacity <= 0 {
		maxCapacity = 2
	}
	if dualUtilThreshold <= 0 {
		dualUtilThreshold = 0.50
	}
	if dualMinVramBytes == 0 {
		dualMinVramBytes = 4 * 1024 * 1024 * 1024
	}

	sem := NewDynamicSemaphore(initialLimit)
	metrics.AnalyzerDemucsDynamicLimit.Set(float64(initialLimit))

	return &AdaptiveDemucsScheduler{
		semaphore:         sem,
		maxCapacity:       maxCapacity,
		dualUtilThreshold: dualUtilThreshold,
		dualMinVramBytes:  dualMinVramBytes,
		currentLimit:      initialLimit,
		statsTracker:      statsTracker,
	}
}

func (s *AdaptiveDemucsScheduler) StartAdaptiveLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.evaluateAndResizeSlotComplex()
			}
		}
	}()
}

// evaluateAndResizeSlotComplex evaluates GPU metrics and resizes semaphore with hysteresis.
func (s *AdaptiveDemucsScheduler) evaluateAndResizeSlotComplex() {
	gpuM := sysinfo.GetLatestGpuMetrics()
	var gpuUtil float64 = 0.0
	var availVram uint64 = math.MaxUint64
	if gpuM != nil {
		gpuUtil = gpuM.UtilizationPercent
		availVram = gpuM.AvailableVramBytes
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	targetLimit := DetermineDemucsSlotLimitPure(gpuUtil, availVram, s.dualUtilThreshold, s.dualMinVramBytes, s.maxCapacity)

	if targetLimit < s.currentLimit {
		// スケールダウン（2 → 1）: GPU 負荷検知時に即座に縮退
		s.currentLimit = targetLimit
		s.consecutiveLowCount = 0
		s.semaphore.SetLimit(targetLimit)
		metrics.AnalyzerDemucsDynamicLimit.Set(float64(targetLimit))
	} else if targetLimit > s.currentLimit {
		// スケールアップ（1 → 2）: Advisory 2 遵守（2回連続 = 約4秒間安定時のみ拡張）
		s.consecutiveLowCount++
		if s.consecutiveLowCount >= 2 {
			s.currentLimit = targetLimit
			s.consecutiveLowCount = 0
			s.semaphore.SetLimit(targetLimit)
			metrics.AnalyzerDemucsDynamicLimit.Set(float64(targetLimit))
		}
	} else {
		s.consecutiveLowCount = 0
	}
}

func (s *AdaptiveDemucsScheduler) Acquire() {
	metrics.AnalyzerDemucsQueueWaiters.Inc()
	waitStart := time.Now()
	s.semaphore.Acquire()
	metrics.AnalyzerDemucsQueueWaiters.Dec()
	metrics.AnalyzerDemucsSlotsInUse.Inc()
	metrics.AnalyzerDemucsDaemonActiveSlots.Inc()

	if s.statsTracker != nil {
		s.statsTracker.RecordDemucsWait(time.Since(waitStart))
	}
}

func (s *AdaptiveDemucsScheduler) Release() {
	s.semaphore.Release()
	metrics.AnalyzerDemucsSlotsInUse.Dec()
	metrics.AnalyzerDemucsDaemonActiveSlots.Dec()
}

func (s *AdaptiveDemucsScheduler) GetLimit() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentLimit
}
