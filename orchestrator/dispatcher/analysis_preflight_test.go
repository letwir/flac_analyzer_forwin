package dispatcher

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type fakeAnalysisSnapshotLookup struct {
	rows  []analysisSnapshotRow
	err   error
	calls int
	keys  []unregTrackKey
}

func (f *fakeAnalysisSnapshotLookup) Lookup(_ context.Context, keys []unregTrackKey) ([]analysisSnapshotRow, error) {
	f.calls++
	f.keys = append([]unregTrackKey(nil), keys...)
	return f.rows, f.err
}

func TestPrepareAnalysisTasksBatchesCueAndAttachesDecisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "album.flac")
	now := time.Now()
	complete := snapshotForTest(&now, `{"analysis_schema_version":1}`, `{"mix":{"bpm":120},"demucs":{"bass":{"rms":1},"drums":{"rms":1},"vocals":{"rms":1},"other":{"rms":1},"guitar":{"rms":1},"piano":{"rms":1}}}`, `{"genre":0.9}`)
	complete.RowID, complete.AudioHash = 41, "hash-1"
	stemsOnly := snapshotForTest(&now, `{"analysis_schema_version":1}`, `{"mix":{"bpm":120}}`, `{"genre":0.9}`)
	stemsOnly.RowID, stemsOnly.AudioHash = 42, "hash-2"
	lookup := &fakeAnalysisSnapshotLookup{rows: []analysisSnapshotRow{
		{key: mustUnregTrackKey(t, path, 1), snapshot: complete},
		{key: mustUnregTrackKey(t, path, 2), snapshot: stemsOnly},
	}}
	tasks, err := prepareAnalysisTasks(t.Context(), []TaskPayload{{FlacPath: path, TrackNumber: 1}, {FlacPath: path, TrackNumber: 2}, {FlacPath: path, TrackNumber: 3}}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if lookup.calls != 1 || len(lookup.keys) != 3 {
		t.Fatalf("lookup calls=%d keys=%d", lookup.calls, len(lookup.keys))
	}
	if tasks[0].AnalysisDecision != Skip || tasks[1].AnalysisDecision != StemsOnly || tasks[2].AnalysisDecision != FullAnalysis {
		t.Fatalf("unexpected decisions: %s %s %s", tasks[0].AnalysisDecision, tasks[1].AnalysisDecision, tasks[2].AnalysisDecision)
	}
	if tasks[0].AnalysisRowID != 41 || tasks[0].AnalysisRowVersion != 42 || tasks[0].AnalysisAudioHash != "hash-1" {
		t.Fatalf("snapshot guard not attached: %+v", tasks[0])
	}
}

func TestPrepareAnalysisTasksFailsClosed(t *testing.T) {
	lookup := &fakeAnalysisSnapshotLookup{err: errors.New("offline")}
	if tasks, err := prepareAnalysisTasks(t.Context(), []TaskPayload{{FlacPath: filepath.Join(t.TempDir(), "x.flac"), TrackNumber: 1}}, lookup); err == nil || tasks != nil {
		t.Fatalf("tasks=%v err=%v", tasks, err)
	}
}

func TestPrepareAnalysisTasksRejectsDuplicateRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.flac")
	key := mustUnregTrackKey(t, path, 1)
	lookup := &fakeAnalysisSnapshotLookup{rows: []analysisSnapshotRow{{key: key}, {key: key}}}
	if _, err := prepareAnalysisTasks(t.Context(), []TaskPayload{{FlacPath: path, TrackNumber: 1}}, lookup); err == nil {
		t.Fatal("expected duplicate-row failure")
	}
}
