package dispatcher

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestGPUArbiterFIFOAndCancellation(t *testing.T) {
	arb := NewGPUArbiter()

	// Already-canceled context must fail immediately without taking ownership.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := arb.Acquire(cancelledCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on pre-canceled context, got %v", err)
	}
	if err := arb.Acquire(context.Background()); err != nil {
		t.Fatalf("expected unowned arbiter to grant immediately after canceled attempt, got %v", err)
	}

	// Queue waiter1, waiter2 (canceled mid-wait), waiter3 deterministically on queue insertion.
	w1Done := make(chan struct{})
	w2Err := make(chan error, 1)
	order := make([]string, 0, 2)
	var mu sync.Mutex

	w1Queued := make(chan struct{})
	go func() {
		defer close(w1Done)
		ctx := withGPUArbiterQueuedHook(context.Background(), func() { close(w1Queued) })
		if err := arb.Acquire(ctx); err != nil {
			t.Errorf("waiter1 acquire failed: %v", err)
			return
		}
		mu.Lock()
		order = append(order, "w1")
		mu.Unlock()
		arb.Release()
	}()
	<-w1Queued

	w2BaseCtx, w2Cancel := context.WithCancel(context.Background())
	w2Queued := make(chan struct{})
	w2Ctx := withGPUArbiterQueuedHook(w2BaseCtx, func() { close(w2Queued) })
	go func() {
		w2Err <- arb.Acquire(w2Ctx)
	}()
	<-w2Queued
	w2Cancel()
	if err := <-w2Err; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected waiter2 canceled, got %v", err)
	}

	w3Done := make(chan struct{})
	w3Queued := make(chan struct{})
	go func() {
		defer close(w3Done)
		ctx := withGPUArbiterQueuedHook(context.Background(), func() { close(w3Queued) })
		if err := arb.Acquire(ctx); err != nil {
			t.Errorf("waiter3 acquire failed: %v", err)
			return
		}
		mu.Lock()
		order = append(order, "w3")
		mu.Unlock()
		arb.Release()
	}()
	<-w3Queued

	arb.Release()
	<-w1Done
	<-w3Done

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "w1" || order[1] != "w3" {
		t.Fatalf("unexpected FIFO order: %v, want [w1 w3]", order)
	}
}

func TestGPUArbiterBoundedPostDemucsFeatureTurn(t *testing.T) {
	arb := NewGPUArbiter()
	if err := arb.AcquireDemucs(context.Background()); err != nil {
		t.Fatalf("initial AcquireDemucs failed: %v", err)
	}

	var mu sync.Mutex
	order := make([]string, 0, 3)

	waiterAcquire := func(label string, fn func(context.Context) error) <-chan struct{} {
		queued := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			ctx := withGPUArbiterQueuedHook(context.Background(), func() { close(queued) })
			if err := fn(ctx); err != nil {
				t.Errorf("%s acquire failed: %v", label, err)
				return
			}
			mu.Lock()
			order = append(order, label)
			mu.Unlock()
			arb.Release()
		}()
		<-queued
		return done
	}

	// Queue Demucs1 first, then Feature1 and Feature2 behind it.
	d1Done := waiterAcquire("demucs1", arb.AcquireDemucs)
	f1Done := waiterAcquire("feature1", arb.AcquireFeatureBatch)
	f2Done := waiterAcquire("feature2", arb.AcquireFeatureBatch)

	// Register one bounded post-Demucs feature turn before releasing.
	arb.RegisterPostDemucsFeatureTurn()
	arb.Release()

	<-d1Done
	<-f1Done
	<-f2Done

	mu.Lock()
	defer mu.Unlock()
	// Feature1 jumps ahead of Demucs1 (1 bounded turn), then Demucs1 runs before Feature2.
	want := []string{"feature1", "demucs1", "feature2"}
	if len(order) != len(want) {
		t.Fatalf("order len = %d (%v), want %v", len(order), order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("grant order = %v, want %v", order, want)
		}
	}
}

