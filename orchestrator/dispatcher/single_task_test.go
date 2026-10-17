package dispatcher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"flac_analyzer/orchestrator/state"
)

func TestWaitForExecutionDelayCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err := waitForExecutionDelay(ctx, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("cancelled wait returned too slowly")
	}
}

func TestCancelledSingleTaskDoesNotClaimTrack(t *testing.T) {
	db, err := state.InitDB(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := &Dispatcher{db: db}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	untouchedPath := filepath.Join(t.TempDir(), "untouched.flac")
	executed, err := d.RunSingleTask(ctx, TaskPayload{FlacPath: untouchedPath, TrackNumber: 2})
	if !errors.Is(err, context.Canceled) || executed {
		t.Fatalf("expected pre-claim cancellation, executed=%v err=%v", executed, err)
	}
	_, err = db.GetTaskState(untouchedPath, 2)
	if err != sql.ErrNoRows {
		t.Fatalf("cancelled task should remain absent, got %v", err)
	}
}

func TestSingleTaskResourceWaitCancellationReturnsRetryableState(t *testing.T) {
	db, err := state.InitDB(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	path := filepath.Join(t.TempDir(), "waiting.flac")
	reserveCalls := 0
	d := &Dispatcher{
		config: Config{GatekeeperRetryDelaySec: 60},
		db:     db,
		prepareAnalysisFn: func(_ context.Context, tasks []TaskPayload) ([]TaskPayload, error) {
			tasks[0].AnalysisDecision = FullAnalysis
			return tasks, nil
		},
		reserveTaskFn: func(TaskPayload) (AdmissionLease, error) {
			reserveCalls++
			cancel()
			return AdmissionLease{}, ErrAdmissionUnavailable
		},
	}

	executed, err := d.RunSingleTask(ctx, TaskPayload{FlacPath: path, TrackNumber: 1})
	if !executed || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected claimed task to exit on cancellation, executed=%v err=%v", executed, err)
	}
	if reserveCalls != 1 {
		t.Fatalf("reserve calls=%d want=1 after cancellation", reserveCalls)
	}
	got, err := db.GetTaskState(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusFailedMaybeRetry {
		t.Fatalf("task status=%s want=%s", got.Status, state.StatusFailedMaybeRetry)
	}
}

func TestSingleTaskPermanentBudgetExcessDoesNotRetry(t *testing.T) {
	db, err := state.InitDB(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path := filepath.Join(t.TempDir(), "oversized.flac")
	reserveCalls := 0
	d := &Dispatcher{
		config: Config{GatekeeperRetryDelaySec: 60},
		db:     db,
		prepareAnalysisFn: func(_ context.Context, tasks []TaskPayload) ([]TaskPayload, error) {
			tasks[0].AnalysisDecision = FullAnalysis
			return tasks, nil
		},
		reserveTaskFn: func(TaskPayload) (AdmissionLease, error) {
			reserveCalls++
			return AdmissionLease{}, fmt.Errorf("test budget: %w", ErrPermanentBudgetExcess)
		},
	}
	executed, err := d.RunSingleTask(t.Context(), TaskPayload{FlacPath: path, TrackNumber: 1})
	if !executed || !errors.Is(err, ErrPermanentBudgetExcess) {
		t.Fatalf("expected permanent rejection, executed=%v err=%v", executed, err)
	}
	if reserveCalls != 1 {
		t.Fatalf("reserve calls=%d want=1", reserveCalls)
	}
	got, err := db.GetTaskState(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusFailed {
		t.Fatalf("task status=%s want=%s", got.Status, state.StatusFailed)
	}
}

func TestBindSingleExecutionContext(t *testing.T) {
	d := &Dispatcher{}
	ctx, cancel := context.WithCancel(context.Background())
	release := d.BindSingleExecutionContext(ctx)
	cancel()
	if !errors.Is(d.currentExecutionContext().Err(), context.Canceled) {
		t.Fatal("bound context cancellation was not visible")
	}
	release()
	if d.currentExecutionContext().Err() != nil {
		t.Fatal("execution context was not released")
	}
}
