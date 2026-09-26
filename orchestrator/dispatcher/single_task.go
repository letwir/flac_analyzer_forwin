package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"flac_analyzer/orchestrator/state"
)

// BindSingleExecutionContext installs the process-lifetime cancellation source
// before CUE inspection and keeps it active through every sequential track.
func (d *Dispatcher) BindSingleExecutionContext(ctx context.Context) func() {
	d.executionCtxMu.Lock()
	d.executionCtx = ctx
	d.executionCtxMu.Unlock()
	return func() {
		d.executionCtxMu.Lock()
		d.executionCtx = nil
		d.executionCtxMu.Unlock()
	}
}

func (d *Dispatcher) ExpandSingleFile(payload TaskPayload) ([]TaskPayload, error) {
	cue, err := d.InspectCue(payload.FlacPath)
	if err != nil {
		return nil, fmt.Errorf("inspect CUE metadata: %w", err)
	}
	if cue == nil || len(cue.Tracks) == 0 {
		payload.TrackNumber = 1
		payload.StartSample = 0
		payload.EndSample = 0
		return []TaskPayload{payload}, nil
	}
	tasks := make([]TaskPayload, 0, len(cue.Tracks))
	for _, tr := range cue.Tracks {
		t := payload
		t.TrackNumber, t.StartSample, t.EndSample = tr.TrackNumber, tr.StartSample, tr.EndSample
		t.SampleRate = cue.SampleRate
		t.Title, t.Artist = tr.Title.String(), tr.Artist.String()
		t.Album, t.AlbumArtist = cue.Album.String(), cue.AlbumArtist.String()
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func (d *Dispatcher) RunSingleTask(ctx context.Context, task TaskPayload) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	abs, err := filepath.Abs(task.FlacPath)
	if err != nil {
		return false, fmt.Errorf("resolve FLAC path: %w", err)
	}
	task.FlacPath = filepath.Clean(abs)

	planned, err := d.prepareAnalysisTasks(ctx, []TaskPayload{task})
	if err != nil {
		return false, fmt.Errorf("preflight failed: %w", err)
	}
	task = planned[0]

	payload, err := json.Marshal(task)
	if err != nil {
		return false, fmt.Errorf("encode task payload: %w", err)
	}

	if task.AnalysisDecision == Skip {
		claimed, err := d.db.ClaimSingleTask(task.FlacPath, task.TrackNumber, string(payload), task.Force, true)
		if err != nil || !claimed {
			return claimed, err
		}
		_ = d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusCompleted, "analysis preflight: complete")
		_ = d.db.Flush()
		return true, nil
	}

	claimed, err := d.db.ClaimSingleTaskRequiredAnalysis(task.FlacPath, task.TrackNumber, string(payload), task.Force, true)
	if err != nil || !claimed {
		return claimed, err
	}

	for {
		lease, reserveErr := d.reserveTaskAdmission(task)
		if reserveErr == nil {
			reserveErr = d.recheckTaskAdmissionBeforeDispatch(task, lease)
			if reserveErr == nil {
				break
			}
			d.releaseUnstartedTaskAdmission(task)
		}
		if errors.Is(reserveErr, ErrPermanentBudgetExcess) {
			_ = d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusFailed, reserveErr.Error())
			_ = d.db.Flush()
			return true, reserveErr
		}
		wait := secondsToDuration(d.GetConfig().GatekeeperRetryDelaySec)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusFailedMaybeRetry, ctx.Err().Error())
			_ = d.db.Flush()
			return true, ctx.Err()
		case <-timer.C:
		}
	}

	d.executeTaskPipelineWithMode(1, task, true)
	if err := d.db.Flush(); err != nil {
		return true, fmt.Errorf("flush terminal state: %w", err)
	}
	st, err := d.db.GetTaskState(task.FlacPath, task.TrackNumber)
	if err != nil {
		return true, fmt.Errorf("read terminal state: %w", err)
	}
	if st.Status != state.StatusCompleted {
		return true, fmt.Errorf("task ended as %s: %s", st.Status, st.ErrorMessage)
	}
	return true, nil
}

func NewSingleFilePayload(path string, force bool) (TaskPayload, error) {
	canonical, err := canonicalSingleFilePath(path)
	if err != nil {
		return TaskPayload{}, err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return TaskPayload{}, err
	}
	return TaskPayload{FlacPath: canonical, FileSize: info.Size(), Force: force}, nil
}