func TestGPUArbiterCanceledFeatureBatchPreservesCredit(t *testing.T) {
	arb := NewGPUArbiter()
	if err := arb.AcquireDemucs(context.Background()); err != nil {
		t.Fatalf("initial AcquireDemucs failed: %v", err)
	}

	var mu sync.Mutex
	order := make([]string, 0, 2)

	// Queue Demucs1 first.
	d1Queued := make(chan struct{})
	d1Done := make(chan struct{})
	go func() {
		defer close(d1Done)
		ctx := withGPUArbiterQueuedHook(context.Background(), func() { close(d1Queued) })
		if err := arb.AcquireDemucs(ctx); err != nil {
			t.Errorf("demucs1 acquire failed: %v", err)
			return
		}
		mu.Lock()
		order = append(order, "demucs1")
		mu.Unlock()
		arb.Release()
	}()
	<-d1Queued

	// Queue Feature1 and block it inside its queued hook (after insertion into waiters,
	// before entering select) so we can exercise cancellation AT handoff after usedCredit is set.
	f1BaseCtx, f1Cancel := context.WithCancel(context.Background())
	f1HookEntered := make(chan struct{})
	f1HookRelease := make(chan struct{})
	f1Err := make(chan error, 1)
	go func() {
		ctx := withGPUArbiterQueuedHook(f1BaseCtx, func() {
			close(f1HookEntered)
			<-f1HookRelease
		})
		f1Err <- arb.AcquireFeatureBatch(ctx)
	}()
	<-f1HookEntered

	f2Queued := make(chan struct{})
	f2Done := make(chan struct{})
	go func() {
		defer close(f2Done)
		ctx := withGPUArbiterQueuedHook(context.Background(), func() { close(f2Queued) })
		if err := arb.AcquireFeatureBatch(ctx); err != nil {
			t.Errorf("feature2 acquire failed: %v", err)
			return
		}
		mu.Lock()
		order = append(order, "feature2")
		mu.Unlock()
		arb.Release()
	}()
	<-f2Queued

	// Register credit and release Demucs while Feature1 is in waiters; handOffLocked selects
	// Feature1 and sets usedCredit=true. Then cancel Feature1 before unblocking its hook so
	// Feature1 sees ctx.Err()!=nil on handoff and must restore credit for Feature2.
	arb.RegisterPostDemucsFeatureTurn()
	arb.Release()
	f1Cancel()
	close(f1HookRelease)
	if err := <-f1Err; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected feature1 canceled at handoff, got %v", err)
	}

	<-d1Done
	<-f2Done

	mu.Lock()
	defer mu.Unlock()
	want := []string{"feature2", "demucs1"}
	if len(order) != len(want) || order[0] != want[0] || order[1] != want[1] {
		t.Fatalf("grant order after canceled FeatureBatch = %v, want %v", order, want)
	}
}

