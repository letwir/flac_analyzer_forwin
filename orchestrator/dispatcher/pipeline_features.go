// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// Mor: (DemucsSR, TrackHash, Stems) -> (LibrosaJSON, TensorJSON, EssentiaJSON, Error)
package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// FeatureOutputs encapsulates serialized JSON outputs from worker daemon.
// Its shape remains compatible with the legacy extract_all response.
type FeatureOutputs struct {
	LibOut    string
	TensorOut string
	EssOut    string
}

type featureLaneFunc func(context.Context) (*DaemonResponse, error)

type featureLaneResult struct {
	lane FeatureLane
	resp *DaemonResponse
	err  error
}

// runFeatureLanes starts exactly one CPU lane and one GPU lane. It waits for
// both functions to return so each owns and releases its client before an
// error reaches the caller.
func runFeatureLanes(ctx context.Context, cpu, gpu featureLaneFunc) (*DaemonResponse, *DaemonResponse, error) {
	if ctx == nil || cpu == nil || gpu == nil {
		return nil, nil, fmt.Errorf("feature lanes require a context and both lane functions")
	}

	laneCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	results := make(chan featureLaneResult, 2)
	var wg sync.WaitGroup
	wg.Go(func() {
		resp, err := cpu(laneCtx)
		results <- featureLaneResult{lane: FeatureLaneCPU, resp: resp, err: err}
	})
	wg.Go(func() {
		resp, err := gpu(laneCtx)
		results <- featureLaneResult{lane: FeatureLaneGPU, resp: resp, err: err}
	})

	var cpuResp, gpuResp *DaemonResponse
	var cpuErr, gpuErr error
	for range 2 {
		result := <-results
		switch result.lane {
		case FeatureLaneCPU:
			cpuResp, cpuErr = result.resp, result.err
		case FeatureLaneGPU:
			gpuResp, gpuErr = result.resp, result.err
		}
		if result.err != nil {
			cancel(result.err)
		}
	}
	wg.Wait()

	if cpuErr != nil || gpuErr != nil {
		return nil, nil, errors.Join(
			wrapFeatureLaneError(FeatureLaneCPU, cpuErr),
			wrapFeatureLaneError(FeatureLaneGPU, gpuErr),
		)
	}
	return cpuResp, gpuResp, nil
}

func wrapFeatureLaneError(lane FeatureLane, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s feature lane: %w", lane, err)
}

type featureLaneOps struct {
	extractCPU     func(context.Context, *WorkerDaemonClient, ExtractAllPayload) (*DaemonResponse, error)
	extractGPU     func(context.Context, *WorkerDaemonClient, ExtractAllPayload) (*DaemonResponse, error)
	cleanupGPU     func(context.Context, *WorkerDaemonClient) error
	discardClient  func(*WorkerDaemonPool, *WorkerDaemonClient) error
	deferRetire    func(*WorkerDaemonPool, *WorkerDaemonClient) (<-chan struct{}, error)
	releaseClient  func(*WorkerDaemonPool, *WorkerDaemonClient) error
	retireWatcher  func(*WorkerDaemonPool, *WorkerDaemonClient) <-chan struct{}
	hasLiveProcess func(*WorkerDaemonClient) bool
}

type liveRetirementTracker struct {
	mu      sync.Mutex
	waiters []<-chan struct{}
}

func (r *liveRetirementTracker) add(done <-chan struct{}) {
	if r == nil || done == nil {
		return
	}
	r.mu.Lock()
	r.waiters = append(r.waiters, done)
	r.mu.Unlock()
}

func (r *liveRetirementTracker) snapshot() []<-chan struct{} {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]<-chan struct{}(nil), r.waiters...)
}

type liveRetirementCtxKey struct{}

func withLiveRetirementTracker(ctx context.Context, tracker *liveRetirementTracker) context.Context {
	if tracker == nil {
		return ctx
	}
	return context.WithValue(ctx, liveRetirementCtxKey{}, tracker)
}

func retirementTrackerFromContext(ctx context.Context) *liveRetirementTracker {
	if ctx == nil {
		return nil
	}
	if t, ok := ctx.Value(liveRetirementCtxKey{}).(*liveRetirementTracker); ok {
		return t
	}
	return nil
}

