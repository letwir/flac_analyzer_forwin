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

func TestClaimPendingTasksPrefersShorterEstimatedDuration(t *testing.T) {
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
	if len(claimed) != 2 || claimed[0].FilePath != "C:/music/short.flac" {
		t.Fatalf("expected short task first, claimed=%#v", claimed)
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
