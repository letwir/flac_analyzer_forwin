package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	AnalyzerTasksTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "analyzer_tasks_total",
			Help: "Total number of tasks processed, partitioned by status",
		},
		[]string{"status"}, // "success", "error", "oom_failed", "skipped"
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
)

func InitMetricsServer(addr string) error {
	http.Handle("/metrics", promhttp.Handler())
	return http.ListenAndServe(addr, nil)
}