// transferOrCloseArenaLease closes and releases arenaLease synchronously if no timed-out
// worker exit is pending; otherwise it transfers CloseAndRelease ownership to await
// all live worker exit signals before closing SHM mappings and releasing the pool lease.
func transferOrCloseArenaLease(arenaLease **ArenaLease, tracker *liveRetirementTracker, logFn func(string, ...interface{})) error {
	if arenaLease == nil || *arenaLease == nil {
		return nil
	}
	lease := *arenaLease
	waiters := tracker.snapshot()
	if len(waiters) == 0 {
		*arenaLease = nil
		return lease.CloseAndRelease()
	}
	*arenaLease = nil
	workerID := -1
	if lease.Set != nil {
		workerID = lease.Set.WorkerID
	}
	go func(l *ArenaLease, doneChs []<-chan struct{}, wid int) {
		for _, ch := range doneChs {
			<-ch
		}
		if closeErr := l.CloseAndRelease(); closeErr != nil && logFn != nil {
			logFn("[W-%d] Async SHM arena CloseAndRelease failed after live worker exit: %v", wid, closeErr)
		}
	}(lease, waiters, workerID)
	return nil
}

func (d *Dispatcher) shouldRetainGpuDaemonIdle() bool {
	if d == nil {
		return false
	}
	if d.retainGpuDaemonIdleFn != nil {
		return d.retainGpuDaemonIdleFn()
	}
	return d.GetConfig().RetainGpuDaemonIdle
}

func (d *Dispatcher) resolvedFeatureLaneOps() featureLaneOps {
	ops := featureLaneOps{
		extractCPU: func(ctx context.Context, c *WorkerDaemonClient, p ExtractAllPayload) (*DaemonResponse, error) {
			return c.ExtractCPU(ctx, p)
		},
		extractGPU: func(ctx context.Context, c *WorkerDaemonClient, p ExtractAllPayload) (*DaemonResponse, error) {
			return c.ExtractGPU(ctx, p)
		},
		cleanupGPU: func(ctx context.Context, c *WorkerDaemonClient) error {
			return c.CleanupGPU(ctx)
		},
		discardClient: func(pool *WorkerDaemonPool, c *WorkerDaemonClient) error {
			return pool.Discard(c)
		},
		deferRetire: func(pool *WorkerDaemonPool, c *WorkerDaemonClient) (<-chan struct{}, error) {
			return pool.DeferRetire(c)
		},
		releaseClient: func(pool *WorkerDaemonPool, c *WorkerDaemonClient) error {
			return pool.ReleaseChecked(c)
		},
		retireWatcher: func(pool *WorkerDaemonPool, c *WorkerDaemonClient) <-chan struct{} {
			return pool.RetireWatcher(c)
		},
		hasLiveProcess: func(c *WorkerDaemonClient) bool {
			return c.HasLiveProcess()
		},
	}
	if d == nil || d.featureOps == nil {
		return ops
	}
	if d.featureOps.extractCPU != nil {
		ops.extractCPU = d.featureOps.extractCPU
	}
	if d.featureOps.extractGPU != nil {
		ops.extractGPU = d.featureOps.extractGPU
	}
	if d.featureOps.cleanupGPU != nil {
		ops.cleanupGPU = d.featureOps.cleanupGPU
	}
	if d.featureOps.discardClient != nil {
		ops.discardClient = d.featureOps.discardClient
	}
	if d.featureOps.releaseClient != nil {
		ops.releaseClient = d.featureOps.releaseClient
	}
	if d.featureOps.retireWatcher != nil {
		ops.retireWatcher = d.featureOps.retireWatcher
	}
	if d.featureOps.hasLiveProcess != nil {
		ops.hasLiveProcess = d.featureOps.hasLiveProcess
	}
	if d.featureOps.deferRetire != nil {
		ops.deferRetire = d.featureOps.deferRetire
	} else if d.featureOps.discardClient != nil || d.featureOps.retireWatcher != nil {
		discardFn := ops.discardClient
		watcherFn := ops.retireWatcher
		ops.deferRetire = func(pool *WorkerDaemonPool, c *WorkerDaemonClient) (<-chan struct{}, error) {
			err := discardFn(pool, c)
			return watcherFn(pool, c), err
		}
	}
	return ops
}

