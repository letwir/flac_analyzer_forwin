package dispatcher

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

func TestGPUArbiterFIFO(t *testing.T) {
	arbiter := NewGPUArbiter()
	if err := arbiter.Acquire(t.Context()); err != nil { // Hold ownership
		t.Fatal(err)
	}

	var order []int
	acquired := make(chan struct{}, 3)
	for i := range 3 {
		go func(id int) {
			if err := arbiter.Acquire(t.Context()); err != nil {
				acquired <- struct{}{}
				return
			}
			order = append(order, id)
			arbiter.Release()
			acquired <- struct{}{}
		}(i)
		waitForGPUArbiterWaiters(t, arbiter, i+1)
	}

	arbiter.Release() // Start the chain

	for i := 0; i < 3; i++ {
		<-acquired
	}
	if len(order) != 3 || order[0] != 0 || order[1] != 1 || order[2] != 2 {
		t.Fatalf("expected FIFO order [0 1 2], got: %v", order)
	}
}

func TestGPUArbiterCanceledWaiterRemoval(t *testing.T) {
	arbiter := NewGPUArbiter()
	if err := arbiter.Acquire(t.Context()); err != nil { // Hold ownership
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- arbiter.Acquire(ctx)
	}()

	waitForGPUArbiterWaiters(t, arbiter, 1)
	cancel()
	err := <-errCh
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}

	arbiter.mu.Lock()
	count := len(arbiter.waiters)
	arbiter.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected 0 waiters after cancellation, got: %d", count)
	}
}

func waitForGPUArbiterWaiters(t *testing.T, arbiter *GPUArbiter, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		arbiter.mu.Lock()
		waiters := len(arbiter.waiters)
		arbiter.mu.Unlock()
		if waiters == count {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("GPU arbiter waiters did not reach %d before timeout", count)
}

func TestGPUArbiterRetryableDeadlineClassification(t *testing.T) {
	d := &Dispatcher{
		gpuArbiter: NewGPUArbiter(),
	}
	if err := d.gpuArbiter.Acquire(t.Context()); err != nil { // Hold the arbiter lock
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	_, err := d.executeFeatureLane(ctx, FeatureLaneGPU, ExtractAllPayload{})
	if !errors.Is(err, ErrRetryableTimeout) {
		t.Fatalf("expected ErrRetryableTimeout when GPU arbiter times out, got: %v", err)
	}

	d.gpuArbiter.Release()

	// Now it should pass the arbiter but fail at pool check
	_, err = d.executeFeatureLane(t.Context(), FeatureLaneGPU, ExtractAllPayload{})
	if err == nil || !strings.Contains(err.Error(), "worker daemon pool is unavailable") {
		t.Fatalf("expected pool unavailable error, got: %v", err)
	}
}
