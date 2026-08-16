package dispatcher

import (
	"context"
	"sync"
	"time"

	"flac_analyzer/orchestrator/metrics"
	"flac_analyzer/orchestrator/sysinfo"
)

// Mor: (TaskExecution × SystemState) -> MetricsStream
// Functor: f_stats ∘ g_dispatch
// Semantics: リアルタイム統計・所要時間・スループット集約射

// FileProgressTracker tracks multi-track completion for single FLAC files.
type FileProgressTracker struct {
	StartTime           time.Time
	TotalTracks         int
	FinishedTracks      int
	SuccessTracks       int
	FailedTracks        int
	AccumulatedDuration time.Duration
}

// StatsTracker aggregates track/file execution durations, throughput, and system resource metrics.
type StatsTracker struct {
	mu sync.RWMutex

	// トラック単位の統計
	totalTasksProcessed int64
	taskDurations       []float64
	avgTaskDurationSec  float64
	lastTaskDurationSec float64

	// ファイル単位の統計 (CUEマルチトラック統合)
	fileTrackMap        map[string]*FileProgressTracker
	totalFilesProcessed int64
	fileDurations       []float64
	avgFileDurationSec  float64
	lastFileDurationSec float64

	// スループット計測用ウィンドウ (直近1分間)
	taskTimestamps []time.Time
	fileTimestamps []time.Time

	// キュー長・残り時間予測
	queueLength int

	// ステージ別 EMA (Exponential Moving Average: alpha=0.15)
	avgStageDurationSec map[string]float64
}

// NewStatsTracker constructs a initialized StatsTracker instance.
func NewStatsTracker() *StatsTracker {
	return &StatsTracker{
		fileTrackMap:        make(map[string]*FileProgressTracker),
		taskDurations:       make([]float64, 0, 50),
		fileDurations:       make([]float64, 0, 50),
		taskTimestamps:      make([]time.Time, 0, 100),
		fileTimestamps:      make([]time.Time, 0, 100),
		avgStageDurationSec: make(map[string]float64),
	}
}

// RegisterFileTracks registers the expected track count for a given FLAC file.
func (st *StatsTracker) RegisterFileTracks(filePath string, totalTracks int) {
	st.mu.Lock()
	defer st.mu.Unlock()

	if totalTracks <= 0 {
		totalTracks = 1
	}

	if _, exists := st.fileTrackMap[filePath]; !exists {
		st.fileTrackMap[filePath] = &FileProgressTracker{
			StartTime:   time.Now(),
			TotalTracks: totalTracks,
		}
	}
}

// RecordTaskCompletion records the execution duration of a single track/task.
func (st *StatsTracker) RecordTaskCompletion(filePath string, duration time.Duration, success bool) {
	st.mu.Lock()
	defer st.mu.Unlock()

	durSec := duration.Seconds()
	st.lastTaskDurationSec = durSec
	st.totalTasksProcessed++

	// Prometheus Histogram & Gauge 更新
	metrics.AnalyzerTaskDurationSeconds.Observe(durSec)
	metrics.AnalyzerLastTaskDurationSeconds.Set(durSec)

	// 指数移動平均 (EMA: alpha=0.15) の更新
	if st.avgTaskDurationSec == 0 {
		st.avgTaskDurationSec = durSec
	} else {
		st.avgTaskDurationSec = (0.15 * durSec) + (0.85 * st.avgTaskDurationSec)
	}
	metrics.AnalyzerAvgTaskDurationSeconds.Set(st.avgTaskDurationSec)

	// スループット用タイムスタンプ記録
	now := time.Now()
	st.taskTimestamps = append(st.taskTimestamps, now)
	st.cleanOldTimestamps(now)

	// ファイル進捗の更新
	tracker, exists := st.fileTrackMap[filePath]
	if !exists {
		// 単体FLAC等で事前登録がない場合
		tracker = &FileProgressTracker{
			StartTime:   now.Add(-duration),
			TotalTracks: 1,
		}
		st.fileTrackMap[filePath] = tracker
	}

	tracker.FinishedTracks++
	tracker.AccumulatedDuration += duration
	if success {
		tracker.SuccessTracks++
	} else {
		tracker.FailedTracks++
	}

	// 当該ファイルの全トラックが完了したか判定
	if tracker.FinishedTracks >= tracker.TotalTracks {
		realDur := now.Sub(tracker.StartTime).Seconds()
		fileDur := realDur
		if fileDur <= 0 || (tracker.TotalTracks == 1 && fileDur < tracker.AccumulatedDuration.Seconds()) {
			fileDur = tracker.AccumulatedDuration.Seconds()
		}

		st.lastFileDurationSec = fileDur
		st.totalFilesProcessed++

		metrics.AnalyzerFileDurationSeconds.Observe(fileDur)
		metrics.AnalyzerLastFileDurationSeconds.Set(fileDur)

		if st.avgFileDurationSec == 0 {
			st.avgFileDurationSec = fileDur
		} else {
			st.avgFileDurationSec = (0.15 * fileDur) + (0.85 * st.avgFileDurationSec)
		}
		metrics.AnalyzerAvgFileDurationSeconds.Set(st.avgFileDurationSec)

		fileStatus := "success"
		if tracker.FailedTracks > 0 && tracker.SuccessTracks == 0 {
			fileStatus = "error"
		}
		metrics.AnalyzerFilesTotal.WithLabelValues(fileStatus).Inc()

		st.fileTimestamps = append(st.fileTimestamps, now)
		delete(st.fileTrackMap, filePath)
	}

	st.updateThroughputAndEta()
}