func (d *Dispatcher) currentTrimContext() context.Context {
	d.gpuTrimMu.Lock()
	defer d.gpuTrimMu.Unlock()
	if d.gpuTrimStopping {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	if d.trimCtx == nil {
		d.trimCtx, d.cancelTrimCtx = context.WithCancel(context.Background())
	}
	return d.trimCtx
}

func (d *Dispatcher) ensureGpuTrimCondLocked() *sync.Cond {
	if d.gpuTrimCond == nil {
		d.gpuTrimCond = sync.NewCond(&d.gpuTrimMu)
	}
	return d.gpuTrimCond
}

func (d *Dispatcher) finishGpuTrimLoopLocked() {
	d.gpuTrimPending = false
	d.gpuTrimRunning = false
	d.ensureGpuTrimCondLocked().Broadcast()
}

func (d *Dispatcher) stopIdleGPUDaemonTrim() {
	if d == nil {
		return
	}
	d.gpuTrimMu.Lock()
	d.gpuTrimStopping = true
	d.gpuTrimPending = false
	d.ensureGpuTrimCondLocked().Broadcast()
	cancel := d.cancelTrimCtx
	d.gpuTrimMu.Unlock()
	if cancel != nil {
		cancel()
	}
	d.gpuTrimWg.Wait()
}

func (d *Dispatcher) scheduleIdleGPUDaemonTrim() {
	d.enqueueIdleGPUDaemonTrim(true)
}

func (d *Dispatcher) trimIdleGPUDaemons() {
	d.enqueueIdleGPUDaemonTrim(false)
}

func (d *Dispatcher) enqueueIdleGPUDaemonTrim(async bool) {
	if d == nil || d.gpuDaemonPool == nil || d.gpuArbiter == nil || d.shouldRetainGpuDaemonIdle() {
		return
	}
	d.gpuTrimMu.Lock()
	if d.gpuTrimStopping || d.gpuDaemonPool.IsClosed() || d.shouldRetainGpuDaemonIdle() {
		d.gpuTrimPending = false
		d.gpuTrimMu.Unlock()
		return
	}
	d.gpuTrimPending = true
	if d.gpuTrimRunning {
		if !async {
			// Synchronous trim must wait until the active trim pass completes any newly pending work.
			cond := d.ensureGpuTrimCondLocked()
			for d.gpuTrimRunning && !d.gpuTrimStopping {
				cond.Wait()
			}
		}
		d.gpuTrimMu.Unlock()
		return
	}
	d.gpuTrimRunning = true
	d.gpuTrimWg.Add(1)
	d.gpuTrimMu.Unlock()

	if async {
		go d.runIdleGPUDaemonTrimLoop()
		return
	}
	d.runIdleGPUDaemonTrimLoop()
}

func (d *Dispatcher) runIdleGPUDaemonTrimLoop() {
	defer d.gpuTrimWg.Done()
	for {
		d.gpuTrimMu.Lock()
		if d.gpuTrimStopping || !d.gpuTrimPending || d.shouldRetainGpuDaemonIdle() || d.gpuDaemonPool.IsClosed() {
			d.finishGpuTrimLoopLocked()
			d.gpuTrimMu.Unlock()
			return
		}
		d.gpuTrimPending = false
		d.gpuTrimMu.Unlock()

		trimCtx := d.currentTrimContext()
		if err := d.gpuArbiter.Acquire(trimCtx); err != nil {
			// Shutdown took ownership; gpuDaemonPool.Close will reap remaining idle daemons.
			d.gpuTrimMu.Lock()
			d.finishGpuTrimLoopLocked()
			d.gpuTrimMu.Unlock()
			return
		}
		d.gpuTrimMu.Lock()
		stopping := d.gpuTrimStopping
		if stopping {
			d.finishGpuTrimLoopLocked()
		}
		d.gpuTrimMu.Unlock()
		if stopping {
			d.gpuArbiter.Release()
			return
		}
		// Re-check retention policy after acquiring GPU arbiter in case hot-reload enabled retention while queued.
		var watchers []<-chan struct{}
		if !d.shouldRetainGpuDaemonIdle() && !d.gpuDaemonPool.IsClosed() {
			_, watchers = d.gpuDaemonPool.TrimIdleWithWatchers(0)
		}
		if len(watchers) == 0 {
			if !d.shouldRetainGpuDaemonIdle() {
				d.releaseAllRetainedGPUResidency()
			}
			d.gpuArbiter.Release()
			continue
		}
		waitDone := make(chan struct{})
		go func(ws []<-chan struct{}) {
			for _, w := range ws {
				<-w
			}
			close(waitDone)
		}(watchers)
		select {
		case <-waitDone:
			d.releaseAllRetainedGPUResidency()
			d.gpuArbiter.Release()
		case <-trimCtx.Done():
			// Shutdown cancellation: keep gpuArbiter fenced and residency leased until retired workers exit,
			// but return so Stop() can proceed to bounded gpuDaemonPool.Close().
			go func() {
				<-waitDone
				d.releaseAllRetainedGPUResidency()
				d.gpuArbiter.Release()
			}()
			d.gpuTrimMu.Lock()
			d.finishGpuTrimLoopLocked()
			d.gpuTrimMu.Unlock()
			return
		}
	}
}

func (d *Dispatcher) executeFeatureLane(
	ctx context.Context,
	lane FeatureLane,
	payload ExtractAllPayload,
) (_ *DaemonResponse, retErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s feature lane cancelled before acquire: %w", lane, err)
	}
	ctxAcquire, cancelAcquire := context.WithTimeout(ctx, 120*time.Second)
	defer cancelAcquire()

	pool := d.cpuDaemonPool
	arbiterReleased := false
	if lane == FeatureLaneGPU {
		pool = d.gpuDaemonPool
		if d.gpuArbiter == nil {
			return nil, fmt.Errorf("GPU arbiter is unavailable")
		}
		if err := d.gpuArbiter.Acquire(ctxAcquire); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, fmt.Errorf("%w: failed to acquire GPU arbiter: %w", ErrRetryableTimeout, err)
			}
			return nil, fmt.Errorf("acquire GPU arbiter: %w", err)
		}
		defer func() {
			if !arbiterReleased {
				d.gpuArbiter.Release()
			}
		}()
	}
	if pool == nil {
		return nil, fmt.Errorf("%s worker daemon pool is unavailable", lane)
	}
	client, spawnDone, err := pool.AcquireTracked(ctxAcquire)
	if err != nil {
		tracker := retirementTrackerFromContext(ctx)
		var gpuWaiters []<-chan struct{}
		if spawnDone != nil {
			// Pending/unresolved spawn or unhealthy retirement still running; fence SHM/GPU until exit completes.
			if tracker != nil {
				tracker.add(spawnDone)
			}
			if lane == FeatureLaneGPU {
				gpuWaiters = append(gpuWaiters, spawnDone)
			}
		}
		if lane == FeatureLaneGPU && !d.shouldRetainGpuDaemonIdle() {
			// Dispose of any idle GPU daemon synchronously under the current GPU arbiter grant
			// before returning so no competing Demucs task can start while VRAM is still occupied.
			_, trimWatchers := pool.TrimIdleWithWatchers(0)
			for _, w := range trimWatchers {
				if tracker != nil {
					tracker.add(w)
				}
				gpuWaiters = append(gpuWaiters, w)
			}
		}
		if lane == FeatureLaneGPU && len(gpuWaiters) > 0 {
			arbiterReleased = true
			go func(ws []<-chan struct{}) {
				for _, w := range ws {
					<-w
				}
				d.releaseAllRetainedGPUResidency()
				d.gpuArbiter.Release()
			}(gpuWaiters)
		} else if lane == FeatureLaneGPU && !d.shouldRetainGpuDaemonIdle() {
			d.releaseAllRetainedGPUResidency()
		}
		return nil, fmt.Errorf("acquire %s worker daemon: %w", lane, err)
	}
	if client == nil {
		return nil, fmt.Errorf("acquired nil %s worker daemon", lane)
	}

	ops := d.resolvedFeatureLaneOps()
	retireClient := false
	fenceRetirementWatcher := func(watchCh <-chan struct{}) {
		if watchCh == nil {
			return
		}
		if tracker := retirementTrackerFromContext(ctx); tracker != nil {
			tracker.add(watchCh)
		}
		if d != nil {
			d.trackRetiringClient(client)
		}
		releaseArbiter := lane == FeatureLaneGPU
		if releaseArbiter {
			arbiterReleased = true
		}
		go func(c *WorkerDaemonClient, ch <-chan struct{}, relArb bool) {
			<-ch
			if d != nil {
				d.untrackRetiringClient(c)
				if lane == FeatureLaneGPU {
					d.releaseRetainedGPUResidency(c)
				}
			}
			if relArb {
				d.gpuArbiter.Release()
			}
		}(client, watchCh, releaseArbiter)
	}
	defer func() {
		// Read retention decision once on exit to keep pool disposition and ledger consistent.
		retainNow := d.shouldRetainGpuDaemonIdle()
		if lane == FeatureLaneGPU && !retainNow {
			retireClient = true
		}
		if !retireClient && lane == FeatureLaneGPU {
			if resErr := d.reserveRetainedGPUResidency(client); resErr != nil {
				retireClient = true
				retErr = errors.Join(retErr, fmt.Errorf("reserve retained GPU residency: %w", resErr))
			}
		}
		if retireClient {
			watchCh, retireErr := ops.deferRetire(pool, client)
			if retireErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("retire %s worker daemon: %w", lane, retireErr))
			}
			if lane == FeatureLaneGPU && watchCh == nil {
				d.releaseRetainedGPUResidency(client)
			}
			fenceRetirementWatcher(watchCh)
			return
		}
		if retireErr := ops.releaseClient(pool, client); retireErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("retire %s worker daemon: %w", lane, retireErr))
			// Pool watcher lookup is atomic with membership check: non-nil means exit & removal are pending,
			// while nil means the client has already been confirmed exited and removed from allDaemons.
			watchCh := ops.retireWatcher(pool, client)
			if lane == FeatureLaneGPU && watchCh == nil {
				d.releaseRetainedGPUResidency(client)
			}
			fenceRetirementWatcher(watchCh)
			return
		}
	}()

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s feature lane cancelled after acquire: %w", lane, err)
	}

	switch lane {
	case FeatureLaneCPU:
		resp, err := ops.extractCPU(ctx, client, payload)
		if err != nil {
			retireClient = true
			return nil, err
		}
		return resp, nil
	case FeatureLaneGPU:
		resp, extractErr := ops.extractGPU(ctx, client, payload)
		if extractErr != nil {
			retireClient = true
			return nil, extractErr
		}
		if !d.shouldRetainGpuDaemonIdle() {
			// Skip CleanupGPU for non-retained GPU daemons: retire the checked-out client via
			// DeferRetire while keeping gpuArbiter and any SHM lease fenced until exit completes.
			retireClient = true
			return resp, nil
		}
		// Run CleanupGPU on the exact retained active daemon while holding gpuArbiter
		// so PyTorch CUDA allocator cache is freed cleanly; retire on cleanup error.
		cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 30*time.Second)
		cleanErr := ops.cleanupGPU(cleanupCtx, client)
		cleanupCancel()
		if cleanErr != nil {
			retireClient = true
			return nil, fmt.Errorf("GPU cleanup failed: %w", cleanErr)
		}
		return resp, nil
	default:
		return nil, fmt.Errorf("unknown feature lane %q", lane)
	}
}

