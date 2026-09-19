package dispatcher

import (
	"math"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"flac_analyzer/orchestrator/metrics"
	"flac_analyzer/orchestrator/sysinfo"
)

func TestStatsTracker_TrackAndFileDuration(t *testing.T) {
	st := NewStatsTracker()

	flacPath := "C:/Music/test_album.flac"
	st.RegisterFileTracks(flacPath, 2)
	st.SetQueueLength(10)

	// 1. Record Track 1 duration (5 seconds)
	st.RecordTaskCompletion(flacPath, 5*time.Second, true)

	if st.totalTasksProcessed != 1 {
		t.Fatalf("expected totalTasksProcessed=1, got %d", st.totalTasksProcessed)
	}
	if st.lastTaskDurationSec != 5.0 {
		t.Fatalf("expected lastTaskDurationSec=5.0, got %f", st.lastTaskDurationSec)
	}

	// 2. Record Track 2 duration (3 seconds) -> Completes the file!
	st.RecordTaskCompletion(flacPath, 3*time.Second, true)

	if st.totalTasksProcessed != 2 {
		t.Fatalf("expected totalTasksProcessed=2, got %d", st.totalTasksProcessed)
	}
	if st.totalFilesProcessed != 1 {
		t.Fatalf("expected totalFilesProcessed=1, got %d", st.totalFilesProcessed)
	}

	// 3. Verify Prometheus metric values
	lastTaskVal := testutil.ToFloat64(metrics.AnalyzerLastTaskDurationSeconds)
	if lastTaskVal != 3.0 {
		t.Errorf("expected AnalyzerLastTaskDurationSeconds=3.0, got %f", lastTaskVal)
	}

	lastFileVal := testutil.ToFloat64(metrics.AnalyzerLastFileDurationSeconds)
	if lastFileVal <= 0 {
		t.Errorf("expected AnalyzerLastFileDurationSeconds > 0, got %f", lastFileVal)
	}

	tasksPerMin := testutil.ToFloat64(metrics.AnalyzerTasksPerMinute)
	if tasksPerMin != 2.0 {
		t.Errorf("expected AnalyzerTasksPerMinute=2.0, got %f", tasksPerMin)
	}
}

func TestStatsTracker_QueueLengthAndETA(t *testing.T) {
	st := NewStatsTracker()
	st.avgTaskDurationSec = 10.0
	st.SetQueueLength(5)

	etaVal := testutil.ToFloat64(metrics.AnalyzerEtaSeconds)
	if etaVal != 50.0 {
		t.Errorf("expected AnalyzerEtaSeconds=50.0, got %f", etaVal)
	}
}

func TestStatsTracker_StagesAndWaits(t *testing.T) {
	st := NewStatsTracker()

	// 1. Record Stage Durations
	st.RecordStageDuration("demucs", 12*time.Second)
	st.RecordStageDuration("librosa", 4*time.Second)
	st.RecordStageDuration("tensor", 2*time.Second)
	st.RecordStageDuration("essentia", 3*time.Second)
	st.RecordStageDuration("flac_tagger", 1*time.Second)
	st.RecordStageDuration("db_ingest", 500*time.Millisecond)

	demucsLastVal := testutil.ToFloat64(metrics.AnalyzerLastStageDurationSeconds.WithLabelValues("demucs"))
	if demucsLastVal != 12.0 {
		t.Errorf("expected AnalyzerLastStageDurationSeconds(demucs)=12.0, got %f", demucsLastVal)
	}

	// 2. Record Wait Contention Durations
	st.RecordDemucsWait(2500 * time.Millisecond)
	st.RecordTensorWait(150 * time.Millisecond)
	st.RecordGatekeeperWait(10 * time.Second)
	st.RecordShmAllocDuration(50 * time.Millisecond)

	demucsWaitVal := testutil.ToFloat64(metrics.AnalyzerLastDemucsWaitSeconds)
	if demucsWaitVal != 2.5 {
		t.Errorf("expected AnalyzerLastDemucsWaitSeconds=2.5, got %f", demucsWaitVal)
	}

	gatekeeperWaitVal := testutil.ToFloat64(metrics.AnalyzerLastGatekeeperWaitSeconds)
	if gatekeeperWaitVal != 10.0 {
		t.Errorf("expected AnalyzerLastGatekeeperWaitSeconds=10.0, got %f", gatekeeperWaitVal)
	}

	// 3. Record Python Step Profiles
	st.RecordPythonStepDuration("demucs", "decode", 0.35)
	st.RecordPythonStepDuration("demucs", "inference", 8.5)
	st.RecordPythonStepDuration("librosa", "extract", 3.2)
	st.RecordPythonStepDuration("ingester", "db_query", 0.08)

	pyDemucsInfVal := testutil.ToFloat64(metrics.AnalyzerPythonLastStageDurationSeconds.WithLabelValues("demucs", "inference"))
	if pyDemucsInfVal != 8.5 {
		t.Errorf("expected AnalyzerPythonLastStageDurationSeconds(demucs, inference)=8.5, got %f", pyDemucsInfVal)
	}
}

