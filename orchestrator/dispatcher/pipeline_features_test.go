package dispatcher

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunFeatureLanesCancelsPeerAndWaitsForCleanup(t *testing.T) {
	cpuFailure := errors.New("cpu extraction failed")
	gpuStarted := make(chan struct{})
	var cpuReleased, gpuReleased atomic.Int32
	var gpuCancelled atomic.Bool

	_, _, err := runFeatureLanes(
		t.Context(),
		func(context.Context) (*DaemonResponse, error) {
			defer cpuReleased.Add(1)
			<-gpuStarted
			return nil, cpuFailure
		},
		func(ctx context.Context) (*DaemonResponse, error) {
			defer gpuReleased.Add(1)
			close(gpuStarted)
			<-ctx.Done()
			gpuCancelled.Store(true)
			return nil, ctx.Err()
		},
	)
	if !errors.Is(err, cpuFailure) {
		t.Fatalf("error = %v, want CPU failure", err)
	}
	if !gpuCancelled.Load() {
		t.Fatal("GPU lane did not observe CPU lane cancellation")
	}
	if cpuReleased.Load() != 1 || gpuReleased.Load() != 1 {
		t.Fatalf("lane cleanup counts = cpu:%d gpu:%d, want 1:1", cpuReleased.Load(), gpuReleased.Load())
	}
}

func TestRunFeatureLanesJoinsBackwardCompatibleOutputs(t *testing.T) {
	cpuResp, gpuResp, err := runFeatureLanes(
		t.Context(),
		func(context.Context) (*DaemonResponse, error) {
			return &DaemonResponse{
				Librosa:  map[string]interface{}{"mix": map[string]interface{}{"bpm": 120.0}},
				Essentia: map[string]interface{}{"mood_happy": 0.8},
			}, nil
		},
		func(context.Context) (*DaemonResponse, error) {
			return &DaemonResponse{Tensor: map[string]interface{}{"mix": map[string]interface{}{"flux": 2.0}}}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := joinFeatureLaneResponses(cpuResp, gpuResp)
	if err != nil {
		t.Fatal(err)
	}
	for name, output := range map[string]string{
		"librosa":  outputs.LibOut,
		"tensor":   outputs.TensorOut,
		"essentia": outputs.EssOut,
	} {
		if !strings.Contains(output, "mix") && name != "essentia" {
			t.Fatalf("%s output lost legacy shape: %s", name, output)
		}
	}
	if !strings.Contains(outputs.EssOut, "mood_happy") {
		t.Fatalf("essentia output lost prediction: %s", outputs.EssOut)
	}
}
