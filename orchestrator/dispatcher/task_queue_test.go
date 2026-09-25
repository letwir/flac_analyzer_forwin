package dispatcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flac_analyzer/orchestrator/state"
)

func TestTaskWithActualFileSizeOverridesUntrustedMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "song.flac")
	if err := os.WriteFile(path, []byte("flac-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	task, err := taskWithActualFileSize(TaskPayload{FlacPath: path, FileSize: 999999})
	if err != nil {
		t.Fatal(err)
	}
	if task.FileSize != int64(len("flac-bytes")) {
		t.Fatalf("FileSize=%d, want actual size %d", task.FileSize, len("flac-bytes"))
	}
	if _, err := taskWithActualFileSize(TaskPayload{FlacPath: filepath.Join(t.TempDir(), "missing.flac")}); err == nil {
		t.Fatal("missing input file unexpectedly accepted for ordered queueing")
	}
}

func TestFeederReevaluatesPendingTasksWhenWorkerBecomesReady(t *testing.T) {
	db, err := state.InitDB(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	d := &Dispatcher{
		config:        Config{GatekeeperRetryDelaySec: 60},
		db:            db,
		taskQueue:     make(chan TaskPayload),
		taskFeederCtx: context.Background(),
		prepareAnalysisFn: func(_ context.Context, tasks []TaskPayload) ([]TaskPayload, error) {
			for i := range tasks {
				tasks[i].AnalysisDecision = FullAnalysis
			}
			return tasks, nil
		},
		reserveTaskFn: func(TaskPayload) (AdmissionLease, error) {
			return AdmissionLease{}, nil
		},
	}

	for _, entry := range []struct {
		path string
		size int64
	}{
		{path: "C:/music/large.flac", size: 9000},
		{path: "C:/music/medium.flac", size: 4000},
	} {
		payload := fmt.Sprintf(`{"flacPath":%q,"trackNumber":1,"fileSize":%d}`, entry.path, entry.size)
		if _, err := db.CheckOrInsertWithPayload(entry.path, 1, payload, false); err != nil {
			t.Fatal(err)
		}
	}

	if ready := d.fillTaskQueue(0); ready != 0 {
		t.Fatalf("idle feeder returned ready count %d, want 0", ready)
	}
	for _, path := range []string{"C:/music/large.flac", "C:/music/medium.flac"} {
		status, err := db.GetTaskState(path, 1)
		if err != nil || status.Status != state.StatusPending {
			t.Fatalf("task %s was claimed without a ready worker: status=%s err=%v", path, status.Status, err)
		}
	}

	newSmall := `{"flacPath":"C:/music/new-small.flac","trackNumber":1,"fileSize":1200}`
	if _, err := db.CheckOrInsertWithPayload("C:/music/new-small.flac", 1, newSmall, false); err != nil {
		t.Fatal(err)
	}
	dispatched := make(chan int, 1)
	go func() {
		d.fillTaskQueue(1)
		dispatched <- 1
	}()

	select {
	case task := <-d.taskQueue:
		if task.FlacPath != "C:/music/new-small.flac" {
			t.Fatalf("worker received %s before newly added smaller file", task.FlacPath)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("feeder did not hand a task to the ready worker")
	}
	select {
	case <-dispatched:
	case <-time.After(2 * time.Second):
		t.Fatal("feeder did not return after dispatch")
	}
}

func TestFeederReturnsClaimedTaskToPendingWhenShutdownPrecedesHandoff(t *testing.T) {
	db, err := state.InitDB(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	path := "C:/music/waiting-worker.flac"
	if _, err := db.CheckOrInsertWithPayload(path, 1, `{"flacPath":"C:/music/waiting-worker.flac","trackNumber":1,"fileSize":1000}`, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &Dispatcher{
		config:        Config{GatekeeperRetryDelaySec: 60},
		db:            db,
		taskQueue:     make(chan TaskPayload),
		taskFeederCtx: ctx,
		prepareAnalysisFn: func(_ context.Context, tasks []TaskPayload) ([]TaskPayload, error) {
			for i := range tasks {
				tasks[i].AnalysisDecision = FullAnalysis
			}
			return tasks, nil
		},
		reserveTaskFn: func(TaskPayload) (AdmissionLease, error) {
			return AdmissionLease{Token: 42}, nil
		},
	}
	done := make(chan struct{})
	go func() {
		d.fillTaskQueue(1)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := db.GetTaskState(path, 1)
		if err != nil {
			t.Fatal(err)
		}
		if status.Status == state.StatusQueued {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, err := db.GetTaskState(path, 1)
	if err != nil || status.Status != state.StatusQueued {
		t.Fatalf("task did not reach pre-handoff QUEUED state: status=%s err=%v", status.Status, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("feeder did not exit after cancellation")
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	status, err = db.GetTaskState(path, 1)
	if err != nil || status.Status != state.StatusPending {
		t.Fatalf("unhanded task was not restored to PENDING: status=%s err=%v", status.Status, err)
	}
}
