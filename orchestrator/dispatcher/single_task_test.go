package dispatcher

import (
	"context"
	"database/sql"
	"errors"
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
