package metrics

import (
	"net/http"
	_ "net/http/pprof" // ライブプロファイリング用 pprof ハンドラを http.DefaultServeMux に自動登録いたしますわ！

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Mor: PipelineState -> PrometheusStream
// Functor: f_metrics ∘ g_observe
// Semantics: ETL パイプライン・ボトルネック可観測性射（Stage Latency / Contention / Python Profiles）

var (
	AnalyzerTasksTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "analyzer_tasks_total",
			Help: "Total number of tasks (tracks) processed, partitioned by status",
		},
		[]string{"status"}, // "success", "error", "oom_failed", "skipped"
	)

	AnalyzerFilesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "analyzer_files_total",
			Help: "Total number of FLAC files processed, partitioned by status",
		},
		[]string{"status"}, // "success", "error", "skipped"
	)

	AnalyzerQueueLength = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_queue_length",
			Help: "Current length of the pending task queue",
		},
	)

	AnalyzerActiveWorkers = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_active_workers",
			Help: "Number of workers currently processing tasks",
		},
	)

	AnalyzerDemucsSlotsInUse = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_demucs_slots_in_use",
			Help: "Number of Demucs concurrency slots currently in use",
		},
	)

	AnalyzerDemucsDynamicLimit = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_demucs_dynamic_limit",
			Help: "Current adaptive concurrency limit for Demucs GPU execution (1 or 2)",
		},
	)

	AnalyzerDemucsDaemonActiveSlots = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_demucs_daemon_active_slots",
			Help: "Number of Demucs resident daemon slots currently active",
		},
	)

	AnalyzerDemucsDaemonPoolSize = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_demucs_daemon_pool_size",
			Help: "Total number of spawned Demucs resident daemons in pool",
		},
	)

	AnalyzerErrorsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "analyzer_errors_total",
			Help: "Total number of errors encountered in the orchestrator or Python workers",
		},
	)

	// 1曲（トラック）あたりの所要時間ヒストグラム＆直近・平均所要時間 (Seconds)
	AnalyzerTaskDurationSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "analyzer_task_duration_seconds",
			Help:    "Execution duration for single track processing in seconds",
			Buckets: []float64{1, 2, 5, 10, 15, 20, 30, 45, 60, 90, 120, 180, 300},
		},
	)

	AnalyzerLastTaskDurationSeconds = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_last_task_duration_seconds",
			Help: "Processing duration of the most recently completed track in seconds",
		},
	)

	AnalyzerAvgTaskDurationSeconds = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_avg_task_duration_seconds",
			Help: "Exponential moving average of single track processing duration in seconds",
		},
	)

	// 1ファイルあたりの所要時間ヒストグラム＆直近・平均所要時間 (Seconds)
	AnalyzerFileDurationSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "analyzer_file_duration_seconds",
			Help:    "Execution duration for entire FLAC file processing (all tracks combined) in seconds",
			Buckets: []float64{2, 5, 10, 20, 30, 45, 60, 90, 120, 180, 240, 360, 600},
		},
	)

	AnalyzerLastFileDurationSeconds = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_last_file_duration_seconds",
			Help: "Processing duration of the most recently completed FLAC file in seconds",
		},
	)

	AnalyzerAvgFileDurationSeconds = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_avg_file_duration_seconds",
			Help: "Exponential moving average of entire FLAC file processing duration in seconds",
		},
	)

	// ─── 技法①：パイプライン・ステージ別レイテンシ分解 (Stage Latency Breakdown) ───
	// stages: "hash_check", "shm_alloc", "demucs", "librosa", "tensor", "essentia", "flac_tagger", "db_ingest"
	AnalyzerStageDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "analyzer_stage_duration_seconds",
			Help:    "Execution duration for each distinct pipeline stage in seconds",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 60, 120},
		},
		[]string{"stage"},
	)

	AnalyzerLastStageDurationSeconds = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "analyzer_last_stage_duration_seconds",
			Help: "Most recent execution duration for each distinct pipeline stage in seconds",
		},
		[]string{"stage"},
	)

	AnalyzerAvgStageDurationSeconds = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "analyzer_avg_stage_duration_seconds",
			Help: "Exponential moving average execution duration for each pipeline stage in seconds",
		},
		[]string{"stage"},
	)

	// ─── 技法②：リソース競合・待機時間 (Contention & Saturation) ───
	// Demucs セマフォ待ち時間
	AnalyzerDemucsWaitSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "analyzer_demucs_wait_seconds",
			Help:    "Wait time spent waiting for Demucs execution semaphore slot in seconds",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300},
		},
	)

	AnalyzerLastDemucsWaitSeconds = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_last_demucs_wait_seconds",
			Help: "Most recent wait duration for Demucs semaphore slot in seconds",
		},
	)

	// Tensor 排他セマフォ待ち時間
	AnalyzerTensorWaitSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "analyzer_tensor_wait_seconds",
			Help:    "Wait time spent waiting for Tensor (ONNX/PyTorch) semaphore slot in seconds",
			Buckets: []float64{0.05, 0.1, 0.5, 1, 2, 5, 10, 20, 30, 60},
		},
	)

	AnalyzerLastTensorWaitSeconds = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_last_tensor_wait_seconds",
			Help: "Most recent wait duration for Tensor semaphore slot in seconds",
		},
	)

	// Gatekeeper リソース（RAM/Disk）防御待機時間
	AnalyzerGatekeeperWaitSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "analyzer_gatekeeper_wait_seconds",
			Help:    "Wait time spent blocked by Gatekeeper resource defense in seconds",
			Buckets: []float64{1, 5, 10, 20, 30, 60, 120, 300},
		},
	)

	AnalyzerLastGatekeeperWaitSeconds = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_last_gatekeeper_wait_seconds",
			Help: "Most recent wait duration blocked by Gatekeeper in seconds",
		},
	)

	// 共有メモリ (SHM) 確保・初期化所要時間
	AnalyzerShmAllocDurationSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "analyzer_shm_alloc_duration_seconds",
			Help:    "Duration required to allocate and lock Windows Shared Memory arenas in seconds",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 2, 5},
		},
	)

	// セマフォ待機ワーカー数
	AnalyzerDemucsQueueWaiters = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_demucs_queue_waiters",
			Help: "Number of workers currently queued waiting for Demucs slot",
		},
	)

	AnalyzerTensorQueueWaiters = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_tensor_queue_waiters",
			Help: "Number of workers currently queued waiting for Tensor slot",
		},
	)

	// ─── 技法③：Python サブプロセス内部ステップ別所要時間 (Subprocess Profile Ingestion) ───
	// component: "demucs", "librosa", "tensor", "essentia", "tagger", "ingester"
	// step: "decode", "inference", "shm_write", "warmup", "extract", "write", "db_query"
	AnalyzerPythonStageDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "analyzer_python_stage_duration_seconds",
			Help:    "Internal execution duration of Python worker sub-steps in seconds",
			Buckets: []float64{0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 60},
		},
		[]string{"component", "step"},
	)

	AnalyzerPythonLastStageDurationSeconds = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "analyzer_python_last_stage_duration_seconds",
			Help: "Most recent internal duration of Python worker sub-steps in seconds",
		},
		[]string{"component", "step"},
	)

	// スループット (Tracks / Files per minute)
	AnalyzerTasksPerMinute = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_tasks_per_minute",
			Help: "Estimated processing throughput in tracks per minute",
		},
	)

	AnalyzerFilesPerMinute = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_files_per_minute",
			Help: "Estimated processing throughput in FLAC files per minute",
		},
	)

	// 残り推定時間 (Estimated Time of Arrival / Remaining seconds)
	AnalyzerEtaSeconds = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_eta_seconds",
			Help: "Estimated remaining time in seconds to process all queued tasks",
		},
	)

	// システムストレージ＆メモリ残量 (Bytes)
	AnalyzerDiskFreeBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_disk_free_bytes",
			Help: "Available disk space in bytes for queue and temporary storage",
		},
	)

	AnalyzerRamAvailableBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_ram_available_bytes",
			Help: "Available physical system RAM in bytes",
		},
	)

	// ─── 技法④：GPU & VRAM 可観測性 (GPU Utilization & Video Memory) ───
	AnalyzerGpuUtilizationPercent = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_gpu_utilization_percent",
			Help: "Realtime total GPU utilization percentage (0-100)",
		},
	)

	AnalyzerGpuDedicatedUsedBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_gpu_dedicated_used_bytes",
			Help: "Dedicated video memory (VRAM) used in bytes",
		},
	)

	AnalyzerGpuDedicatedTotalBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_gpu_dedicated_total_bytes",
			Help: "Dedicated video memory (VRAM) total capacity in bytes",
		},
	)

	AnalyzerGpuSharedUsedBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_gpu_shared_used_bytes",
			Help: "Shared system memory used by GPU in bytes",
		},
	)

	AnalyzerGpuTotalCommittedBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_gpu_total_committed_bytes",
			Help: "Total committed GPU memory in bytes",
		},
	)

	AnalyzerGpuWaitSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "analyzer_gpu_wait_seconds",
			Help:    "Wait time spent blocked by GPU utilization or VRAM deficit in seconds",
			Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60, 120},
		},
	)

	AnalyzerLastGpuWaitSeconds = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "analyzer_last_gpu_wait_seconds",
			Help: "Most recent wait duration blocked by GPU in seconds",
		},
	)

	AnalyzerGpuThrottleEventsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "analyzer_gpu_throttle_events_total",
			Help: "Total count of dispatch throttle events triggered by GPU saturation",
		},
	)
)

// InitMetricsServer starts the Prometheus metrics HTTP server with pprof enabled.
func InitMetricsServer(addr string) error {
	http.Handle("/metrics", promhttp.Handler())
	// pprof はブランクインポート (_ "net/http/pprof") により /debug/pprof/ 下に自動登録されますわ！
	return http.ListenAndServe(addr, nil)
}