func joinFeatureLaneResponses(cpuResp, gpuResp *DaemonResponse) (*FeatureOutputs, error) {
	if cpuResp == nil || gpuResp == nil {
		return nil, fmt.Errorf("feature lane response missing")
	}
	libOut, err := json.Marshal(cpuResp.Librosa)
	if err != nil {
		return nil, fmt.Errorf("marshal Librosa features: %w", err)
	}
	tensorOut, err := json.Marshal(gpuResp.Tensor)
	if err != nil {
		return nil, fmt.Errorf("marshal Tensor features: %w", err)
	}
	essOut, err := json.Marshal(cpuResp.Essentia)
	if err != nil {
		return nil, fmt.Errorf("marshal Essentia features: %w", err)
	}
	return &FeatureOutputs{
		LibOut:    string(libOut),
		TensorOut: string(tensorOut),
		EssOut:    string(essOut),
	}, nil
}

func (d *Dispatcher) recordFeatureLaneStats(start time.Time, cpuResp, gpuResp *DaemonResponse) {
	if d.statsTracker == nil {
		return
	}
	d.statsTracker.RecordStageDuration("daemon_extract", time.Since(start))
	for _, profile := range []map[string]float64{cpuResp.Profile, gpuResp.Profile} {
		if libSec, ok := profile["librosa_sec"]; ok {
			d.statsTracker.RecordStageDuration("librosa", time.Duration(libSec*float64(time.Second)))
		}
		if tenSec, ok := profile["tensor_sec"]; ok {
			d.statsTracker.RecordStageDuration("tensor", time.Duration(tenSec*float64(time.Second)))
		}
		if essSec, ok := profile["essentia_sec"]; ok {
			d.statsTracker.RecordStageDuration("essentia", time.Duration(essSec*float64(time.Second)))
		}
	}
}

