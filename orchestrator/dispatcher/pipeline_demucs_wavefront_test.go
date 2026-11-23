package dispatcher

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestStemWavefrontStartsBothLanesOnFirstPublishedStem(t *testing.T) {
	lifecycle, child, err := NewStemLifecycle(t.Context(), "request-21", 21, []string{"mix", "bass"}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()
	firstCPU := make(chan struct{})
	firstGPU := make(chan struct{})
	releaseFirst := make(chan struct{})
	runner, err := newStemWavefront(child, 4, lifecycle, 1, func(ctx context.Context, lane FeatureLane, payload ExtractAllPayload) (*DaemonResponse, error) {
		if _, isFirst := payload.Stems["mix"]; isFirst {
			if lane == FeatureLaneCPU {
				close(firstCPU)
			} else {
				close(firstGPU)
			}
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			}
		}
		switch lane {
		case FeatureLaneCPU:
			return &DaemonResponse{Librosa: map[string]interface{}{"demucs": map[string]interface{}{payload.TrackHash: payload.Stems}}, Essentia: map[string]interface{}{"ok": true}}, nil
		case FeatureLaneGPU:
			return &DaemonResponse{Tensor: map[string]interface{}{"demucs": map[string]interface{}{payload.TrackHash: payload.Stems}}}, nil
		default:
			return nil, context.Canceled
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	first := DemucsStemReadyEvent{Status: "stem_ready", RequestID: "request-21", Generation: 21, Stem: "mix", AudioHash: "hash", SR: 44100, Info: StemInfo{Shape: []int64{1, 4}, Dtype: "float32"}}
	if err := runner.Publish(first); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstCPU:
	case <-time.After(time.Second):
		t.Fatal("CPU lane did not start after first stem publication")
	}
	select {
	case <-firstGPU:
	case <-time.After(time.Second):
		t.Fatal("GPU lane did not start after first stem publication")
	}
	close(releaseFirst)
	second := first
	second.Stem = "bass"
	if err := runner.Publish(second); err != nil {
		t.Fatal(err)
	}
	runner.CloseInput()
	cpu, gpu, hash, sr, err := runner.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if hash != "hash" || sr != 44100 || len(cpu.Librosa) == 0 || len(gpu.Tensor) == 0 {
		t.Fatalf("wavefront result hash=%q sr=%d cpu=%+v gpu=%+v", hash, sr, cpu, gpu)
	}
	if err := runner.ValidateFinalStems(map[string]StemInfo{"mix": first.Info, "bass": second.Info}); err != nil {
		t.Fatal(err)
	}
	for _, stem := range []string{"mix", "bass"} {
		state, refs, stateErr := lifecycle.State(21, stem)
		if stateErr != nil || state != StemReleased || refs != 0 {
			t.Fatalf("%s lifecycle state=%d refs=%d err=%v", stem, state, refs, stateErr)
		}
	}
}

func TestStemWavefrontBoundedHandoff(t *testing.T) {
	stems := []string{"mix", "bass", "drums", "vocals", "other"}
	lifecycle, child, err := NewStemLifecycle(t.Context(), "request-bound", 22, stems, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()

	ctx, cancel := context.WithCancel(child)
	defer cancel()

	runner, err := newStemWavefront(ctx, len(stems), lifecycle, 1, func(ctx context.Context, lane FeatureLane, payload ExtractAllPayload) (*DaemonResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, stem := range stems {
		ev := DemucsStemReadyEvent{
			Status:     "stem_ready",
			RequestID:  "request-bound",
			Generation: 22,
			Stem:       stem,
			AudioHash:  "hash",
			SR:         44100,
			Info:       StemInfo{Shape: []int64{1, 4}, Dtype: "float32"},
		}
		if err := runner.Publish(ev); err != nil {
			t.Fatalf("failed to queue stem %q: %v", stem, err)
		}
	}

	if cap(runner.cpuJobs) != len(stems) || cap(runner.gpuJobs) != len(stems) {
		t.Fatalf("expected queue capacities to be %d, got cpu: %d, gpu: %d", len(stems), cap(runner.cpuJobs), cap(runner.gpuJobs))
	}

	runner.CloseInput()
	cancel()
	_, _, _, _, err = runner.Wait()
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
}

func TestStemWavefrontPreservesSubsequentTeardownErrorAfterFirstFailure(t *testing.T) {
	lifecycle, child, err := NewStemLifecycle(t.Context(), "request-multi-err", 23, []string{"mix"}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()

	cpuErr := errors.New("cpu lane failed first")
	gpuRetireErr := errors.New("gpu defer retire timed out")
	gpuStarted := make(chan struct{})
	var gpuStartedOnce sync.Once

	runner, err := newStemWavefront(child, 1, lifecycle, 1, func(ctx context.Context, lane FeatureLane, _ ExtractAllPayload) (*DaemonResponse, error) {
		switch lane {
		case FeatureLaneCPU:
			<-gpuStarted
			return nil, cpuErr
		case FeatureLaneGPU:
			gpuStartedOnce.Do(func() { close(gpuStarted) })
			<-ctx.Done()
			// Preserve first cancellation cause and add a subsequent teardown/DeferRetire error.
			return nil, errors.Join(context.Cause(ctx), gpuRetireErr)
		default:
			return nil, context.Canceled
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := runner.Publish(DemucsStemReadyEvent{
		Status:     "stem_ready",
		RequestID:  "request-multi-err",
		Generation: 23,
		Stem:       "mix",
		AudioHash:  "hash",
		SR:         44100,
		Info:       StemInfo{Shape: []int64{1, 4}, Dtype: "float32"},
	}); err != nil {
		t.Fatal(err)
	}
	runner.CloseInput()

	_, _, _, _, waitErr := runner.Wait()
	if waitErr == nil {
		t.Fatal("expected non-nil error from Wait")
	}
	if !errors.Is(waitErr, cpuErr) {
		t.Fatalf("expected Wait error to include first failure %v, got %v", cpuErr, waitErr)
	}
	if !errors.Is(waitErr, gpuRetireErr) {
		t.Fatalf("expected Wait error to include subsequent teardown error %v, got %v", gpuRetireErr, waitErr)
	}
	if !errors.Is(context.Cause(child), cpuErr) {
		t.Fatalf("expected lifecycle context cause to remain first failure %v, got %v", cpuErr, context.Cause(child))
	}
}

func TestStemWavefrontPreservesParentCancellationCauseAndSubsequentRetirementError(t *testing.T) {
	parentCause := errors.New("parent demucs aborted externally")
	parentCtx, parentCancel := context.WithCancelCause(t.Context())
	defer parentCancel(nil)

	lifecycle, child, err := NewStemLifecycle(parentCtx, "request-parent-cause", 24, []string{"mix"}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()

	gpuRetireErr := errors.New("gpu defer retire timed out on cancel")
	gpuStarted := make(chan struct{})
	var gpuStartedOnce sync.Once

	runner, err := newStemWavefront(child, 1, lifecycle, 1, func(ctx context.Context, lane FeatureLane, _ ExtractAllPayload) (*DaemonResponse, error) {
		if lane != FeatureLaneGPU {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		gpuStartedOnce.Do(func() { close(gpuStarted) })
		<-ctx.Done()
		// Return generic ctx.Err() joined with teardown error (without calling context.Cause(ctx)),
		// mimicking a lane whose RPC/wait returns context.Canceled + DeferRetire error.
		return nil, errors.Join(ctx.Err(), gpuRetireErr)
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := runner.Publish(DemucsStemReadyEvent{
		Status:     "stem_ready",
		RequestID:  "request-parent-cause",
		Generation: 24,
		Stem:       "mix",
		AudioHash:  "hash",
		SR:         44100,
		Info:       StemInfo{Shape: []int64{1, 4}, Dtype: "float32"},
	}); err != nil {
		t.Fatal(err)
	}
	<-gpuStarted
	parentCancel(parentCause)
	runner.CloseInput()

	_, _, _, _, waitErr := runner.Wait()
	if waitErr == nil {
		t.Fatal("expected Wait error after parent cancellation")
	}
	if !errors.Is(waitErr, parentCause) {
		t.Fatalf("expected Wait error to preserve parent cancellation cause %v, got %v", parentCause, waitErr)
	}
	if !errors.Is(waitErr, gpuRetireErr) {
		t.Fatalf("expected Wait error to also preserve subsequent retirement error %v, got %v", gpuRetireErr, waitErr)
	}
}

func TestJoinWavefrontCausePreservesBothErrors(t *testing.T) {
	cause := errors.New("custom parent cause")
	retire := errors.New("retire timeout")
	waveErr := errors.Join(cause, retire)
	got := joinWavefrontCause(context.Canceled, waveErr)
	if !errors.Is(got, context.Canceled) || !errors.Is(got, cause) || !errors.Is(got, retire) {
		t.Fatalf("expected joined error to retain primary, cause, and retirement error, got %v", got)
	}
	if joinWavefrontCause(context.Canceled, nil) != context.Canceled {
		t.Fatal("nil waveErr must return primary unchanged")
	}
	primary := errors.Join(cause, retire)
	if joinWavefrontCause(primary, cause) != primary {
		t.Fatal("waveErr already contained in primary must not be re-joined")
	}
}

func TestStemWavefrontWaitPreservesParentCauseAfterLocalFailureCancelledChildFirst(t *testing.T) {
	parentCause := errors.New("parent cancelled during retirement")
	parentCtx, parentCancel := context.WithCancelCause(t.Context())
	defer parentCancel(nil)

	lifecycle, child, err := NewStemLifecycle(parentCtx, "request-parent-late", 25, []string{"mix"}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()

	localErr := errors.New("local lane failed before parent cancel")
	release := make(chan struct{})
	published := make(chan struct{})
	runner, err := newStemWavefront(child, 1, lifecycle, 1, func(ctx context.Context, lane FeatureLane, _ ExtractAllPayload) (*DaemonResponse, error) {
		if lane == FeatureLaneCPU {
			// Fail only after Publish has delivered to both lanes, so the child cancellation
			// cannot interrupt Publish itself.
			<-published
			return nil, localErr
		}
		<-ctx.Done()
		<-release
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.parent = parentCtx
	if err := runner.Publish(DemucsStemReadyEvent{
		Status: "stem_ready", RequestID: "request-parent-late", Generation: 25, Stem: "mix",
		AudioHash: "hash", SR: 44100, Info: StemInfo{Shape: []int64{1, 4}, Dtype: "float32"},
	}); err != nil {
		t.Fatal(err)
	}
	close(published)
	runner.CloseInput()
	<-child.Done() // child is cancelled by the local failure first.
	parentCancel(parentCause)
	close(release)

	_, _, _, _, waitErr := runner.Wait()
	if !errors.Is(waitErr, localErr) || !errors.Is(waitErr, parentCause) {
		t.Fatalf("expected Wait to keep local failure and later parent cause, got %v", waitErr)
	}
}
