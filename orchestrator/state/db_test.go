package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDurableTaskLifecycle(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "orchestrator.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	payload := `{"flacPath":"C:/music/song.flac","trackNumber":2,"fileSize":1234}`
	shouldRun, err := db.CheckOrInsertWithPayload("C:/music/song.flac", 2, payload, false)
	if err != nil || !shouldRun {
		t.Fatalf("expected durable registration, shouldRun=%v err=%v", shouldRun, err)
	}

	shouldRun, err = db.CheckOrInsertWithPayload("C:/music/song.flac", 2, payload, false)
	if err != nil || shouldRun {
		t.Fatalf("expected duplicate durable registration to skip, shouldRun=%v err=%v", shouldRun, err)
	}

	claimed, err := db.ClaimPendingTasks(1)
	if err != nil {
		t.Fatalf("ClaimPendingTasks failed: %v", err)
	}
	if len(claimed) != 1 || claimed[0].PayloadJSON != payload {
		t.Fatalf("unexpected claimed payload: %#v", claimed)
	}

	shouldRun, err = db.CheckOrInsertWithPayload("C:/music/song.flac", 2, payload, false)
	if err != nil || shouldRun {
		t.Fatalf("expected QUEUED task to skip, shouldRun=%v err=%v", shouldRun, err)
	}
}