// cleanOldTimestamps prunes timestamps older than 60 seconds (Pure Window Pruning).
func (st *StatsTracker) cleanOldTimestamps(now time.Time) {
	threshold := now.Add(-60 * time.Second)

	// Clean task timestamps
	idx := 0
	for i, t := range st.taskTimestamps {
		if t.After(threshold) {
			idx = i
			break
		}
	}
	if idx > 0 {
		st.taskTimestamps = st.taskTimestamps[idx:]
	}

	// Clean file timestamps
	idxFile := 0
	for i, t := range st.fileTimestamps {
		if t.After(threshold) {
			idxFile = i
			break
		}
	}
	if idxFile > 0 {
		st.fileTimestamps = st.fileTimestamps[idxFile:]
	}
}

// RecordStageDuration records execution duration for a specific pipeline stage.
func (st *StatsTracker) RecordStageDuration(stage string, duration time.Duration) {
	st.mu.Lock()
	defer st.mu.Unlock()

	durSec := duration.Seconds()
	metrics.AnalyzerStageDurationSeconds.WithLabelValues(stage).Observe(durSec)
	metrics.AnalyzerLastStageDurationSeconds.WithLabelValues(stage).Set(durSec)

	currentAvg := st.avgStageDurationSec[stage]
	if currentAvg == 0 {
		currentAvg = durSec
	} else {
		currentAvg = (0.15 * durSec) + (0.85 * currentAvg)
	}
	st.avgStageDurationSec[stage] = currentAvg
	metrics.AnalyzerAvgStageDurationSeconds.WithLabelValues(stage).Set(currentAvg)
}

// RecordDemucsWait records waiting duration for Demucs semaphore slot.
func (st *StatsTracker) RecordDemucsWait(duration time.Duration) {
	durSec := duration.Seconds()
	metrics.AnalyzerDemucsWaitSeconds.Observe(durSec)
	metrics.AnalyzerLastDemucsWaitSeconds.Set(durSec)
}

// RecordTensorWait records waiting duration for Tensor semaphore slot.
func (st *StatsTracker) RecordTensorWait(duration time.Duration) {
	durSec := duration.Seconds()
	metrics.AnalyzerTensorWaitSeconds.Observe(durSec)
	metrics.AnalyzerLastTensorWaitSeconds.Set(durSec)
}

// RecordGatekeeperWait records waiting duration blocked by Gatekeeper.
func (st *StatsTracker) RecordGatekeeperWait(duration time.Duration) {
	durSec := duration.Seconds()
	metrics.AnalyzerGatekeeperWaitSeconds.Observe(durSec)
	metrics.AnalyzerLastGatekeeperWaitSeconds.Set(durSec)
}

// RecordShmAllocDuration records SHM allocation and locking duration.
func (st *StatsTracker) RecordShmAllocDuration(duration time.Duration) {
	durSec := duration.Seconds()
	metrics.AnalyzerShmAllocDurationSeconds.Observe(durSec)
}

// RecordPythonStepDuration records internal duration of a Python subprocess step.
func (st *StatsTracker) RecordPythonStepDuration(component, step string, durSec float64) {
	if durSec <= 0 {
		return
	}
	metrics.AnalyzerPythonStageDurationSeconds.WithLabelValues(component, step).Observe(durSec)
	metrics.AnalyzerPythonLastStageDurationSeconds.WithLabelValues(component, step).Set(durSec)
}

// updateThroughputAndEta recalculates throughput per minute and estimated time of arrival.
func (st *StatsTracker) updateThroughputAndEta() {
	tasksPerMin := float64(len(st.taskTimestamps))
	filesPerMin := float64(len(st.fileTimestamps))

	metrics.AnalyzerTasksPerMinute.Set(tasksPerMin)
	metrics.AnalyzerFilesPerMinute.Set(filesPerMin)

	// ETA 計算: 残りキュー長 × 平均トラック所要時間 / 有効並列度
	if st.avgTaskDurationSec > 0 && st.queueLength > 0 {
		estimatedSec := float64(st.queueLength) * st.avgTaskDurationSec
		metrics.AnalyzerEtaSeconds.Set(estimatedSec)
	} else {
		metrics.AnalyzerEtaSeconds.Set(0)
	}
}

// SetQueueLength updates current pending queue length.
func (st *StatsTracker) SetQueueLength(qLen int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.queueLength = qLen
	st.updateThroughputAndEta()
}

// StartSystemResourceCollector periodically updates Disk and RAM available bytes metrics.
func (st *StatsTracker) StartSystemResourceCollector(ctx context.Context, queueDir string, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				st.collectSystemResources(queueDir)
			}
		}
	}()
}

func (st *StatsTracker) collectSystemResources(queueDir string) {
	// RAM 測定
	if memInfo, err := sysinfo.GetMemoryInfo(); err == nil && memInfo != nil {
		metrics.AnalyzerRamAvailableBytes.Set(float64(memInfo.AvailPhys))
	}

	// 作業ディスク測定
	targetDir := queueDir
	if targetDir == "" {
		targetDir = "."
	}
	if diskInfo, err := sysinfo.GetDiskFreeSpace(targetDir); err == nil && diskInfo != nil {
		metrics.AnalyzerDiskFreeBytes.Set(float64(diskInfo.FreeBytesAvailable))
	}
}