func (d *Dispatcher) trackRetiringClient(c *WorkerDaemonClient) {
	if d == nil || c == nil {
		return
	}
	d.retireMu.Lock()
	if d.pendingRetirements == nil {
		d.pendingRetirements = make(map[*WorkerDaemonClient]struct{})
	}
	d.pendingRetirements[c] = struct{}{}
	d.retireMu.Unlock()
}

func (d *Dispatcher) untrackRetiringClient(c *WorkerDaemonClient) {
	if d == nil || c == nil {
		return
	}
	d.retireMu.Lock()
	delete(d.pendingRetirements, c)
	d.retireMu.Unlock()
}

// executeFeaturesStage runs one CPU and one GPU daemon lane concurrently using a mandatory
// ArenaLease pointer in SHM mode so any timed-out worker retirement transfers lease cleanup
// until OS process exit is confirmed.
func (d *Dispatcher) executeFeaturesStage(
	demucsSR int,
	trackHash string,
	demucsStems map[string]StemInfo,
	arenaLease **ArenaLease,
	storageMode StorageMode,
	task TaskPayload,
	currentCfg Config,
) (_ *FeatureOutputs, err error) {
	if d.cpuDaemonPool == nil || d.gpuDaemonPool == nil {
		return nil, fmt.Errorf("CPU and GPU worker daemon pools are required")
	}
	var arenaSet *WorkerArenaSet
	if arenaLease != nil && *arenaLease != nil {
		arenaSet = (*arenaLease).Set
	}
	if storageMode == StorageModeSHM && (arenaLease == nil || *arenaLease == nil || arenaSet == nil) {
		return nil, fmt.Errorf("SHM feature extraction requires an active ArenaLease")
	}

	tracker := &liveRetirementTracker{}
	defer func() {
		if len(tracker.snapshot()) > 0 {
			// Transfer arena lease cleanup until all live timed-out readers confirm exit;
			// avoid UnfreezeAll while a dying worker may still map the SHM view.
			if transErr := transferOrCloseArenaLease(arenaLease, tracker, d.LogError); transErr != nil {
				err = errors.Join(err, transErr)
			}
			return
		}
		if arenaSet != nil {
			err = errors.Join(err, arenaSet.UnfreezeAll())
		}
	}()

	start := time.Now()
	timeoutDur := ComputeAdaptiveTimeoutPure(
		task,
		currentCfg.FeatureExtractTimeoutSec,
		currentCfg.AdaptiveTimeoutRatio,
		currentCfg.MaxAdaptiveTimeoutSec,
	)
	ctx, cancel := context.WithTimeout(d.currentExecutionContext(), timeoutDur)
	defer cancel()
	laneCtxBase := withLiveRetirementTracker(ctx, tracker)
	payload := ExtractAllPayload{SR: demucsSR, TrackHash: trackHash, Stems: demucsStems}

	cpuResp, gpuResp, err := runFeatureLanes(
		laneCtxBase,
		func(laneCtx context.Context) (*DaemonResponse, error) {
			return d.executeFeatureLane(laneCtx, FeatureLaneCPU, payload)
		},
		func(laneCtx context.Context) (*DaemonResponse, error) {
			return d.executeFeatureLane(laneCtx, FeatureLaneGPU, payload)
		},
	)
	d.scheduleIdleGPUDaemonTrim()
	if err != nil {
		return nil, fmt.Errorf("feature extraction failed: %w", err)
	}
	d.recordFeatureLaneStats(start, cpuResp, gpuResp)
	return joinFeatureLaneResponses(cpuResp, gpuResp)
}
