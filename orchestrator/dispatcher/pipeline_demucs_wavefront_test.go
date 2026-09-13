package dispatcher

import (
	"context"
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
	runner, err := newStemWavefront(child, lifecycle, func(ctx context.Context, lane FeatureLane, payload ExtractAllPayload) (*DaemonResponse, error) {
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
