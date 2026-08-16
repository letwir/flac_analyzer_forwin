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