func TestCountWaitingTasksCountsDurableWaitingStatuses(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	paths := []string{
		"C:/music/task-1.flac",
		"C:/music/task-2.flac",
		"C:/music/task-3.flac",
		"C:/music/task-4.flac",
		"C:/music/task-5.flac",
	}
	for _, path := range paths {
		if _, err := db.CheckOrInsertWithPayload(path, 1, `{"trackNumber":1}`, false); err != nil {
			t.Fatalf("register %s: %v", path, err)
		}
	}

	claimed, err := db.ClaimPendingTasks(len(paths) - 2)
	if err != nil || len(claimed) != len(paths)-2 {
		t.Fatalf("claim tasks: count=%d err=%v", len(claimed), err)
	}
	if err := db.UpdateStatus(claimed[0].FilePath, claimed[0].TrackNumber, StatusRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateStatus(claimed[1].FilePath, claimed[1].TrackNumber, StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateStatus(paths[3], 1, StatusFailedMaybeRetry, "resource gate"); err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("flush status updates: %v", err)
	}

	got, err := db.CountWaitingTasks()
	if err != nil {
		t.Fatalf("CountWaitingTasks failed: %v", err)
	}
	// One queued, one retryable, and one pending task are waiting. RUNNING and
	// COMPLETED rows must not be reported as queue backlog.
	if got != 3 {
		t.Fatalf("CountWaitingTasks()=%d, want 3", got)
	}
}

func TestOpenReadOnlyDoesNotCreateOrModifyStateDB(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing.db")
	if db, err := OpenReadOnly(missingPath); err == nil {
		_ = db.Close()
		t.Fatal("OpenReadOnly unexpectedly created a missing database")
	}
	if _, err := os.Stat(missingPath); !os.IsNotExist(err) {
		t.Fatalf("missing database was created or stat failed unexpectedly: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "orchestrator.db")
	writable, err := InitDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writable.CheckOrInsertWithForce("C:/music/readonly.flac", 1, false); err != nil {
		t.Fatal(err)
	}
	if err := writable.UpdateStatus("C:/music/readonly.flac", 1, StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
	if err := writable.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	readOnly, err := OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readOnly.GetTaskState("C:/music/readonly.flac", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusCompleted {
		t.Fatalf("status=%s want=%s", got.Status, StatusCompleted)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("read-only access modified database metadata: before=%v/%d after=%v/%d", before.ModTime(), before.Size(), after.ModTime(), after.Size())
	}
}

func TestRetryableTaskCanBeReleasedAndRequeued(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "orchestrator.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	payload := `{"flacPath":"C:/music/retry.flac","trackNumber":1}`
	if _, err := db.CheckOrInsertWithPayload("C:/music/retry.flac", 1, payload, false); err != nil {
		t.Fatalf("registration failed: %v", err)
	}
	if err := db.UpdateStatus("C:/music/retry.flac", 1, StatusFailedMaybeRetry, "low RAM"); err != nil {
		t.Fatalf("failed to mark retryable: %v", err)
	}

	requeued, err := db.RequeueRetryableTasks(1, -1)
	if err != nil || requeued != 1 {
		t.Fatalf("expected one retryable task to be requeued, count=%d err=%v", requeued, err)
	}
	claimed, err := db.ClaimPendingTasks(1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("expected requeued task to be claimable, claimed=%#v err=%v", claimed, err)
	}
}

func TestRetryableLegacyTaskWithoutPayloadCanBeReleased(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "orchestrator.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	if _, err := db.CheckOrInsertWithForce("C:/music/legacy.flac", 3, true); err != nil {
		t.Fatalf("legacy registration failed: %v", err)
	}
	if err := db.UpdateStatus("C:/music/legacy.flac", 3, StatusFailedMaybeRetry, "low RAM"); err != nil {
		t.Fatalf("failed to mark legacy task retryable: %v", err)
	}

	requeued, err := db.RequeueRetryableTasks(1, -1)
	if err != nil || requeued != 1 {
		t.Fatalf("expected legacy retryable task to be requeued, count=%d err=%v", requeued, err)
	}
	claimed, err := db.ClaimPendingTasks(1)
	if err != nil || len(claimed) != 1 || claimed[0].PayloadJSON != "" {
		t.Fatalf("expected legacy task without payload to be claimable, claimed=%#v err=%v", claimed, err)
	}
}

func TestClaimPendingTasksUsesFIFORegardlessOfEstimatedDuration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "orchestrator.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	longPayload := `{"flacPath":"C:/music/long.flac","trackNumber":1,"startSample":0,"endSample":52920000,"sampleRate":44100}`
	shortPayload := `{"flacPath":"C:/music/short.flac","trackNumber":1,"startSample":0,"endSample":441000,"sampleRate":44100}`
	if _, err := db.CheckOrInsertWithPayload("C:/music/long.flac", 1, longPayload, false); err != nil {
		t.Fatalf("long task registration failed: %v", err)
	}
	if _, err := db.CheckOrInsertWithPayload("C:/music/short.flac", 1, shortPayload, false); err != nil {
		t.Fatalf("short task registration failed: %v", err)
	}

	claimed, err := db.ClaimPendingTasks(2)
	if err != nil {
		t.Fatalf("ClaimPendingTasks failed: %v", err)
	}
	if len(claimed) != 2 || claimed[0].FilePath != "C:/music/long.flac" || claimed[1].FilePath != "C:/music/short.flac" {
		t.Fatalf("expected FIFO task order [long, short], claimed=%#v", claimed)
	}
}

func TestClaimPendingTasksFIFOIgnoresAgingPriority(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	shortPayload := `{"flacPath":"C:/music/fresh-short.flac","trackNumber":1,"startSample":0,"endSample":441000,"sampleRate":44100}`
	longPayload := `{"flacPath":"C:/music/aged-long.flac","trackNumber":1,"startSample":0,"endSample":52920000,"sampleRate":44100}`
	if _, err := db.CheckOrInsertWithPayload("C:/music/fresh-short.flac", 1, shortPayload, false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CheckOrInsertWithPayload("C:/music/aged-long.flac", 1, longPayload, false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`UPDATE task_state SET age_anchor_at = datetime('now', '-1801 seconds') WHERE file_path = ?`, "C:/music/aged-long.flac"); err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimPendingTasks(1)
	if err != nil || len(claimed) != 1 || claimed[0].FilePath != "C:/music/fresh-short.flac" {
		t.Fatalf("expected the earlier task first: claimed=%#v err=%v", claimed, err)
	}
}

func TestClaimPendingTasksRejectsUnknownOrder(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ClaimPendingTasksInOrder(1, "file_size"); err == nil {
		t.Fatal("expected unsupported ordering to fail until a policy is implemented")
	}
}

func TestParkDoesNotResetAgingAnchor(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	payload := `{"flacPath":"C:/music/aged-park.flac","trackNumber":1,"fileSize":999999999}`
	if _, err := db.CheckOrInsertWithPayload("C:/music/aged-park.flac", 1, payload, false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`UPDATE task_state SET age_anchor_at = datetime('now', '-1801 seconds') WHERE file_path = ?`, "C:/music/aged-park.flac"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimPendingTasks(1); err != nil {
		t.Fatal(err)
	}
	if err := db.ParkTaskForRetry("C:/music/aged-park.flac", 1, "pressure", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RequeueRetryableTasks(1, 0); err != nil {
		t.Fatal(err)
	}
	var aged int
	if err := db.conn.QueryRow(`SELECT age_anchor_at <= datetime('now', '-1800 seconds') FROM task_state WHERE file_path = ?`, "C:/music/aged-park.flac").Scan(&aged); err != nil || aged != 1 {
		t.Fatalf("aging anchor reset: aged=%d err=%v", aged, err)
	}
}

func TestParkGenerationPreventsDuplicateClaimUntilDue(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	payload := `{"flacPath":"C:/music/parked.flac","trackNumber":1}`
	if _, err := db.CheckOrInsertWithPayload("C:/music/parked.flac", 1, payload, false); err != nil {
		t.Fatal(err)
	}
	if claimed, err := db.ClaimPendingTasks(1); err != nil || len(claimed) != 1 {
		t.Fatalf("initial claim: %#v %v", claimed, err)
	}
	if err := db.ParkTaskForRetry("C:/music/parked.flac", 1, "memory pressure", 60); err != nil {
		t.Fatal(err)
	}
	if count, err := db.RequeueRetryableTasks(1, 0); err != nil || count != 0 {
		t.Fatalf("premature requeue count=%d err=%v", count, err)
	}
	var generation int
	if err := db.conn.QueryRow(`SELECT park_generation FROM task_state WHERE file_path = ?`, "C:/music/parked.flac").Scan(&generation); err != nil || generation != 1 {
		t.Fatalf("generation=%d err=%v", generation, err)
	}
	if err := db.ParkTaskForRetry("C:/music/parked.flac", 1, "duplicate", 0); err == nil {
		t.Fatal("duplicate park unexpectedly succeeded")
	}
}

func TestResetStaleTasksPreservesPendingAndRecoversQueued(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "orchestrator.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	payload := `{"flacPath":"C:/music/resume.flac","trackNumber":1}`
	if _, err := db.CheckOrInsertWithPayload("C:/music/resume.flac", 1, payload, false); err != nil {
		t.Fatalf("registration failed: %v", err)
	}
	if _, err := db.ClaimPendingTasks(1); err != nil {
		t.Fatalf("claim failed: %v", err)
	}

	resetCount, err := db.ResetStaleTasks()
	if err != nil || resetCount != 1 {
		t.Fatalf("expected one QUEUED task to be recovered, count=%d err=%v", resetCount, err)
	}
	claimed, err := db.ClaimPendingTasks(1)
	if err != nil || len(claimed) != 1 || claimed[0].PayloadJSON != payload {
		t.Fatalf("expected recovered task to resume, claimed=%#v err=%v", claimed, err)
	}
}

func TestClaimSingleTaskStatusBoundaries(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "orchestrator.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	payload := `{"flacPath":"C:/music/single.flac","trackNumber":1}`
	claimed, err := db.ClaimSingleTask("C:/music/single.flac", 1, payload, false, false)
	if err != nil || !claimed {
		t.Fatalf("absent task claim failed: claimed=%v err=%v", claimed, err)
	}
	if claimed, err = db.ClaimSingleTask("C:/music/single.flac", 1, payload, true, false); err == nil || claimed {
		t.Fatalf("force must not steal active task: claimed=%v err=%v", claimed, err)
	}
	if claimed, err = db.ClaimSingleTask("C:/music/single.flac", 1, payload, false, true); err != nil || !claimed {
		t.Fatalf("exclusive single runner should recover stale active task: claimed=%v err=%v", claimed, err)
	}

	if err := db.UpdateStatus("C:/music/single.flac", 1, StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	if claimed, err = db.ClaimSingleTask("C:/music/single.flac", 1, payload, false, false); err != nil || claimed {
		t.Fatalf("completed task should be benign skip: claimed=%v err=%v", claimed, err)
	}
	if claimed, err = db.ClaimSingleTask("C:/music/single.flac", 1, payload, true, false); err != nil || !claimed {
		t.Fatalf("force should reclaim completed task: claimed=%v err=%v", claimed, err)
	}
}
func TestPendingTaskOrderUsesFileSizeAscending(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	tasks := []CheckOrInsertTask{
		{
			FilePath:    "/test1.flac",
			TrackNumber: 1,
			PayloadJSON: `{"fileSize":1000000, "startSample":0, "endSample":44100}`, // duration 1s
			Force:       true,
		},
		{
			FilePath:    "/test2.flac",
			TrackNumber: 1,
			PayloadJSON: `{"fileSize":2000000, "startSample":0, "endSample":88200}`, // duration 2s
			Force:       true,
		},
	}

	results, err := db.CheckOrInsertBatch(tasks)
	if err != nil {
		t.Fatalf("CheckOrInsertBatch failed: %v", err)
	}
	if !results[0] || !results[1] {
		t.Fatalf("CheckOrInsertBatch results %v", results)
	}

	claimed, err := db.ClaimPendingTasksInOrder(2, PendingTaskOrderSizeAscending)
	if err != nil {
		t.Fatalf("ClaimPendingTasksInOrder failed: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("Expected 2 claimed tasks, got %d", len(claimed))
	}
	if claimed[0].FilePath != "/test1.flac" || claimed[1].FilePath != "/test2.flac" {
		t.Fatalf("expected byte-size order [/test1.flac /test2.flac], got [%s %s]", claimed[0].FilePath, claimed[1].FilePath)
	}
}

func TestFileSizeOrderReevaluatesNewPendingTasks(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, task := range []CheckOrInsertTask{
		{FilePath: "/music/large.flac", TrackNumber: 1, PayloadJSON: `{"fileSize":9000}`},
		{FilePath: "/music/medium.flac", TrackNumber: 1, PayloadJSON: `{"fileSize":4000}`},
		{FilePath: "/music/new-small.flac", TrackNumber: 1, PayloadJSON: `{"fileSize":1200}`},
	} {
		if _, err := db.CheckOrInsertBatch([]CheckOrInsertTask{task}); err != nil {
			t.Fatalf("enqueue %s: %v", task.FilePath, err)
		}
	}

	first, err := db.ClaimPendingTasksInOrder(1, PendingTaskOrderSizeAscending)
	if err != nil || len(first) != 1 || first[0].FilePath != "/music/new-small.flac" {
		t.Fatalf("new smaller arrival was not selected first: tasks=%#v err=%v", first, err)
	}
	remaining, err := db.ClaimPendingTasksInOrder(2, PendingTaskOrderSizeAscending)
	if err != nil || len(remaining) != 2 || remaining[0].FilePath != "/music/medium.flac" || remaining[1].FilePath != "/music/large.flac" {
		t.Fatalf("remaining tasks not sorted by size: tasks=%#v err=%v", remaining, err)
	}
}

func TestFileSizeOrderUsesEnqueueOrderForTiesAndPutsUnknownLast(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tasks := []CheckOrInsertTask{
		{FilePath: "/music/equal-first.flac", TrackNumber: 1, PayloadJSON: `{"fileSize":2000}`},
		{FilePath: "/music/unknown.flac", TrackNumber: 1, PayloadJSON: `{"fileSize":0}`},
		{FilePath: "/music/equal-second.flac", TrackNumber: 1, PayloadJSON: `{"fileSize":2000}`},
		{FilePath: "/music/invalid.flac", TrackNumber: 1, PayloadJSON: `not-json`},
	}
	if _, err := db.CheckOrInsertBatch(tasks); err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimPendingTasksInOrder(len(tasks), PendingTaskOrderSizeAscending)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/music/equal-first.flac", "/music/equal-second.flac", "/music/unknown.flac", "/music/invalid.flac"}
	if len(claimed) != len(want) {
		t.Fatalf("claimed %d tasks, want %d", len(claimed), len(want))
	}
	for i := range want {
		if claimed[i].FilePath != want[i] {
			t.Fatalf("task[%d]=%s, want %s; all=%#v", i, claimed[i].FilePath, want[i], claimed)
		}
	}
}

func TestBatchTransaction(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_batch.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	tasks := []CheckOrInsertTask{
		{
			FilePath:    "/test1.flac",
			TrackNumber: 1,
			PayloadJSON: "{}",
			Force:       false,
		},
		{
			FilePath:    "/test2.flac",
			TrackNumber: 1,
			PayloadJSON: "{}",
			Force:       false,
		},
	}
	results, err := db.CheckOrInsertBatch(tasks)
	if err != nil {
		t.Fatalf("CheckOrInsertBatch failed: %v", err)
	}
	if !results[0] || !results[1] {
		t.Fatalf("Expected both to run")
	}

	results2, err := db.CheckOrInsertBatch(tasks)
	if err != nil {
		t.Fatalf("CheckOrInsertBatch failed: %v", err)
	}
	if results2[0] || results2[1] {
		t.Fatalf("Expected both not to run on second try")
	}
}
