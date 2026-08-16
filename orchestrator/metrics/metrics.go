package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Prometheus メトリクス定義（Prometheus Metric Collectors）
// 1曲（トラック）および 1ファイルあたりの所要時間・スループット・システム残量を完全可視化いたしますわ！
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
)

// InitMetricsServer starts the Prometheus metrics HTTP server.
func InitMetricsServer(addr string) error {
	http.Handle("/metrics", promhttp.Handler())
	return http.ListenAndServe(addr, nil)
}