func TestGPUArbiterDelayedFeatureEnqueueWaitsBeforeRelease(t *testing.T) {
	arb := NewGPUArbiter()
	if err := arb.AcquireDemucs(context.Background()); err != nil {
		t.Fatalf("initial AcquireDemucs failed: %v", err)
	}

	var mu sync.Mutex
	order := make([]string, 0, 2)

	// Queue Demucs1 while Demucs0 still holds GPU.
	d1Queued := make(chan struct{})
	d1Granted := make(chan struct{})
	d1Done := make(chan struct{})
	go func() {
		defer close(d1Done)
		ctx := withGPUArbiterQueuedHook(context.Background(), func() { close(d1Queued) })
		if err := arb.AcquireDemucs(ctx); err != nil {
			t.Errorf("demucs1 acquire failed: %v", err)
			return
		}
		close(d1Granted)
		mu.Lock()
		order = append(order, "demucs1")
		mu.Unlock()
		arb.Release()
	}()
	<-d1Queued

	// Exercise the real stemWavefront + consume(FeatureLaneGPU) + WaitFirstGPUFeatureQueued handoff
	// with a channel-gated GPU feature consumer.
	lifecycle, child, err := NewStemLifecycle(t.Context(), "req-arbiter-delay", 99, []string{"mix"}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()

	gpuEntered := make(chan struct{})
	gpuGate := make(chan struct{})
	runner, err := newStemWavefront(child, 1, lifecycle, 1, func(ctx context.Context, lane FeatureLane, payload ExtractAllPayload) (*DaemonResponse, error) {
		if lane == FeatureLaneCPU {
			return &DaemonResponse{Librosa: map[string]interface{}{"ok": true}, Essentia: map[string]interface{}{"ok": true}}, nil
		}
		close(gpuEntered)
		<-gpuGate
		if err := arb.AcquireFeatureBatch(ctx); err != nil {
			return nil, err
		}
		mu.Lock()
		order = append(order, "feature1")
		mu.Unlock()
		arb.Release()
		return &DaemonResponse{Tensor: map[string]interface{}{"ok": true}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := runner.Publish(DemucsStemReadyEvent{
		Status:     "stem_ready",
		RequestID:  "req-arbiter-delay",
		Generation: 99,
		Stem:       "mix",
		AudioHash:  "hash-delay",
		SR:         44100,
		Info:       StemInfo{Shape: []int64{1, 4}, Dtype: "float32"},
	}); err != nil {
		t.Fatal(err)
	}
	<-gpuEntered

	waitDone := make(chan bool, 1)
	go func() {
		waitDone <- runner.WaitFirstGPUFeatureQueued(context.Background())
	}()

	// WaitFirstGPUFeatureQueued and Demucs1 must remain blocked while FeatureBatch has not queued yet.
	select {
	case got := <-waitDone:
		t.Fatalf("WaitFirstGPUFeatureQueued returned early (%v) before AcquireFeatureBatch queued", got)
	case <-d1Granted:
		t.Fatal("demucs1 granted before FeatureBatch entered queue")
	case <-time.After(30 * time.Millisecond):
	}

	close(gpuGate)
	if enqueued := <-waitDone; !enqueued {
		t.Fatal("expected WaitFirstGPUFeatureQueued to report true once FeatureBatch queued")
	}
	arb.RegisterPostDemucsFeatureTurn()
	arb.Release()

	runner.CloseInput()
	if _, _, _, _, waitErr := runner.Wait(); waitErr != nil {
		t.Fatalf("wavefront wait failed: %v", waitErr)
	}
	<-d1Done

	mu.Lock()
	defer mu.Unlock()
	want := []string{"feature1", "demucs1"}
	if len(order) != len(want) || order[0] != want[0] || order[1] != want[1] {
		t.Fatalf("order with delayed feature enqueue = %v, want %v", order, want)
	}

	// Verify WaitFirstGPUFeatureQueued returns false when GPU consumer exits without enqueueing.
	lc2, child2, err := NewStemLifecycle(t.Context(), "req-arbiter-noqueue", 100, []string{"mix"}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lc2.Close()
	w2, err := newStemWavefront(child2, 1, lc2, 1, func(ctx context.Context, lane FeatureLane, payload ExtractAllPayload) (*DaemonResponse, error) {
		return &DaemonResponse{Librosa: map[string]interface{}{"ok": true}, Essentia: map[string]interface{}{"ok": true}, Tensor: map[string]interface{}{"ok": true}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	w2.CloseInput()
	if enqueued := w2.WaitFirstGPUFeatureQueued(context.Background()); enqueued {
		t.Fatal("expected WaitFirstGPUFeatureQueued to return false when consumer exited without enqueueing")
	}
	_, _, _, _, _ = w2.Wait()
}

func TestGPUArbiterConcurrentStressNoDeadlock(t *testing.T) {
	arb := NewGPUArbiter()
	const workers = 12
	const iters = 25

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				switch id % 3 {
				case 0:
					if err := arb.AcquireDemucs(ctx); err != nil {
						t.Errorf("worker %d AcquireDemucs failed: %v", id, err)
					} else {
						if i%2 == 0 {
							arb.RegisterPostDemucsFeatureTurn()
						}
						arb.Release()
					}
				case 1:
					if err := arb.AcquireFeatureBatch(ctx); err != nil {
						t.Errorf("worker %d AcquireFeatureBatch failed: %v", id, err)
					} else {
						arb.Release()
					}
				default:
					if err := arb.Acquire(ctx); err != nil {
						t.Errorf("worker %d Acquire failed: %v", id, err)
					} else {
						arb.Release()
					}
				}
				cancel()
			}
		}(w)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("GPUArbiter concurrent stress timed out (possible deadlock)")
	}

	// Arbiter must be free after all goroutines release.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := arb.Acquire(ctx); err != nil {
		t.Fatalf("expected arbiter unowned after stress test, got %v", err)
	}
	arb.Release()
}
