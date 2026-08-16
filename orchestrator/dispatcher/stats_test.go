package dispatcher

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"flac_analyzer/orchestrator/metrics"
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