func TestPublishGpuMetrics_Fresh(t *testing.T) {
	now := time.Now()
	gpuM := &sysinfo.GpuMetrics{
		UtilizationPercent:          0,
		UtilizationValid:            true,
		DedicatedUsedBytes:          0,
		DedicatedTotalBytes:         16 * 1024 * 1024 * 1024,
		DedicatedUsageValid:         true,
		DedicatedCapacityValid:      true,
		AvailableVramBytes:          16 * 1024 * 1024 * 1024,
		DedicatedAvailableValid:     true,
		SharedUsedBytes:             0,
		SharedUsageValid:            true,
		TotalCommittedBytes:         0,
		TotalCommittedValid:         true,
		DedicatedCapacityProvenance: sysinfo.ProvenanceManualOverride,
		CollectedAt:                 now,
	}

	publishGpuMetrics(gpuM, now)

	if val := testutil.ToFloat64(metrics.AnalyzerGpuUtilizationPercent); val != 0 {
		t.Errorf("Expected utilization 0, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuUtilizationValid); val != 1 {
		t.Errorf("Expected utilization valid 1, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedUsedBytes); val != 0 {
		t.Errorf("Expected dedicated used 0, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedValid); val != 1 {
		t.Errorf("Expected dedicated valid 1, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedCapacitySource); val != 1 {
		t.Errorf("Expected capacity source 1 (manual override), got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuSharedValid); val != 1 {
		t.Errorf("Expected shared valid 1, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuCommittedValid); val != 1 {
		t.Errorf("Expected committed valid 1, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuSampleAgeSeconds); val < 0 || math.IsNaN(val) {
		t.Errorf("Expected non-negative sample age, got %v", val)
	}
}

func TestPublishGpuMetrics_InvalidMissing(t *testing.T) {
	now := time.Now()
	gpuM := &sysinfo.GpuMetrics{
		UtilizationValid:            false,
		DedicatedUsageValid:         false,
		DedicatedCapacityValid:      false,
		DedicatedAvailableValid:     false,
		SharedUsageValid:            false,
		TotalCommittedValid:         false,
		DedicatedCapacityProvenance: sysinfo.ProvenanceNone,
		CollectedAt:                 now,
	}

	publishGpuMetrics(gpuM, now)

	if val := testutil.ToFloat64(metrics.AnalyzerGpuUtilizationPercent); !math.IsNaN(val) {
		t.Errorf("Expected utilization NaN, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuUtilizationValid); val != 0 {
		t.Errorf("Expected utilization valid 0, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedUsedBytes); !math.IsNaN(val) {
		t.Errorf("Expected dedicated used NaN, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedTotalBytes); !math.IsNaN(val) {
		t.Errorf("Expected dedicated total NaN, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedValid); val != 0 {
		t.Errorf("Expected dedicated valid 0, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedAvailableValid); val != 0 {
		t.Errorf("Expected available valid 0, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedCapacitySource); val != 0 {
		t.Errorf("Expected capacity source 0, got %v", val)
	}
}

func TestPublishGpuMetrics_Stale(t *testing.T) {
	// Sample collected 15s ago with valid usage; stale threshold is 10s.
	// Usage/util/shared/committed should be invalidated; capacity stays valid.
	staleTime := time.Now().Add(-15 * time.Second)
	now := time.Now()
	gpuM := &sysinfo.GpuMetrics{
		UtilizationPercent:          42.0,
		UtilizationValid:            true,
		DedicatedUsedBytes:          4 * 1024 * 1024 * 1024,
		DedicatedTotalBytes:         16 * 1024 * 1024 * 1024,
		DedicatedUsageValid:         true,
		DedicatedCapacityValid:      true,
		AvailableVramBytes:          0,
		DedicatedAvailableValid:     false,
		SharedUsedBytes:             1024,
		SharedUsageValid:            true,
		TotalCommittedBytes:         5 * 1024 * 1024 * 1024,
		TotalCommittedValid:         true,
		DedicatedCapacityProvenance: sysinfo.ProvenanceManualOverride,
		CollectedAt:                 staleTime,
	}

	publishGpuMetrics(gpuM, now)

	// Stale: utilization, usage, shared, committed should be NaN/invalid
	if val := testutil.ToFloat64(metrics.AnalyzerGpuUtilizationValid); val != 0 {
		t.Errorf("Expected stale utilization valid 0, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedUsedBytes); !math.IsNaN(val) {
		t.Errorf("Expected stale dedicated used NaN, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedValid); val != 0 {
		t.Errorf("Expected stale dedicated valid 0, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuSharedValid); val != 0 {
		t.Errorf("Expected stale shared valid 0, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuCommittedValid); val != 0 {
		t.Errorf("Expected stale committed valid 0, got %v", val)
	}
	// Capacity is NOT invalidated by staleness
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedTotalBytes); val != float64(16*1024*1024*1024) {
		t.Errorf("Expected stale capacity to remain valid, got %v", val)
	}
}

func TestPublishGpuMetrics_IndependentlyUnknownCapacity(t *testing.T) {
	// Usage is valid, capacity is unknown. Usage should still be exported.
	now := time.Now()
	gpuM := &sysinfo.GpuMetrics{
		UtilizationPercent:          10.0,
		UtilizationValid:            true,
		DedicatedUsedBytes:          2 * 1024 * 1024 * 1024,
		DedicatedUsageValid:         true,
		DedicatedCapacityValid:      false,
		DedicatedTotalBytes:         0,
		AvailableVramBytes:          0,
		DedicatedAvailableValid:     false,
		SharedUsedBytes:             512,
		SharedUsageValid:            true,
		TotalCommittedBytes:         3 * 1024 * 1024 * 1024,
		TotalCommittedValid:         true,
		DedicatedCapacityProvenance: sysinfo.ProvenanceUnknown,
		CollectedAt:                 now,
	}

	publishGpuMetrics(gpuM, now)

	// Usage valid, capacity unknown -> dedicated valid gauge=0 but used bytes exported
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedUsedBytes); val != float64(2*1024*1024*1024) {
		t.Errorf("Expected dedicated used bytes exported, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedTotalBytes); !math.IsNaN(val) {
		t.Errorf("Expected dedicated total NaN when capacity unknown, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedValid); val != 0 {
		t.Errorf("Expected dedicated valid 0 when capacity unknown, got %v", val)
	}
	// Available preserves 0/0 contract
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedAvailableBytes); val != 0 {
		t.Errorf("Expected available bytes 0, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedAvailableValid); val != 0 {
		t.Errorf("Expected available valid 0, got %v", val)
	}
}

func TestPublishGpuMetrics_ZeroTimestamp(t *testing.T) {
	// Zero CollectedAt should yield NaN sample age and stale-invalidated usage
	gpuM := &sysinfo.GpuMetrics{
		UtilizationPercent:          50.0,
		UtilizationValid:            true,
		DedicatedUsedBytes:          1024,
		DedicatedUsageValid:         true,
		DedicatedCapacityValid:      true,
		DedicatedTotalBytes:         8 * 1024 * 1024 * 1024,
		DedicatedCapacityProvenance: sysinfo.ProvenanceManualOverride,
		CollectedAt:                 time.Time{}, // zero
	}

	publishGpuMetrics(gpuM, time.Now())

	if val := testutil.ToFloat64(metrics.AnalyzerGpuSampleAgeSeconds); !math.IsNaN(val) {
		t.Errorf("Expected NaN sample age for zero timestamp, got %v", val)
	}
	// Zero timestamp is stale, so usage should be invalidated
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedUsedBytes); !math.IsNaN(val) {
		t.Errorf("Expected NaN dedicated used for zero timestamp, got %v", val)
	}
	if val := testutil.ToFloat64(metrics.AnalyzerGpuUtilizationValid); val != 0 {
		t.Errorf("Expected utilization invalid for zero timestamp, got %v", val)
	}
	// Capacity NOT invalidated by staleness
	if val := testutil.ToFloat64(metrics.AnalyzerGpuDedicatedTotalBytes); val != float64(8*1024*1024*1024) {
		t.Errorf("Expected capacity preserved for zero timestamp, got %v", val)
	}
}

func TestPublishGpuMetrics_Nil(t *testing.T) {
	// Nil gpuM should not panic
	publishGpuMetrics(nil, time.Now())
}

