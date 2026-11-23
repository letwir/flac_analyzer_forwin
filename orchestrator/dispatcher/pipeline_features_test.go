package dispatcher

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

func TestExecuteFeatureLane_GPUNilGuards(t *testing.T) {
	noArbiter := &Dispatcher{}
	_, err := noArbiter.executeFeatureLane(t.Context(), FeatureLaneGPU, ExtractAllPayload{})
	if err == nil || !strings.Contains(err.Error(), "GPU arbiter is unavailable") {
		t.Fatalf("expected arbiter unavailable, got %v", err)
	}

	noPool := &Dispatcher{gpuArbiter: NewGPUArbiter()}
	_, err = noPool.executeFeatureLane(t.Context(), FeatureLaneGPU, ExtractAllPayload{})
	if err == nil || !strings.Contains(err.Error(), "gpu worker daemon pool is unavailable") {
		t.Fatalf("expected pool unavailable, got %v", err)
	}
}

func TestTrimIdleGPUDaemons_HonorsRetentionAndLeavesCheckedOut(t *testing.T) {
	pool := NewWorkerDaemonPool(2, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	t.Cleanup(func() { _ = pool.Close() })
	idle := &WorkerDaemonClient{id: 1, testHealthy: true}
	checkedOut := &WorkerDaemonClient{id: 2, testHealthy: true}
	pool.allDaemons = append(pool.allDaemons, idle, checkedOut)
	pool.daemons <- idle

	arbiter := NewGPUArbiter()
	// With production Config.RetainGpuDaemonIdle=true, idle healthy clients remain warm.
	d := &Dispatcher{
		gpuArbiter:    arbiter,
		gpuDaemonPool: pool,
		config:        Config{RetainGpuDaemonIdle: true},
	}
	t.Cleanup(d.stopIdleGPUDaemonTrim)
	d.trimIdleGPUDaemons()
	if !idle.IsHealthy() || !checkedOut.IsHealthy() || len(pool.daemons) != 1 || len(pool.allDaemons) != 2 {
		t.Fatalf("expected healthy idle GPU daemon retained under RetainGpuDaemonIdle=true (idleHealthy=%v daemons=%d all=%d)", idle.IsHealthy(), len(pool.daemons), len(pool.allDaemons))
	}

	// Disabling RetainGpuDaemonIdle via UpdateConfig trims idle clients under gpuArbiter while sparing checkedOut.
	d.UpdateConfig(Config{RetainGpuDaemonIdle: false})
	d.trimIdleGPUDaemons()
	if idle.IsHealthy() || !checkedOut.IsHealthy() || len(pool.daemons) != 0 || len(pool.allDaemons) != 1 || pool.allDaemons[0] != checkedOut {
		t.Fatalf("expected idle GPU daemon trimmed and checkedOut spared under RetainGpuDaemonIdle=false (idleHealthy=%v checkedOutHealthy=%v all=%d)", idle.IsHealthy(), checkedOut.IsHealthy(), len(pool.allDaemons))
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)
	fmt.Fprintln(os.Stdout, `{"status":"ready","role":"feature_gpu","device":"cpu"}`)
	var b [1]byte
	if _, err := os.Stdin.Read(b[:]); err == nil {
		fmt.Fprintln(os.Stderr, "STDIN_PARTIAL_READ")
	}
	// Park without reading remaining stdin or exiting until killed by parent teardown.
	// Use a bounded timer instead of bare `select {}` so Go's all-goroutines-asleep detector never fires.
	<-time.After(time.Minute)
}

func TestWorkerDaemonClient_StalledStdinWriteCancelsAndClosesWithoutDeadlock(t *testing.T) {
	// Spawn real helper child that reads 1 byte of the oversized RPC and then leaves stdin unread,
	// stalling ExtractGPU's write in Windows OS pipe I/O until context cancellation tears down the child.
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stdin failed: %v", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		t.Fatalf("os.Pipe stdout failed: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		t.Fatalf("os.Pipe stderr failed: %v", err)
	}
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
		t.Fatalf("helper Start failed: %v", err)
	}
	_ = stdinR.Close()
	_ = stdoutW.Close()
	_ = stderrW.Close()

	c := &WorkerDaemonClient{
		id:           808,
		cmd:          cmd,
		stdin:        stdinW,
		stdoutCloser: stdoutR,
		stderr:       stderrR,
		maxRecycle:   100,
	}
	t.Cleanup(func() {
		_ = c.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	stdoutReader := bufio.NewReaderSize(stdoutR, 64*1024)
	c.stdout = stdoutReader
	readyCh := make(chan error, 1)
	go func() {
		_, readyErr := stdoutReader.ReadString('\n')
		readyCh <- readyErr
	}()
	select {
	case readyErr := <-readyCh:
		if readyErr != nil {
			t.Fatalf("failed waiting for helper ready line: %v", readyErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for helper ready line")
	}

	partialReadCh := make(chan struct{})
	stderrEOF := make(chan struct{})
	go func() {
		defer close(stderrEOF)
		r := bufio.NewReader(stderrR)
		marked := false
		for {
			line, readErr := r.ReadString('\n')
			if !marked && strings.Contains(line, "STDIN_PARTIAL_READ") {
				marked = true
				close(partialReadCh)
			}
			if readErr != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	extractDone := make(chan error, 1)
	go func() {
		_, eErr := c.ExtractGPU(ctx, ExtractAllPayload{TrackHash: strings.Repeat("A", 512*1024)})
		extractDone <- eErr
	}()

	// Wait deterministically until helper confirms reading the first byte of the oversized RPC.
	select {
	case <-partialReadCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for helper child to receive start of oversized RPC")
	}

	// Queue a second RPC (Ping) while ExtractGPU holds the RPC slot and verify its context cancels promptly.
	pingCtx, cancelPing := context.WithTimeout(t.Context(), 15*time.Millisecond)
	defer cancelPing()
	if pingErr := c.Ping(pingCtx); !errors.Is(pingErr, context.DeadlineExceeded) {
		t.Fatalf("expected queued Ping waiting on RPC slot to cancel with DeadlineExceeded, got %v", pingErr)
	}

	// Cancel stalled oversized RPC -> RPC returns bounded by its own ctx without waiting 2s,
	// and AwaitExit observes child termination, reaping, and real closure of all 3 parent handles.
	cancel()
	select {
	case extractErr := <-extractDone:
		if !errors.Is(extractErr, context.Canceled) {
			t.Fatalf("expected context.Canceled on stalled oversized RPC, got %v", extractErr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ExtractGPU writer did not return promptly under its own cancelled context")
	}

	awaitCtx, cancelAwait := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancelAwait()
	if awaitErr := c.AwaitExit(awaitCtx); awaitErr != nil {
		t.Fatalf("expected clean AwaitExit after stalled RPC kill, got %v", awaitErr)
	}

	select {
	case <-stderrEOF:
	case <-time.After(2 * time.Second):
		t.Fatal("expected parent stderr handle closed by teardown")
	}
	if cmd.ProcessState == nil || c.HasLiveProcess() {
		t.Fatalf("expected helper child reaped with populated ProcessState after teardown, state=%v live=%v", cmd.ProcessState, c.HasLiveProcess())
	}
	var probeBuf [1]byte
	if _, wErr := stdinW.Write(probeBuf[:]); !errors.Is(wErr, os.ErrClosed) {
		t.Fatalf("expected real stdin handle closed (os.ErrClosed), got %v", wErr)
	}
	if _, outErr := stdoutR.Read(probeBuf[:]); !errors.Is(outErr, os.ErrClosed) {
		t.Fatalf("expected real stdout handle closed (os.ErrClosed), got %v", outErr)
	}
	if _, errErr := stderrR.Read(probeBuf[:]); !errors.Is(errErr, os.ErrClosed) {
		t.Fatalf("expected real stderr handle closed (os.ErrClosed), got %v", errErr)
	}

	// Distinct errors with identical message text must both be preserved by combineDemucsWavefrontError.
	sepErr := errors.New("failure")
	waveErr := errors.New("failure")
	joined := combineDemucsWavefrontError(sepErr, waveErr)
	if !errors.Is(joined, sepErr) || !errors.Is(joined, waveErr) {
		t.Fatalf("expected combined error to preserve distinct sepErr and waveErr even with identical messages, got %v", joined)
	}
}

func TestGPUFeaturePoolTrimIdleLeavesCheckedOutDaemon(t *testing.T) {
	pool := NewWorkerDaemonPool(2, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	idle := &WorkerDaemonClient{}
	checkedOut := &WorkerDaemonClient{}
	pool.allDaemons = append(pool.allDaemons, idle, checkedOut)
	pool.daemons <- idle

	if count := pool.TrimIdle(0); count != 1 {
		t.Fatalf("trimmed %d idle daemons, want 1", count)
	}
	if !idle.closed || checkedOut.closed {
		t.Fatal("trim closed a checked-out daemon or retained an idle daemon")
	}
	if len(pool.allDaemons) != 1 || pool.allDaemons[0] != checkedOut {
		t.Fatalf("pool retained %d daemons, want only the checked-out daemon", len(pool.allDaemons))
	}
}

func TestExecuteFeatureLane_GPURunsCleanupOnExactClientUnderArbiter(t *testing.T) {
	pool := NewWorkerDaemonPool(2, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	t.Cleanup(func() { _ = pool.Close() })
	clientA := &WorkerDaemonClient{id: 101, testHealthy: true}
	clientB := &WorkerDaemonClient{id: 102, testHealthy: true}
	pool.allDaemons = append(pool.allDaemons, clientA, clientB)
	pool.daemons <- clientA
	pool.daemons <- clientB

	arbiter := NewGPUArbiter()
	var extractClient, cleanupClient *WorkerDaemonClient
	var arbiterBusyDuringCleanup bool
	retain := false
	d := &Dispatcher{
		gpuArbiter:            arbiter,
		gpuDaemonPool:         pool,
		retainGpuDaemonIdleFn: func() bool { return retain },
		featureOps: &featureLaneOps{
			extractGPU: func(_ context.Context, c *WorkerDaemonClient, _ ExtractAllPayload) (*DaemonResponse, error) {
				extractClient = c
				return &DaemonResponse{Status: "success", Tensor: map[string]interface{}{"ok": true}}, nil
			},
			cleanupGPU: func(_ context.Context, c *WorkerDaemonClient) error {
				if !retain {
					return errors.New("CleanupGPU must not be called when RetainGpuDaemonIdle=false")
				}
				cleanupClient = c
				probeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
				defer cancel()
				if err := arbiter.Acquire(probeCtx); err != nil {
					arbiterBusyDuringCleanup = true
				} else {
					arbiter.Release()
				}
				return nil
			},
		},
	}

	// RetainGpuDaemonIdle=true: CleanupGPU runs on exact active client under held arbiter and retains both warm.
	retain = true
	resp, err := d.executeFeatureLane(t.Context(), FeatureLaneGPU, ExtractAllPayload{})
	if err != nil || resp == nil {
		t.Fatalf("executeFeatureLane GPU failed: (%+v, %v)", resp, err)
	}
	if extractClient == nil || cleanupClient != extractClient {
		t.Fatalf("CleanupGPU ran on client=%v, want exact ExtractGPU client=%v", cleanupClient, extractClient)
	}
	if !arbiterBusyDuringCleanup {
		t.Fatal("gpuArbiter was not held during CleanupGPU on active GPU daemon")
	}
	d.trimIdleGPUDaemons()
	if clientA.closed || len(pool.allDaemons) != 2 {
		t.Fatalf("expected idle daemons retained when RetainGpuDaemonIdle=true, closed=%v len=%d", clientA.closed, len(pool.allDaemons))
	}

	// RetainGpuDaemonIdle=false: checked-out active GPU client skips CleanupGPU and is retired
	// immediately via DeferRetire inside executeFeatureLane under the held arbiter.
	retain = false
	extractClient = nil
	cleanupClient = nil
	resp, err = d.executeFeatureLane(t.Context(), FeatureLaneGPU, ExtractAllPayload{})
	if err != nil || resp == nil {
		t.Fatalf("executeFeatureLane GPU (retain=false) failed: (%+v, %v)", resp, err)
	}
	if cleanupClient != nil {
		t.Fatalf("expected CleanupGPU to be skipped when RetainGpuDaemonIdle=false, ran on %v", cleanupClient)
	}
	if extractClient == nil || !extractClient.closed || len(pool.allDaemons) != 1 {
		t.Fatalf("expected active GPU client retired immediately inside executeFeatureLane when RetainGpuDaemonIdle=false (all=%d closed=%v)", len(pool.allDaemons), extractClient != nil && extractClient.closed)
	}
	d.trimIdleGPUDaemons()
	if !clientA.closed || !clientB.closed || len(pool.allDaemons) != 0 {
		t.Fatalf("expected idle daemons trimmed when RetainGpuDaemonIdle=false, closedA=%v len=%d", clientA.closed, len(pool.allDaemons))
	}
}

func TestExecuteFeatureLane_GPUDiscardsClientOnExtractOrCleanupError(t *testing.T) {
	cases := []struct {
		name       string
		extractErr error
		cleanupErr error
	}{
		{name: "extract_error", extractErr: errors.New("cuda oom"), cleanupErr: nil},
		{name: "cleanup_error", extractErr: nil, cleanupErr: errors.New("cleanup timeout")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
			t.Cleanup(func() { _ = pool.Close() })
			client := &WorkerDaemonClient{id: 7, testHealthy: true}
			pool.allDaemons = append(pool.allDaemons, client)
			pool.daemons <- client

			d := &Dispatcher{
				config:        Config{RetainGpuDaemonIdle: true},
				gpuArbiter:    NewGPUArbiter(),
				gpuDaemonPool: pool,
				featureOps: &featureLaneOps{
					extractGPU: func(_ context.Context, _ *WorkerDaemonClient, _ ExtractAllPayload) (*DaemonResponse, error) {
						if tc.extractErr != nil {
							return nil, tc.extractErr
						}
						return &DaemonResponse{Status: "success"}, nil
					},
					cleanupGPU: func(_ context.Context, _ *WorkerDaemonClient) error {
						return tc.cleanupErr
					},
				},
			}
			_, err := d.executeFeatureLane(t.Context(), FeatureLaneGPU, ExtractAllPayload{})
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if !client.closed || len(pool.allDaemons) != 0 || len(pool.daemons) != 0 {
				t.Fatalf("poisoned GPU client was not discarded: closed=%v all=%d idle=%d", client.closed, len(pool.allDaemons), len(pool.daemons))
			}
		})
	}
}

func TestExecuteFeatureLane_GPUDiscardTimeoutFencesArbiterUntilExitConfirmed(t *testing.T) {
	pool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	t.Cleanup(func() { _ = pool.Close() })
	client := &WorkerDaemonClient{id: 88, testHealthy: true}
	pool.allDaemons = append(pool.allDaemons, client)
	pool.daemons <- client

	exitGate := make(chan struct{})
	var exitConfirmed atomic.Bool
	var closeAttempts atomic.Int32
	pool.closeHook = func(c *WorkerDaemonClient) error {
		if closeAttempts.Add(1) == 1 || !exitConfirmed.Load() {
			return errors.New("timed out waiting for worker daemon exit")
		}
		return c.Close()
	}
	arbiter := NewGPUArbiter()
	d := &Dispatcher{
		gpuArbiter:    arbiter,
		gpuDaemonPool: pool,
		featureOps: &featureLaneOps{
			extractGPU: func(_ context.Context, _ *WorkerDaemonClient, _ ExtractAllPayload) (*DaemonResponse, error) {
				return nil, errors.New("cuda failure")
			},
			retireWatcher: func(p *WorkerDaemonPool, c *WorkerDaemonClient) <-chan struct{} {
				go func() {
					<-exitGate
					exitConfirmed.Store(true)
				}()
				return p.RetireWatcher(c)
			},
		},
	}

	_, err := d.executeFeatureLane(t.Context(), FeatureLaneGPU, ExtractAllPayload{})
	if err == nil || !strings.Contains(err.Error(), "retire gpu worker daemon") {
		t.Fatalf("expected discard retirement error, got %v", err)
	}
	// Client must remain tracked in allDaemons and arbiter must stay fenced while process is alive.
	if len(pool.allDaemons) != 1 || len(pool.daemons) != 0 {
		t.Fatalf("expected retiring live client preserved in allDaemons and excluded from daemons (all=%d idle=%d)", len(pool.allDaemons), len(pool.daemons))
	}
	probeCtx, cancelProbe := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancelProbe()
	if probeErr := arbiter.Acquire(probeCtx); !errors.Is(probeErr, context.DeadlineExceeded) {
		if probeErr == nil {
			arbiter.Release()
		}
		t.Fatalf("expected gpuArbiter to stay fenced while retired process is alive, got %v", probeErr)
	}

	// Confirm process exit (attempt 1 fails awaitExit+discardClient, attempt 2 succeeds)
	// and verify arbiter releases cleanly and client is removed from allDaemons.
	close(exitGate)
	nextCtx, cancelNext := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelNext()
	if err := arbiter.Acquire(nextCtx); err != nil {
		t.Fatalf("expected gpuArbiter release after process exit confirmation, got %v", err)
	}
	arbiter.Release()
	if closeAttempts.Load() < 2 {
		t.Fatalf("expected pool watcher retries after initial close failure, got %d attempts", closeAttempts.Load())
	}
	if len(pool.allDaemons) != 0 {
		t.Fatalf("expected client removed from allDaemons after confirmed exit, got %d", len(pool.allDaemons))
	}
}

func TestExecuteFeatureLane_RecycleReleaseFailureFencesArbiterAndTransfersArenaLease(t *testing.T) {
	cpuPool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleCPU)
	gpuPool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	t.Cleanup(func() {
		_ = cpuPool.Close()
		_ = gpuPool.Close()
	})
	cpuClient := &WorkerDaemonClient{id: 1, testHealthy: true}
	gpuClient := &WorkerDaemonClient{id: 2, testHealthy: true}
	cpuPool.allDaemons = append(cpuPool.allDaemons, cpuClient)
	gpuPool.allDaemons = append(gpuPool.allDaemons, gpuClient)
	cpuPool.daemons <- cpuClient
	gpuPool.daemons <- gpuClient

	arenaPool := NewShmArenaPool(false)
	t.Cleanup(func() { _ = arenaPool.Close() })
	lease, err := arenaPool.AcquireArenaLease(1)
	if err != nil {
		t.Fatalf("AcquireArenaLease failed: %v", err)
	}

	exitGate := make(chan struct{})
	var closeExitGateOnce sync.Once
	closeExitGate := func() {
		closeExitGateOnce.Do(func() { close(exitGate) })
	}
	t.Cleanup(closeExitGate)
	var exitConfirmed atomic.Bool
	var releaseCalled atomic.Bool
	arbiter := NewGPUArbiter()
	d := &Dispatcher{
		config:        Config{RetainGpuDaemonIdle: true},
		cpuDaemonPool: cpuPool,
		gpuDaemonPool: gpuPool,
		gpuArbiter:    arbiter,
		featureOps: &featureLaneOps{
			extractCPU: func(_ context.Context, _ *WorkerDaemonClient, _ ExtractAllPayload) (*DaemonResponse, error) {
				return &DaemonResponse{Status: "success", Librosa: map[string]interface{}{"ok": true}, Essentia: map[string]interface{}{"ok": true}}, nil
			},
			extractGPU: func(_ context.Context, _ *WorkerDaemonClient, _ ExtractAllPayload) (*DaemonResponse, error) {
				return &DaemonResponse{Status: "success", Tensor: map[string]interface{}{"ok": true}}, nil
			},
			cleanupGPU: func(_ context.Context, _ *WorkerDaemonClient) error {
				return nil
			},
			releaseClient: func(p *WorkerDaemonPool, c *WorkerDaemonClient) error {
				if p == gpuPool {
					releaseCalled.Store(true)
					return p.Discard(c)
				}
				return p.ReleaseChecked(c)
			},
			retireWatcher: func(p *WorkerDaemonPool, c *WorkerDaemonClient) <-chan struct{} {
				return p.RetireWatcher(c)
			},
		},
	}
	gpuPool.closeHook = func(c *WorkerDaemonClient) error {
		if !exitConfirmed.Load() {
			return errors.New("still exiting")
		}
		return c.Close()
	}
	go func() {
		<-exitGate
		exitConfirmed.Store(true)
	}()

	_, featErr := d.executeFeaturesStage(44100, "hash", nil, &lease, StorageModeSHM, TaskPayload{}, Config{RetainGpuDaemonIdle: true})
	if featErr == nil || !strings.Contains(featErr.Error(), "retire gpu worker daemon") {
		t.Fatalf("expected recycle retirement error to propagate, got %v", featErr)
	}
	if !releaseCalled.Load() {
		t.Fatalf("expected gpu release hook to run")
	}
	if lease != nil {
		t.Fatalf("expected caller arenaLease pointer transferred (set to nil) while live exit is pending")
	}
	// Worker 1 SHM lease AND GPU arbiter must both remain held until exitGate closes.
	if _, acqErr := arenaPool.AcquireArenaLease(1); acqErr == nil {
		t.Fatalf("expected worker 1 SHM arena lease held until retired feature worker exits")
	}
	probeCtx, cancelProbe := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancelProbe()
	if probeErr := arbiter.Acquire(probeCtx); !errors.Is(probeErr, context.DeadlineExceeded) {
		if probeErr == nil {
			arbiter.Release()
		}
		t.Fatalf("expected gpuArbiter fenced during recycle exit wait, got %v", probeErr)
	}

	closeExitGate()
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelWait()
	if err := arbiter.Acquire(waitCtx); err != nil {
		t.Fatalf("expected gpuArbiter released after recycle exit confirmed, got %v", err)
	}
	arbiter.Release()

	// Wait briefly for transferred goroutine to finish CloseAndRelease on worker 1.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		reacq, acqErr := arenaPool.AcquireArenaLease(1)
		if acqErr == nil {
			_ = reacq.CloseAndRelease()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("worker 1 SHM arena lease was not released after retired worker exit confirmation")
}

func TestWorkerDaemonPoolCloseRetainsUnresolvedAndAllowsRetry(t *testing.T) {
	pool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	client := &WorkerDaemonClient{id: 55, testHealthy: true}
	pool.allDaemons = append(pool.allDaemons, client)
	pool.daemons <- client

	var failClose atomic.Bool
	failClose.Store(true)
	pool.closeHook = func(c *WorkerDaemonClient) error {
		if failClose.Load() {
			return errors.New("process exit timed out")
		}
		return c.Close()
	}

	// First Close fails cleanly and MUST keep client tracked in allDaemons for retry.
	if err := pool.Close(); err == nil || !strings.Contains(err.Error(), "process exit timed out") {
		t.Fatalf("expected close error while process exit unconfirmed, got %v", err)
	}
	pool.mu.Lock()
	firstLen := len(pool.allDaemons)
	var firstClient *WorkerDaemonClient
	if firstLen > 0 {
		firstClient = pool.allDaemons[0]
	}
	pool.mu.Unlock()
	if firstLen != 1 || firstClient != client {
		t.Fatalf("expected unresolved client retained in allDaemons, got %d (%v)", firstLen, firstClient)
	}

	// Second Close after termination succeeds cleanly and clears allDaemons without channel panic.
	failClose.Store(false)
	if err := pool.Close(); err != nil {
		t.Fatalf("retry pool.Close failed: %v", err)
	}
	pool.mu.Lock()
	remDaemons := len(pool.allDaemons)
	pool.mu.Unlock()
	if remDaemons != 0 || client.IsHealthy() {
		t.Fatalf("expected client closed and removed on retry Close, healthy=%v len=%d", client.IsHealthy(), remDaemons)
	}
}

func TestWorkerDaemonPoolSpawnCancelRetiresLateClientAndBlocksClose(t *testing.T) {
	pool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	spawnEntered := make(chan struct{}, 1)
	allowSpawnFinish := make(chan struct{})
	lateClient := &WorkerDaemonClient{id: 77, testHealthy: true}
	pool.spawnHook = func(id int) (*WorkerDaemonClient, error) {
		select {
		case spawnEntered <- struct{}{}:
		default:
		}
		<-allowSpawnFinish
		return lateClient, nil
	}

	spawnCtx, cancelSpawn := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancelSpawn()
	if _, err := pool.Acquire(spawnCtx); err == nil {
		t.Fatalf("expected Acquire to fail on spawn timeout")
	}
	select {
	case <-spawnEntered:
	case <-time.After(time.Second):
		t.Fatalf("spawnHook never entered")
	}

	// Start Pool.Close while cancelled constructor is still running; Close must wait for spawn completion.
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- pool.Close()
	}()
	select {
	case err := <-closeDone:
		t.Fatalf("pool.Close finished before pending cancelled spawn finished: %v", err)
	case <-time.After(35 * time.Millisecond):
	}

	// Unblock late constructor; lateClient must be registered, discarded/closed, and Pool.Close completes cleanly.
	close(allowSpawnFinish)
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("pool.Close failed after late spawn finished: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("pool.Close timed out after late spawn finished")
	}
	if !lateClient.closed || len(pool.allDaemons) != 0 || pool.spawningCount != 0 {
		t.Fatalf("late cancelled client not retired cleanly: closed=%v all=%d spawning=%d", lateClient.closed, len(pool.allDaemons), pool.spawningCount)
	}
}

func TestExecuteFeatureLane_CancelledGPUSpawnFencesArbiterUntilLateRetirement(t *testing.T) {
	pool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	t.Cleanup(func() { _ = pool.Close() })
	spawnEntered := make(chan struct{})
	var enterOnce sync.Once
	allowSpawnFinish := make(chan struct{})
	var finishOnce sync.Once
	closeFinish := func() { finishOnce.Do(func() { close(allowSpawnFinish) }) }
	t.Cleanup(closeFinish)
	lateClient := &WorkerDaemonClient{id: 91, testHealthy: true}
	pool.spawnHook = func(_ int) (*WorkerDaemonClient, error) {
		enterOnce.Do(func() { close(spawnEntered) })
		<-allowSpawnFinish
		return lateClient, nil
	}

	arbiter := NewGPUArbiter()
	d := &Dispatcher{
		gpuArbiter:    arbiter,
		gpuDaemonPool: pool,
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	laneErrCh := make(chan error, 1)
	go func() {
		_, err := d.executeFeatureLane(ctx, FeatureLaneGPU, ExtractAllPayload{})
		laneErrCh <- err
	}()

	select {
	case <-spawnEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for GPU spawn constructor to enter")
	}
	cancel()

	select {
	case err := <-laneErrCh:
		if err == nil {
			t.Fatal("expected acquire cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for executeFeatureLane to return after cancel")
	}

	// While late cancelled GPU spawn is still unresolved, arbiter must stay fenced!
	probeCtx, cancelProbe := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancelProbe()
	if probeErr := arbiter.Acquire(probeCtx); !errors.Is(probeErr, context.DeadlineExceeded) {
		if probeErr == nil {
			arbiter.Release()
		}
		t.Fatalf("expected gpuArbiter fenced while cancelled GPU spawn still running, got %v", probeErr)
	}

	closeFinish()
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelWait()
	if err := arbiter.Acquire(waitCtx); err != nil {
		t.Fatalf("expected gpuArbiter released after late GPU spawn retired, got %v", err)
	}
	arbiter.Release()
	if !lateClient.closed || len(pool.allDaemons) != 0 {
		t.Fatalf("expected late GPU client retired & removed, closed=%v len=%d", lateClient.closed, len(pool.allDaemons))
	}
}

func TestWorkerDaemonPoolExitWatcherRestoresCapacityAndFencesUnhealthyGPU(t *testing.T) {
	// 1. Capacity recovery: capacity=1 pool with an unhealthy idle daemon whose Close fails twice
	// wakes a blocked AcquireTracked once the exit watcher confirms exit and removes the daemon.
	pool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	t.Cleanup(func() { _ = pool.Close() })
	unhealthy := &WorkerDaemonClient{id: 1, testHealthy: false} // unhealthy because testHealthy=false & cmd=nil
	replacement := &WorkerDaemonClient{id: 2, testHealthy: true}
	var allowClose atomic.Bool
	pool.closeHook = func(c *WorkerDaemonClient) error {
		if c == unhealthy && !allowClose.Load() {
			return errors.New("still shutting down")
		}
		return c.Close()
	}
	pool.spawnHook = func(_ int) (*WorkerDaemonClient, error) {
		return replacement, nil
	}
	pool.allDaemons = append(pool.allDaemons, unhealthy)

	// Waiter blocks at capacity=1 while watcher recovers slot after allowClose becomes true.
	acqDone := make(chan struct {
		c   *WorkerDaemonClient
		err error
	}, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		c, err := pool.Acquire(ctx)
		acqDone <- struct {
			c   *WorkerDaemonClient
			err error
		}{c: c, err: err}
	}()

	// Trigger pool-owned watcher on unhealthy client.
	if err := pool.Discard(unhealthy); err == nil {
		t.Fatal("expected initial Discard on unhealthy client to fail")
	}
	time.Sleep(40 * time.Millisecond)
	allowClose.Store(true)

	select {
	case res := <-acqDone:
		if res.err != nil || res.c != replacement {
			t.Fatalf("expected waiting Acquire to succeed with replacement once watcher freed capacity, got (%v, %v)", res.c, res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting Acquire timed out after exit watcher freed capacity")
	}
}

func TestScheduleIdleGPUDaemonTrim_TrimsRequeuedClientAfterArbiterRelease(t *testing.T) {
	gpuPool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	t.Cleanup(func() { _ = gpuPool.Close() })

	idleClient := &WorkerDaemonClient{id: 301, testHealthy: true}
	gpuPool.allDaemons = append(gpuPool.allDaemons, idleClient)
	gpuPool.daemons <- idleClient

	arbiter := NewGPUArbiter()
	if err := arbiter.Acquire(t.Context()); err != nil {
		t.Fatalf("failed to hold initial arbiter: %v", err)
	}

	d := &Dispatcher{
		config:        Config{RetainGpuDaemonIdle: false},
		gpuArbiter:    arbiter,
		gpuDaemonPool: gpuPool,
	}
	t.Cleanup(d.stopIdleGPUDaemonTrim)

	// Scheduling trim while arbiter is held must stay pending without timing out or touching idleClient yet.
	d.scheduleIdleGPUDaemonTrim()
	time.Sleep(30 * time.Millisecond)
	if !idleClient.IsHealthy() {
		t.Fatal("idle GPU client should not be trimmed while gpuArbiter is still held")
	}

	// Releasing gpuArbiter lets the pending trim acquire it and reap idleClient cleanly.
	arbiter.Release()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !idleClient.IsHealthy() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected queued trim to discard idle GPU client after arbiter release (healthy=%v)", idleClient.IsHealthy())
}

func TestWorkerDaemonPool_CloseUnblocksInFlightSpawnWithCompletionChannel(t *testing.T) {
	spawnEntered := make(chan struct{})
	var enterOnce sync.Once
	spawnGate := make(chan struct{})
	var closeGateOnce sync.Once
	closeGate := func() { closeGateOnce.Do(func() { close(spawnGate) }) }
	t.Cleanup(closeGate)
	pool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	pool.spawnHook = func(id int) (*WorkerDaemonClient, error) {
		enterOnce.Do(func() { close(spawnEntered) })
		<-spawnGate
		return &WorkerDaemonClient{id: id, testHealthy: true}, nil
	}

	type acqRes struct {
		c    *WorkerDaemonClient
		done <-chan struct{}
		err  error
	}
	resCh := make(chan acqRes, 1)
	go func() {
		c, done, err := pool.AcquireTracked(context.Background())
		resCh <- acqRes{c: c, done: done, err: err}
	}()

	select {
	case <-spawnEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for spawn constructor to enter before Close")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- pool.Close() }()

	select {
	case res := <-resCh:
		if res.err == nil || res.c != nil || res.done == nil {
			t.Fatalf("expected Close to wake blocked spawn with error and spawnDone channel, got (%v, %v, %v)", res.c, res.done, res.err)
		}
		closeGate()
		select {
		case <-res.done:
		case <-time.After(2 * time.Second):
			t.Fatal("expected spawnDone to close once abandoned spawn unwound")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AcquireTracked stayed blocked in spawn after pool.Close()")
	}
	<-closeDone
}

func TestWorkerDaemonPool_CancelledOrClosedBeforeSpawnStartsNoChild(t *testing.T) {
	var spawnCalls atomic.Int32
	pool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	t.Cleanup(func() { _ = pool.Close() })
	pool.spawnHook = func(id int) (*WorkerDaemonClient, error) {
		spawnCalls.Add(1)
		return &WorkerDaemonClient{id: id, testHealthy: true}, nil
	}

	// Pre-cancelled context via AcquireTracked must abort cleanly with nil completion channel and zero child spawns.
	preCancel, cancel := context.WithCancel(t.Context())
	cancel()
	c, done, err := pool.AcquireTracked(preCancel)
	if err == nil || c != nil || done != nil {
		t.Fatalf("expected error with nil client/done for pre-cancelled context, got (%v, %v, %v)", c, done, err)
	}

	// Direct pre-start gate check on reserved slot when context cancels before constructor starts.
	pool.mu.Lock()
	pool.spawningCount++
	closedCh := pool.ensureClosedChLocked()
	pool.mu.Unlock()
	c2, done2, err2 := pool.doSpawnComplex(preCancel, 1, closedCh)
	if err2 == nil || c2 != nil {
		t.Fatalf("expected doSpawnComplex pre-start abort on cancelled ctx, got (%v, %v, %v)", c2, done2, err2)
	}
	if done2 != nil {
		select {
		case <-done2:
		case <-time.After(2 * time.Second):
			t.Fatal("expected pre-start aborted spawnDone to close promptly")
		}
	}

	// Closed pool via AcquireTracked and direct doSpawnComplex must also never start a child.
	if closeErr := pool.Close(); closeErr != nil {
		t.Fatalf("pool.Close failed: %v", closeErr)
	}
	c3, done3, err3 := pool.AcquireTracked(t.Context())
	if err3 == nil || c3 != nil || done3 != nil {
		t.Fatalf("expected closed pool AcquireTracked error, got (%v, %v, %v)", c3, done3, err3)
	}
	if spawnCalls.Load() != 0 {
		t.Fatalf("expected zero child spawn calls when cancelled/closed pre-start, got %d", spawnCalls.Load())
	}

	// Also exercise real NewWorkerDaemonClientWithGate constructor path without spawnHook:
	// startGate must reject pre-cancelled / closed launches before pipes or child process exist.
	tmpDir := t.TempDir()
	scriptName, scriptErr := WorkerDaemonRoleFeatureGPU.scriptName()
	if scriptErr != nil {
		t.Fatalf("scriptName failed: %v", scriptErr)
	}
	if writeErr := os.WriteFile(filepath.Join(tmpDir, scriptName), []byte(""), 0600); writeErr != nil {
		t.Fatalf("write dummy script failed: %v", writeErr)
	}
	gateErr := errors.New("gate rejected launch")
	directClient, directErr := NewWorkerDaemonClientWithGate(1, "nonexistent-python-binary", tmpDir, nil, nil, WorkerDaemonRoleFeatureGPU, func(_ func() error) error {
		return gateErr
	})
	if directClient != nil || !errors.Is(directErr, gateErr) {
		t.Fatalf("expected direct startGate rejection before cmd.Start/pipe alloc, got (%v, %v)", directClient, directErr)
	}

	realPool := NewWorkerDaemonPool(1, "nonexistent-python-binary", tmpDir, nil, nil, WorkerDaemonRoleFeatureGPU)
	t.Cleanup(func() { _ = realPool.Close() })
	realPool.mu.Lock()
	realPool.spawningCount++
	realClosedCh := realPool.ensureClosedChLocked()
	realPool.mu.Unlock()
	rc, rDone, rErr := realPool.doSpawnComplex(preCancel, 1, realClosedCh)
	if rErr == nil || rc != nil || !errors.Is(rErr, context.Canceled) {
		t.Fatalf("expected real constructor abort on pre-cancelled context without invoking cmd.Start, got (%v, %v, %v)", rc, rDone, rErr)
	}
	if rDone != nil {
		select {
		case <-rDone:
		case <-time.After(2 * time.Second):
			t.Fatal("expected real pre-start aborted spawnDone to close promptly")
		}
	}
	if closeErr := realPool.Close(); closeErr != nil {
		t.Fatalf("realPool.Close failed: %v", closeErr)
	}
}

type countClosePipe struct {
	closes atomic.Int32
}

func (p *countClosePipe) Read(_ []byte) (int, error)  { return 0, io.EOF }
func (p *countClosePipe) Write(b []byte) (int, error) { return len(b), nil }
func (p *countClosePipe) Close() error {
	p.closes.Add(1)
	return nil
}

func TestWorkerDaemonClient_CloseClosesAllParentPipesOnce(t *testing.T) {
	inP, outP, errP := &countClosePipe{}, &countClosePipe{}, &countClosePipe{}
	c := &WorkerDaemonClient{
		id:           77,
		stdin:        inP,
		stdoutCloser: outP,
		stderr:       errP,
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
	if inP.closes.Load() != 1 || outP.closes.Load() != 1 || errP.closes.Load() != 1 {
		t.Fatalf("expected all 3 parent pipes closed exactly once, got stdin=%d stdout=%d stderr=%d", inP.closes.Load(), outP.closes.Load(), errP.closes.Load())
	}
}

func TestNewWorkerDaemonClientWithGate_NoOpGateReturnsErrorWithoutPanic(t *testing.T) {
	tmpDir := t.TempDir()
	scriptName, err := WorkerDaemonRoleFeatureGPU.scriptName()
	if err != nil {
		t.Fatalf("scriptName failed: %v", err)
	}
	if writeErr := os.WriteFile(filepath.Join(tmpDir, scriptName), []byte(""), 0600); writeErr != nil {
		t.Fatalf("write dummy script failed: %v", writeErr)
	}
	c, err := NewWorkerDaemonClientWithGate(1, "nonexistent-python-binary", tmpDir, nil, nil, WorkerDaemonRoleFeatureGPU, func(_ func() error) error {
		return nil
	})
	if err == nil || c != nil {
		t.Fatalf("expected initialization error when startGate skips startChild, got (%v, %v)", c, err)
	}
}

func TestReleaseChecked_DisposesGPUClientUnderGrantWhenNotRetainedAfterCancelTrim(t *testing.T) {
	gpuPool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	t.Cleanup(func() { _ = gpuPool.Close() })

	allowClose := make(chan struct{})
	var closeGateOnce sync.Once
	closeGate := func() { closeGateOnce.Do(func() { close(allowClose) }) }
	t.Cleanup(closeGate)
	gpuPool.closeHook = func(c *WorkerDaemonClient) error {
		select {
		case <-allowClose:
			return c.Close()
		default:
			return errors.New("still exiting")
		}
	}

	busyClient := &WorkerDaemonClient{id: 505, testHealthy: true}
	gpuPool.mu.Lock()
	gpuPool.allDaemons = append(gpuPool.allDaemons, busyClient)
	gpuPool.mu.Unlock()

	arbiter := NewGPUArbiter()
	d := &Dispatcher{
		config:        Config{RetainGpuDaemonIdle: false},
		gpuArbiter:    arbiter,
		gpuDaemonPool: gpuPool,
	}
	gpuPool.SetRetainIdle(false)
	t.Cleanup(d.stopIdleGPUDaemonTrim)

	// Run cancelled GPU lane while busyClient is checked out so TrimIdleWithWatchers sees empty queue.
	laneCtx, cancelLane := context.WithCancel(t.Context())
	cancelLane()
	if _, laneErr := d.executeFeatureLane(laneCtx, FeatureLaneGPU, ExtractAllPayload{}); laneErr == nil {
		t.Fatal("expected cancelled executeFeatureLane error")
	}

	// Simulate busyClient returning afterwards under its grant via ReleaseChecked: must discard, never queue idle.
	relErr := gpuPool.ReleaseChecked(busyClient)
	if relErr == nil {
		t.Fatal("expected ReleaseChecked to surface pending close error when RetainGpuDaemonIdle=false")
	}
	if len(gpuPool.daemons) != 0 {
		t.Fatalf("expected disposed client never queued into idle daemons, got %d", len(gpuPool.daemons))
	}
	watch := gpuPool.RetireWatcher(busyClient)
	if watch == nil {
		t.Fatal("expected active RetireWatcher while closeHook is pending")
	}
	closeGate()
	select {
	case <-watch:
	case <-time.After(2 * time.Second):
		t.Fatal("expected RetireWatcher to close after exit confirmation")
	}
	if busyClient.IsHealthy() {
		t.Fatal("expected busyClient closed after retirement")
	}
}

func TestTrimIdle_HonorsActiveRetainPolicy(t *testing.T) {
	gpuPool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	t.Cleanup(func() { _ = gpuPool.Close() })

	idleClient := &WorkerDaemonClient{id: 606, testHealthy: true}
	gpuPool.allDaemons = append(gpuPool.allDaemons, idleClient)
	gpuPool.daemons <- idleClient

	d := &Dispatcher{
		config:        Config{RetainGpuDaemonIdle: true},
		gpuDaemonPool: gpuPool,
	}
	gpuPool.SetRetainIdle(true)

	if trimmed := gpuPool.TrimIdle(0); trimmed != 0 || !idleClient.IsHealthy() || len(gpuPool.daemons) != 1 {
		t.Fatalf("expected TrimIdle(0) to keep idle client while RetainGpuDaemonIdle=true, trimmed=%d healthy=%v idle=%d", trimmed, idleClient.IsHealthy(), len(gpuPool.daemons))
	}

	d.UpdateConfig(Config{RetainGpuDaemonIdle: false})
	if trimmed := gpuPool.TrimIdle(0); trimmed != 1 || idleClient.IsHealthy() || len(gpuPool.daemons) != 0 {
		t.Fatalf("expected TrimIdle(0) to retire idle client once RetainGpuDaemonIdle=false, trimmed=%d healthy=%v idle=%d", trimmed, idleClient.IsHealthy(), len(gpuPool.daemons))
	}
}

func TestExecuteFeatureLane_CancelledGPUCheckoutDisposesUnderArbiterWhenNotRetained(t *testing.T) {
	gpuPool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	t.Cleanup(func() { _ = gpuPool.Close() })

	allowClose := make(chan struct{})
	var closeGateOnce sync.Once
	closeGate := func() { closeGateOnce.Do(func() { close(allowClose) }) }
	t.Cleanup(closeGate)
	gpuPool.closeHook = func(c *WorkerDaemonClient) error {
		select {
		case <-allowClose:
			return c.Close()
		default:
			return errors.New("still exiting")
		}
	}

	// Simulate one busy GPU client (checked out before lane runs) so executeFeatureLane enters Case 3
	// while holding gpuArbiter, then releases back into daemons right as laneCtx is cancelled.
	busyClient := &WorkerDaemonClient{id: 404, testHealthy: true}
	gpuPool.mu.Lock()
	gpuPool.allDaemons = append(gpuPool.allDaemons, busyClient)
	gpuPool.mu.Unlock()

	arbiter := NewGPUArbiter()
	d := &Dispatcher{
		config:        Config{RetainGpuDaemonIdle: false},
		gpuArbiter:    arbiter,
		gpuDaemonPool: gpuPool,
	}
	t.Cleanup(d.stopIdleGPUDaemonTrim)

	laneCtx, cancelLane := context.WithCancel(t.Context())
	defer cancelLane()

	laneErrCh := make(chan error, 1)
	go func() {
		_, err := d.executeFeatureLane(laneCtx, FeatureLaneGPU, ExtractAllPayload{})
		laneErrCh <- err
	}()

	// Wait until executeFeatureLane has acquired gpuArbiter and blocked on AcquireTrackedWithPolicy.
	probeDeadline := time.Now().Add(2 * time.Second)
	acquired := false
	for time.Now().Before(probeDeadline) {
		pCtx, pCancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		err := arbiter.Acquire(pCtx)
		pCancel()
		if errors.Is(err, context.DeadlineExceeded) {
			acquired = true
			break
		}
		if err == nil {
			arbiter.Release()
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !acquired {
		t.Fatal("executeFeatureLane did not acquire gpuArbiter as expected")
	}

	// Competing Demucs waiter queues on gpuArbiter before cancellation & client return.
	demucsAcquired := make(chan struct{})
	go func() {
		waitCtx, waitCancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer waitCancel()
		if err := arbiter.Acquire(waitCtx); err == nil {
			close(demucsAcquired)
			arbiter.Release()
		}
	}()

	// Hold gpuPool.mu while queueing busyClient and cancelling laneCtx so that regardless
	// of whether Case 3 selects <-ctx.Done() (TrimIdleWithWatchers path) or <-daemons
	// (finalizeIdleCheckout path), ctx.Err() is non-nil before p.mu is acquired and
	// busyClient is disposed of synchronously under the active gpuArbiter grant.
	gpuPool.mu.Lock()
	gpuPool.daemons <- busyClient
	cancelLane()
	gpuPool.mu.Unlock()

	select {
	case err := <-laneErrCh:
		if err == nil {
			t.Fatal("expected cancelled executeFeatureLane to return error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("executeFeatureLane did not return after cancellation")
	}

	if len(gpuPool.daemons) != 0 {
		t.Fatalf("expected non-retained GPU daemon discarded under grant, got %d idle", len(gpuPool.daemons))
	}

	// Competing arbiter waiter must remain fenced while busyClient exit confirmation is blocked.
	select {
	case <-demucsAcquired:
		t.Fatal("competing arbiter waiter acquired GPU before cancelled non-retained daemon exited")
	case <-time.After(50 * time.Millisecond):
	}

	closeGate()
	select {
	case <-demucsAcquired:
	case <-time.After(2 * time.Second):
		t.Fatal("competing arbiter waiter stayed blocked after cancelled daemon exit completed")
	}
	if busyClient.IsHealthy() {
		t.Fatal("expected cancelled non-retained GPU client closed after retirement")
	}
}

type gatedClosePipe struct {
	gate chan struct{}
}

func (p *gatedClosePipe) Write(b []byte) (int, error) { return len(b), nil }
func (p *gatedClosePipe) Close() error {
	<-p.gate
	return nil
}

type errClosePipe struct {
	err error
}

func (p *errClosePipe) Read(_ []byte) (int, error) { return 0, io.EOF }
func (p *errClosePipe) Close() error               { return p.err }

type gatedErrClosePipe struct {
	gate chan struct{}
	err  error
}

func (p *gatedErrClosePipe) Read(_ []byte) (int, error) { return 0, io.EOF }
func (p *gatedErrClosePipe) Close() error {
	<-p.gate
	return p.err
}

func TestWorkerDaemonClient_CloseCollectsFailingPipeErrorWhilePeerPipeStalls(t *testing.T) {
	gate := make(chan struct{})
	var closeGateOnce sync.Once
	closeGate := func() { closeGateOnce.Do(func() { close(gate) }) }
	t.Cleanup(closeGate)

	outErr := errors.New("stdout handle close failed")
	c := &WorkerDaemonClient{
		id:           909,
		stdin:        &gatedClosePipe{gate: gate},
		stdoutCloser: &errClosePipe{err: outErr},
		stderr:       &countClosePipe{},
	}

	// Subscribe/run AwaitExit concurrently and cancel deterministically only AFTER the stdout error is queued.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.AwaitExit(ctx)
	}()

	pubDeadline := time.Now().Add(2 * time.Second)
	published := false
	for time.Now().Before(pubDeadline) {
		c.mu.Lock()
		nErrs := len(c.pipeErrs)
		c.mu.Unlock()
		if nErrs > 0 {
			published = true
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !published {
		t.Fatal("timed out waiting for stdout close error to publish before cancellation")
	}
	cancel()

	var err error
	select {
	case err = <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("AwaitExit did not return promptly after cancellation")
	}
	if !errors.Is(err, context.Canceled) || !errors.Is(err, outErr) {
		t.Fatalf("expected cancellation to return both context.Canceled and completed stdout close error even while stdin close stalls, got %v", err)
	}
	// Already claimed errors must not repeat; releasing the stalled peer completes teardown cleanly.
	if rem := c.DrainPipeErrors(); rem != nil {
		t.Fatalf("expected claimed pipe error not repeated, got %v", rem)
	}
	closeGate()
	if sig := c.PipesSignal(); sig != nil {
		select {
		case <-sig:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for peer gated pipe close after unblocking gate")
		}
	}

	// Verify pool Discard completes retirement immediately on confirmed exit while late stdout error
	// arrives AFTER Discard returns and BEFORE stalled stdin pipe ever closes, logged exactly once.
	var loggedMu sync.Mutex
	var loggedMsgs []string
	gate2 := make(chan struct{})
	failGate := make(chan struct{})
	var failGateOnce sync.Once
	var gate2Once sync.Once
	closeFailGate := func() { failGateOnce.Do(func() { close(failGate) }) }
	closeGate2 := func() { gate2Once.Do(func() { close(gate2) }) }
	t.Cleanup(func() {
		closeFailGate()
		closeGate2()
	})
	pool := NewWorkerDaemonPool(1, "", "", nil, func(format string, v ...interface{}) {
		loggedMu.Lock()
		loggedMsgs = append(loggedMsgs, fmt.Sprintf(format, v...))
		loggedMu.Unlock()
	}, WorkerDaemonRoleFeatureGPU)
	t.Cleanup(func() { _ = pool.Close() })

	outErr2 := errors.New("late stdout close failure")
	client2 := &WorkerDaemonClient{
		id:           910,
		exited:       true,
		stdin:        &gatedClosePipe{gate: gate2},
		stdoutCloser: &gatedErrClosePipe{gate: failGate, err: outErr2},
	}
	pool.mu.Lock()
	pool.allDaemons = append(pool.allDaemons, client2)
	pool.mu.Unlock()

	if discErr := pool.Discard(client2); discErr != nil {
		t.Fatalf("expected immediate pool Discard on already-exited client while pipes still closing, got %v", discErr)
	}
	pool.mu.Lock()
	remPool := len(pool.allDaemons)
	pool.mu.Unlock()
	if remPool != 0 {
		t.Fatalf("expected exited client removed from allDaemons immediately without waiting on stalled pipes, got %d", remPool)
	}

	// Unblock ONLY the failing stdout closer while stdin (gate2) stays stalled: drainer must wake
	// immediately and log the sentinel error via logPoolError exactly once!
	closeFailGate()
	logDeadline := time.Now().Add(2 * time.Second)
	lateMatches := 0
	for time.Now().Before(logDeadline) {
		loggedMu.Lock()
		lateMatches = 0
		for _, msg := range loggedMsgs {
			if strings.Contains(msg, outErr2.Error()) {
				lateMatches++
			}
		}
		loggedMu.Unlock()
		if lateMatches > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if lateMatches != 1 {
		loggedMu.Lock()
		msgsCopy := append([]string(nil), loggedMsgs...)
		loggedMu.Unlock()
		t.Fatalf("expected late stdout close failure logged once while peer stdin pipe stalled, got count=%d msgs=%v", lateMatches, msgsCopy)
	}
	// Release peer stdin pipe, wait deterministically for Discard's teardown observer to finish,
	// and assert no duplicate error was emitted.
	closeGate2()
	obsDone := client2.LateTeardownDone()
	if obsDone == nil {
		t.Fatal("expected Discard to attach late teardown observer")
	}
	select {
	case <-obsDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Discard's late teardown observer to finish after releasing gate2")
	}
	loggedMu.Lock()
	finalMatches := 0
	for _, msg := range loggedMsgs {
		if strings.Contains(msg, outErr2.Error()) {
			finalMatches++
		}
	}
	msgsCopy := append([]string(nil), loggedMsgs...)
	loggedMu.Unlock()
	if finalMatches != 1 {
		t.Fatalf("expected final log count for late stdout close failure to remain 1 after completion, got %d (%v)", finalMatches, msgsCopy)
	}
}

func TestAttachLateTeardownLogger_ReattachWaitsForBlockedCallbackAndLogsOnce(t *testing.T) {
	pipeGate := make(chan struct{})
	var pipeGateOnce sync.Once
	closePipeGate := func() { pipeGateOnce.Do(func() { close(pipeGate) }) }

	allowLogReturn := make(chan struct{})
	var allowLogOnce sync.Once
	closeAllowLogReturn := func() { allowLogOnce.Do(func() { close(allowLogReturn) }) }
	t.Cleanup(func() {
		closeAllowLogReturn()
		closePipeGate()
	})

	pipeErr := errors.New("blocked callback teardown pipe failure")
	client := &WorkerDaemonClient{
		id:           911,
		exited:       true,
		stdin:        &gatedClosePipe{gate: pipeGate},
		stdoutCloser: &gatedErrClosePipe{gate: pipeGate, err: pipeErr},
	}
	if retireErr := client.CloseForRetirement(context.Background()); retireErr != nil {
		t.Fatalf("expected immediate CloseForRetirement on exited client with gated pipes, got %v", retireErr)
	}

	var mu sync.Mutex
	var logged []string
	logEntered := make(chan struct{})
	var enteredOnce sync.Once

	firstDone := client.AttachLateTeardownLogger(func(format string, v ...interface{}) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(format, v...))
		mu.Unlock()
		enteredOnce.Do(func() { close(logEntered) })
		<-allowLogReturn
	})

	// Unblock pipes so observer 1 wakes, claims pipeErr, and pauses inside its callback.
	closePipeGate()
	select {
	case <-logEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first teardown logger to enter callback")
	}

	// Reattaching a second observer after teardown completed must NOT close completion channels
	// while observer 1 is still inside logFn, and must NOT duplicate the error log.
	reattachDone := client.AttachLateTeardownLogger(func(format string, v ...interface{}) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(format, v...))
		mu.Unlock()
	})
	latestDone := client.LateTeardownDone()
	if latestDone == nil {
		t.Fatal("expected LateTeardownDone to return non-nil completion channel")
	}

	select {
	case <-firstDone:
		t.Fatal("firstDone closed before first teardown logger callback returned")
	case <-reattachDone:
		t.Fatal("reattachDone closed prematurely while earlier teardown logger callback was blocked")
	case <-latestDone:
		t.Fatal("LateTeardownDone closed prematurely while earlier teardown logger callback was blocked")
	case <-time.After(50 * time.Millisecond):
	}

	closeAllowLogReturn()
	for name, ch := range map[string]<-chan struct{}{
		"firstDone":        firstDone,
		"reattachDone":     reattachDone,
		"LateTeardownDone": latestDone,
	} {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s to close after blocked callback returned", name)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	matches := 0
	for _, msg := range logged {
		if strings.Contains(msg, pipeErr.Error()) {
			matches++
		}
	}
	if matches != 1 || len(logged) != 1 {
		t.Fatalf("expected exactly 1 logged teardown error across attached observers, got matches=%d total=%d msgs=%v", matches, len(logged), logged)
	}
}

func TestExecuteFeatureLane_GPUNonRetainedUsesDeferRetireAndFencesGrantAndSHM(t *testing.T) {
	exitGate := make(chan struct{})
	var exitOnce sync.Once
	closeExit := func() { exitOnce.Do(func() { close(exitGate) }) }
	t.Cleanup(closeExit)

	var exitConfirmed atomic.Bool
	arbiter := NewGPUArbiter()
	gpuPool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleFeatureGPU)
	gpuClient := &WorkerDaemonClient{id: 501, testHealthy: true}
	gpuPool.mu.Lock()
	gpuPool.allDaemons = append(gpuPool.allDaemons, gpuClient)
	gpuPool.daemons <- gpuClient
	gpuPool.mu.Unlock()
	gpuPool.closeHook = func(c *WorkerDaemonClient) error {
		if !exitConfirmed.Load() {
			return errors.New("still exiting")
		}
		c.closed = true
		return c.Close()
	}
	t.Cleanup(func() {
		exitConfirmed.Store(true)
		closeExit()
		_ = gpuPool.Close()
	})

	cpuPool := NewWorkerDaemonPool(1, "", "", nil, nil, WorkerDaemonRoleCPU)
	cpuClient := &WorkerDaemonClient{id: 502, testHealthy: true}
	cpuPool.mu.Lock()
	cpuPool.allDaemons = append(cpuPool.allDaemons, cpuClient)
	cpuPool.daemons <- cpuClient
	cpuPool.mu.Unlock()
	t.Cleanup(func() { _ = cpuPool.Close() })

	arenaPool := NewShmArenaPool(false)
	t.Cleanup(func() { _ = arenaPool.Close() })
	lease, err := arenaPool.AcquireArenaLease(1)
	if err != nil {
		t.Fatalf("AcquireArenaLease failed: %v", err)
	}

	cleanupCalled := false
	d := &Dispatcher{
		config:        Config{RetainGpuDaemonIdle: false},
		gpuArbiter:    arbiter,
		gpuDaemonPool: gpuPool,
		cpuDaemonPool: cpuPool,
		featureOps: &featureLaneOps{
			extractCPU: func(_ context.Context, _ *WorkerDaemonClient, _ ExtractAllPayload) (*DaemonResponse, error) {
				return &DaemonResponse{Status: "success", Librosa: map[string]interface{}{"ok": true}, Essentia: map[string]interface{}{"ok": true}}, nil
			},
			extractGPU: func(_ context.Context, _ *WorkerDaemonClient, _ ExtractAllPayload) (*DaemonResponse, error) {
				return &DaemonResponse{Status: "success", Tensor: map[string]interface{}{"ok": true}}, nil
			},
			cleanupGPU: func(_ context.Context, _ *WorkerDaemonClient) error {
				cleanupCalled = true
				return errors.New("cleanup must not run for non-retained GPU daemon")
			},
		},
	}

	feats, featErr := d.executeFeaturesStage(44100, "hash-defer", nil, &lease, StorageModeSHM, TaskPayload{}, Config{RetainGpuDaemonIdle: false})
	if cleanupCalled {
		t.Fatal("CleanupGPU was called even though RetainGpuDaemonIdle=false")
	}
	if feats != nil && featErr != nil {
		t.Fatalf("unexpected mixed result: (%+v, %v)", feats, featErr)
	}
	// Because exitGate is open, DeferRetire sees pending exit -> ArenaLease transferred (lease == nil)
	// and BOTH SHM worker 1 lease AND gpuArbiter stay fenced until exitConfirmed becomes true.
	if lease != nil {
		t.Fatal("expected parent ArenaLease pointer transferred on pending DeferRetire exit")
	}
	if _, acqErr := arenaPool.AcquireArenaLease(1); acqErr == nil {
		t.Fatal("expected worker 1 SHM arena lease held while DeferRetire exit pending")
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancelProbe()
	if err := arbiter.Acquire(probeCtx); !errors.Is(err, context.DeadlineExceeded) {
		if err == nil {
			arbiter.Release()
		}
		t.Fatalf("expected gpuArbiter to stay fenced while DeferRetire process exit pending, got %v", err)
	}

	// Unblock child exit -> both gpuArbiter AND SHM arena release cleanly.
	exitConfirmed.Store(true)
	closeExit()
	deadline := time.Now().Add(2 * time.Second)
	arbiterFreed := false
	for time.Now().Before(deadline) {
		ctxTry, cancelTry := context.WithTimeout(context.Background(), 15*time.Millisecond)
		if err := arbiter.Acquire(ctxTry); err == nil {
			arbiter.Release()
			cancelTry()
			arbiterFreed = true
			break
		}
		cancelTry()
		time.Sleep(10 * time.Millisecond)
	}
	if !arbiterFreed {
		t.Fatal("timed out waiting for gpuArbiter release after DeferRetire exit completed")
	}
	arenaReacquired := false
	for time.Now().Before(deadline) {
		reLease, acqErr := arenaPool.AcquireArenaLease(1)
		if acqErr == nil && reLease != nil {
			_ = reLease.CloseAndRelease()
			arenaReacquired = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !arenaReacquired {
		t.Fatal("timed out waiting for SHM arena release after DeferRetire exit completed")
	}
}

func TestDiscard_PrequeuedTeardownErrorDoesNotBlockCallerWhileCallbackStalls(t *testing.T) {
	allowLogReturn := make(chan struct{})
	var allowOnce sync.Once
	closeAllow := func() { allowOnce.Do(func() { close(allowLogReturn) }) }
	t.Cleanup(closeAllow)

	var mu sync.Mutex
	var logged []string
	logEntered := make(chan struct{})
	var enteredOnce sync.Once

	pool := NewWorkerDaemonPool(1, "", "", nil, func(format string, v ...interface{}) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(format, v...))
		mu.Unlock()
		enteredOnce.Do(func() { close(logEntered) })
		<-allowLogReturn
	}, WorkerDaemonRoleFeatureGPU)
	t.Cleanup(func() { _ = pool.Close() })

	preErr := errors.New("prequeued pipe close failure")
	client := &WorkerDaemonClient{
		id:             912,
		exited:         true,
		pipesAllClosed: true,
		pipeErrs:       []error{preErr},
	}
	// Simulate teardown error queued immediately before attachLatePipeDrainer runs during Discard.
	pool.closeHook = func(_ *WorkerDaemonClient) error { return nil }
	pool.mu.Lock()
	pool.allDaemons = append(pool.allDaemons, client)
	pool.mu.Unlock()

	discardDone := make(chan error, 1)
	go func() {
		discardDone <- pool.Discard(client)
	}()

	// Discard MUST complete immediately without waiting for the blocked logFn callback!
	select {
	case err := <-discardDone:
		if err != nil {
			t.Fatalf("expected Discard on already-exited client to return nil, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("pool.Discard blocked on slow/stalled logFn callback for prequeued teardown error")
	}

	pool.mu.Lock()
	rem := len(pool.allDaemons)
	pool.mu.Unlock()
	if rem != 0 {
		t.Fatalf("expected client removed from allDaemons immediately despite blocked callback, got %d", rem)
	}

	select {
	case <-logEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background logger callback to enter for prequeued error")
	}

	// The pool routes teardown diagnostics through its bounded async queue, so the client-side
	// observer completes without waiting for the stalled sink (retirement independent of logging).
	obsDone := client.LateTeardownDone()
	if obsDone == nil {
		t.Fatal("expected non-nil teardown completion channel")
	}
	select {
	case <-obsDone:
	case <-time.After(2 * time.Second):
		t.Fatal("teardown observer must not wait on the stalled pool logger sink")
	}

	closeAllow()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pool.logMu.Lock()
		idle := !pool.logRunning && len(pool.logOrder) == 0
		pool.logMu.Unlock()
		if idle {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	matches := 0
	for _, msg := range logged {
		if strings.Contains(msg, preErr.Error()) {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("expected prequeued error logged exactly once, got %d in %v", matches, logged)
	}
}

func TestExitWatcher_StalledLoggerDoesNotBlockRetirementConfirmation(t *testing.T) {
	allowLogReturn := make(chan struct{})
	var allowOnce sync.Once
	closeAllow := func() { allowOnce.Do(func() { close(allowLogReturn) }) }
	t.Cleanup(closeAllow)

	var logMu sync.Mutex
	var logged []string
	logEntered := make(chan struct{})
	var enteredOnce sync.Once

	pool := NewWorkerDaemonPool(1, "", "", nil, func(format string, v ...interface{}) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(format, v...))
		logMu.Unlock()
		enteredOnce.Do(func() { close(logEntered) })
		<-allowLogReturn
	}, WorkerDaemonRoleFeatureGPU)

	client := &WorkerDaemonClient{id: 913, testHealthy: true}
	var exitConfirmed atomic.Bool
	pool.closeHook = func(c *WorkerDaemonClient) error {
		if !exitConfirmed.Load() {
			return errors.New("still alive before kill completes")
		}
		c.closed = true
		return nil
	}
	t.Cleanup(func() {
		exitConfirmed.Store(true)
		closeAllow()
		_ = pool.Close()
	})

	pool.mu.Lock()
	pool.allDaemons = append(pool.allDaemons, client)
	pool.mu.Unlock()

	watchCh, err := pool.DeferRetire(client)
	if err == nil {
		t.Fatalf("expected initial live retirement error, got nil")
	}
	if watchCh == nil {
		t.Fatal("expected non-nil RetireWatcher channel")
	}

	// Wait for ensureExitWatcherLocked retry attempt to enter the stalled logger callback.
	select {
	case <-logEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for retry diagnostic to enter blocked logger")
	}

	// Now allow child exit while pool.logger is STILL blocked!
	exitConfirmed.Store(true)
	// Override closeHook isn't needed: exitConfirmed is atomic! Wait: p.clientMayStillBeRunning(client, nil) == false!
	select {
	case <-watchCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RetireWatcher blocked on stalled logger instead of confirming exit promptly")
	}

	pool.mu.Lock()
	rem := len(pool.allDaemons)
	pool.mu.Unlock()
	if rem != 0 {
		t.Fatalf("expected retired client removed from allDaemons while logger blocked, got %d", rem)
	}

	closeAllow()
	// Drain async logger worker before asserting final log contents.
	logDrainDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(logDrainDeadline) {
		pool.logMu.Lock()
		running := pool.logRunning
		pending := len(pool.logOrder)
		pool.logMu.Unlock()
		if !running && pending == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	logMu.Lock()
	defer logMu.Unlock()
	if len(logged) == 0 {
		t.Fatal("expected retry diagnostic log entry to be recorded")
	}
}

func TestPool_BoundedAsyncLoggerCoalescesAndDoesNotBlockTrimIdleWatchers(t *testing.T) {
	allowLogReturn := make(chan struct{})
	var allowOnce sync.Once
	closeAllow := func() { allowOnce.Do(func() { close(allowLogReturn) }) }
	t.Cleanup(closeAllow)

	var logMu sync.Mutex
	var logged []string
	logEntered := make(chan struct{})
	var enteredOnce sync.Once

	pool := NewWorkerDaemonPool(1, "", "", nil, func(format string, v ...interface{}) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(format, v...))
		logMu.Unlock()
		enteredOnce.Do(func() { close(logEntered) })
		<-allowLogReturn
	}, WorkerDaemonRoleFeatureGPU)
	var exitConfirmed atomic.Bool
	pool.closeHook = func(c *WorkerDaemonClient) error {
		if !exitConfirmed.Load() {
			return errors.New("still shutting down")
		}
		return nil
	}
	t.Cleanup(func() {
		closeAllow()
		exitConfirmed.Store(true)
		_ = pool.Close()
	})

	client := &WorkerDaemonClient{id: 914, testHealthy: true}
	pool.mu.Lock()
	pool.allDaemons = append(pool.allDaemons, client)
	pool.daemons <- client
	pool.mu.Unlock()

	trimDone := make(chan int, 1)
	var watchers []<-chan struct{}
	go func() {
		trimmed, ws := pool.TrimIdleWithWatchers(0)
		watchers = ws
		trimDone <- trimmed
	}()

	// TrimIdleWithWatchers must return its watcher promptly even if logger stalls immediately!
	select {
	case trimmed := <-trimDone:
		if trimmed != 0 || len(watchers) != 1 {
			t.Fatalf("expected trimmed=0 with 1 watcher, got trimmed=%d watchers=%d", trimmed, len(watchers))
		}
	case <-time.After(1 * time.Second):
		t.Fatal("TrimIdleWithWatchers blocked on stalled logger")
	}

	select {
	case <-logEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async logger callback to enter")
	}

	// Enqueue 50 repeated diagnostics for the same daemon while the single async logger stalls;
	// coalescing must collapse them to one pending entry without spawning extra workers.
	asyncLog := pool.asyncLogger("coalesce-test")
	for i := 0; i < 50; i++ {
		asyncLog("[DaemonPool] retry #%d", i)
	}
	const coalesceKey = "coalesce-test|[DaemonPool] retry #%d"
	pool.logMu.Lock()
	pendingCount := 0
	for _, k := range pool.logOrder {
		if k == coalesceKey {
			pendingCount++
		}
	}
	latestMsg := ""
	if ent, ok := pool.logLatest[coalesceKey]; ok {
		latestMsg = fmt.Sprintf(ent.format, ent.args...)
	}
	pool.logMu.Unlock()
	if pendingCount != 1 || latestMsg != "[DaemonPool] retry #49" {
		t.Fatalf("expected 50 repeated diagnostics coalesced into 1 latest entry, got pending=%d latest=%q", pendingCount, latestMsg)
	}

	closeAllow()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pool.logMu.Lock()
		done := !pool.logRunning && len(pool.logOrder) == 0
		pool.logMu.Unlock()
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	logMu.Lock()
	sawCoalescedLatest := false
	for _, msg := range logged {
		if msg == "[DaemonPool] retry #49" {
			sawCoalescedLatest = true
		}
	}
	logMu.Unlock()
	if !sawCoalescedLatest {
		t.Fatalf("expected coalesced retry #49 to flush after unblocking logger, logged=%v", logged)
	}
}
