// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// Mor: (Task, StorageMode, CacheDir) -> (TrackHash, DemucsSR, DemucsStems, ArenaSet, Error)
package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var demucsWavefrontGeneration atomic.Uint64

type stemWavefrontJob struct {
	event DemucsStemReadyEvent
}

// stemWavefront has exactly one consumer per feature lane. Each job contains a
// single frozen stem, so transfer of later stems remains backpressured while
// the first published stem is being analyzed. It intentionally sits after
// Demucs inference and does not affect model execution or its output order.
type stemWavefront struct {
	ctx       context.Context
	lifecycle *StemLifecycle
	run       func(context.Context, FeatureLane, ExtractAllPayload) (*DaemonResponse, error)
	cpuJobs   chan stemWavefrontJob
	gpuJobs   chan stemWavefrontJob
	closeOnce           sync.Once
	wg                  sync.WaitGroup
	firstGPUQueued      chan struct{}
	firstGPUQueuedOnce  sync.Once
	gpuConsumerDone     chan struct{}
	gpuConsumerDoneOnce sync.Once

	mu        sync.Mutex
	err       error
	trackHash string
	sr        int
	published map[string]struct{}
	cpu       *DaemonResponse
	gpu       *DaemonResponse

	// parent is the caller-owned context whose cancellation cause must survive even when a
	// local failure cancels the lifecycle child first. Optional.
	parent context.Context
}

func newStemWavefront(ctx context.Context, numStems int, lifecycle *StemLifecycle, cpuConsumers int, run func(context.Context, FeatureLane, ExtractAllPayload) (*DaemonResponse, error)) (*stemWavefront, error) {
	if ctx == nil || lifecycle == nil || run == nil {
		return nil, fmt.Errorf("stem wavefront requires context, lifecycle, and lane runner")
	}
	if cpuConsumers < 1 {
		cpuConsumers = 1
	}
	w := &stemWavefront{
		ctx:             ctx,
		lifecycle:       lifecycle,
		run:             run,
		cpuJobs:         make(chan stemWavefrontJob, numStems),
		gpuJobs:         make(chan stemWavefrontJob, numStems),
		firstGPUQueued:  make(chan struct{}),
		gpuConsumerDone: make(chan struct{}),
		published:       make(map[string]struct{}),
		cpu:             &DaemonResponse{Librosa: make(map[string]interface{}), Essentia: make(map[string]interface{})},
		gpu:             &DaemonResponse{Tensor: make(map[string]interface{})},
	}
	w.wg.Add(1 + cpuConsumers)
	for i := 0; i < cpuConsumers; i++ {
		go w.consume(FeatureLaneCPU, w.cpuJobs)
	}
	go w.consume(FeatureLaneGPU, w.gpuJobs)
	return w, nil
}

func (w *stemWavefront) Publish(event DemucsStemReadyEvent) error {
	if err := w.lifecycle.Publish(event.RequestID, event.Generation, event.Stem); err != nil {
		return err
	}
	select {
	case published := <-w.lifecycle.ReadyEvents():
		if published != (StemReadyEvent{RequestID: event.RequestID, Generation: event.Generation, Stem: event.Stem}) {
			err := fmt.Errorf("stem lifecycle published an unexpected event: %+v", published)
			w.fail(err)
			return err
		}
	case <-w.ctx.Done():
		return context.Cause(w.ctx)
	}

	w.mu.Lock()
	if w.trackHash == "" {
		w.trackHash, w.sr = event.AudioHash, event.SR
	} else if w.trackHash != event.AudioHash || w.sr != event.SR {
		w.mu.Unlock()
		err := fmt.Errorf("stem transfer metadata differs within one Demucs request")
		w.fail(err)
		return err
	}
	w.published[event.Stem] = struct{}{}
	w.mu.Unlock()

	job := stemWavefrontJob{event: event}
	for _, jobs := range []chan stemWavefrontJob{w.cpuJobs, w.gpuJobs} {
		select {
		case jobs <- job:
		case <-w.ctx.Done():
			return context.Cause(w.ctx)
		}
	}
	return nil
}

// ValidateFinalStems prevents a truncated event stream from silently
// producing partial legacy JSON after the final Demucs response arrives.
func (w *stemWavefront) ValidateFinalStems(stems map[string]StemInfo) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(stems) == 0 || len(stems) != len(w.published) {
		return fmt.Errorf("Demucs final stems and published stems differ")
	}
	for stem := range stems {
		if _, ok := w.published[stem]; !ok {
			return fmt.Errorf("Demucs final stem %q was never published", stem)
		}
	}
	return nil
}

func (w *stemWavefront) CloseInput() {
	w.closeOnce.Do(func() {
		close(w.cpuJobs)
		close(w.gpuJobs)
	})
}

func (w *stemWavefront) Fail(err error) {
	if err == nil {
		err = ErrStemTransition
	}
	w.fail(err)
}

func (w *stemWavefront) fail(err error) {
	if err == nil {
		return
	}
	w.mu.Lock()
	firstFailure := w.err == nil
	if firstFailure {
		w.err = err
	} else if !errors.Is(w.err, err) {
		w.err = errors.Join(w.err, err)
	}
	w.mu.Unlock()
	if firstFailure {
		w.lifecycle.Fail(err)
	}
}

func (w *stemWavefront) Wait() (*DaemonResponse, *DaemonResponse, string, int, error) {
	w.wg.Wait()
	w.mu.Lock()
	defer w.mu.Unlock()
	causeErr := context.Cause(w.ctx)
	if w.parent != nil {
		causeErr = joinWavefrontCause(causeErr, context.Cause(w.parent))
	}
	if w.err != nil {
		if causeErr != nil && !errors.Is(w.err, causeErr) {
			return nil, nil, "", 0, errors.Join(causeErr, w.err)
		}
		return nil, nil, "", 0, w.err
	}
	if causeErr != nil {
		return nil, nil, "", 0, causeErr
	}
	if w.trackHash == "" || w.sr <= 0 {
		return nil, nil, "", 0, fmt.Errorf("Demucs completed without a published stem")
	}
	return w.cpu, w.gpu, w.trackHash, w.sr, nil
}

func (w *stemWavefront) markFirstGPUQueued() {
	if w != nil && w.firstGPUQueued != nil {
		w.firstGPUQueuedOnce.Do(func() {
			close(w.firstGPUQueued)
		})
	}
}

func (w *stemWavefront) markGPUConsumerDone() {
	if w != nil && w.gpuConsumerDone != nil {
		w.gpuConsumerDoneOnce.Do(func() {
			close(w.gpuConsumerDone)
		})
	}
}

func (w *stemWavefront) WaitFirstGPUFeatureQueued(ctx context.Context) bool {
	if w == nil || w.firstGPUQueued == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-w.firstGPUQueued:
		return true
	default:
	}
	select {
	case <-w.firstGPUQueued:
		return true
	case <-w.gpuConsumerDone:
		select {
		case <-w.firstGPUQueued:
			return true
		default:
			return false
		}
	case <-w.ctx.Done():
		select {
		case <-w.firstGPUQueued:
			return true
		default:
			return false
		}
	case <-ctx.Done():
		select {
		case <-w.firstGPUQueued:
			return true
		default:
			return false
		}
	}
}

func (w *stemWavefront) consume(lane FeatureLane, jobs <-chan stemWavefrontJob) {
	defer w.wg.Done()
	if lane == FeatureLaneGPU {
		defer w.markGPUConsumerDone()
	}
	for {
		select {
		case <-w.ctx.Done():
			return
		case job, ok := <-jobs:
			if !ok {
				return
			}
			if err := w.lifecycle.Acquire(job.event.Generation, job.event.Stem); err != nil {
				w.fail(fmt.Errorf("acquire %s stem %q: %w", lane, job.event.Stem, err))
				return
			}
			payload := ExtractAllPayload{
				SR:        job.event.SR,
				TrackHash: job.event.AudioHash,
				Stems:     map[string]StemInfo{job.event.Stem: job.event.Info},
			}
			runCtx := w.ctx
			if lane == FeatureLaneGPU {
				runCtx = withGPUArbiterQueuedHook(w.ctx, w.markFirstGPUQueued)
			}
			response, runErr := w.run(runCtx, lane, payload)
			releaseErr := w.lifecycle.Release(job.event.Generation, job.event.Stem)
			if runErr != nil || releaseErr != nil {
				w.fail(errors.Join(wrapFeatureLaneError(lane, runErr), releaseErr))
				return
			}
			if response == nil {
				w.fail(fmt.Errorf("%s feature lane returned no response", lane))
				return
			}
			w.merge(lane, response)
		}
	}
}

func (w *stemWavefront) merge(lane FeatureLane, response *DaemonResponse) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch lane {
	case FeatureLaneCPU:
		mergeFeatureMap(w.cpu.Librosa, response.Librosa)
		mergeFeatureMap(w.cpu.Essentia, response.Essentia)
	case FeatureLaneGPU:
		mergeFeatureMap(w.gpu.Tensor, response.Tensor)
	}
}

func mergeFeatureMap(dst, src map[string]interface{}) {
	for key, value := range src {
		if nested, ok := value.(map[string]interface{}); ok {
			if existing, ok := dst[key].(map[string]interface{}); ok {
				mergeFeatureMap(existing, nested)
				continue
			}
			copy := make(map[string]interface{}, len(nested))
			mergeFeatureMap(copy, nested)
			dst[key] = copy
			continue
		}
		dst[key] = value
	}
}

func freezeStemForWavefront(storageMode StorageMode, arenaSet *WorkerArenaSet, stem string) error {
	if storageMode != StorageModeSHM {
		return nil
	}
	if arenaSet == nil {
		return fmt.Errorf("missing SHM arena set for published stem %q", stem)
	}
	return arenaSet.FreezeStem(stem)
}

func waitForExecutionDelay(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// executeDemucsStage allocates SHM arenas (if SHM mode) and executes Demucs source separation via DemucsDaemonPool.
// SideEffectFn: executeDemucsStage (IO Monad)
func (d *Dispatcher) executeDemucsStage(
	id int,
	task TaskPayload,
	storageMode StorageMode,
	cacheDir string,
	currentCfg Config,
	stems []string,
) (audioHash string, sampleRate int, stemInfos map[string]StemInfo, resultArenaLease *ArenaLease, features *FeatureOutputs, retErr error) {
	demucsTimeoutDur := ComputeAdaptiveTimeoutPure(
		task,
		currentCfg.DemucsTimeoutSec,
		currentCfg.AdaptiveTimeoutRatio,
		currentCfg.MaxAdaptiveTimeoutSec,
	)
	ctxParent := d.currentExecutionContext()
	ctxDemucs, cancelDemucs := context.WithTimeout(ctxParent, demucsTimeoutDur)
	defer cancelDemucs()

	featureTimeoutDur := ComputeAdaptiveTimeoutPure(
		task,
		currentCfg.FeatureExtractTimeoutSec,
		currentCfg.AdaptiveTimeoutRatio,
		currentCfg.MaxAdaptiveTimeoutSec,
	)
	ctxPost, cancelPost := context.WithTimeout(ctxParent, demucsTimeoutDur+featureTimeoutDur)
	defer cancelPost()

	if delaySec := currentCfg.ShmAllocationDelaySec; delaySec > 0 {
		if err := waitForExecutionDelay(ctxDemucs, time.Duration(delaySec)*time.Second); err != nil {
			return "", 0, nil, nil, nil, fmt.Errorf("SHM allocation delay cancelled: %w", err)
		}
	}

	var arenaLease *ArenaLease
	var arenaSet *WorkerArenaSet
	var tagsMap map[string]string
	var allocError error
	var arenaCleanupErr error
	defer func() {
		retErr = errors.Join(retErr, arenaCleanupErr)
	}()
	retireTracker := &liveRetirementTracker{}
	closeArenaOnError := func() {
		if len(retireTracker.snapshot()) > 0 {
			arenaSet = nil
			arenaCleanupErr = errors.Join(arenaCleanupErr, transferOrCloseArenaLease(&arenaLease, retireTracker, d.LogError))
			return
		}
		if arenaSet != nil {
			arenaCleanupErr = errors.Join(arenaCleanupErr, arenaSet.UnfreezeAll())
			arenaSet = nil
		}
		if arenaLease != nil {
			arenaCleanupErr = errors.Join(arenaCleanupErr, arenaLease.CloseAndRelease())
			arenaLease = nil
		}
	}

	if storageMode == StorageModeSHM {
		ratio := currentCfg.ShmExpansionRatio
		if ratio <= 0 {
			ratio = 3.5
		}
		estimatedSize := uint32(EstimateShmSizeForTaskWithRatio(task, ratio))

		shmAllocStart := time.Now()
		var acquireErr error
		arenaLease, acquireErr = d.arenaPool.AcquireArenaLease(id)
		if acquireErr != nil {
			return "", 0, nil, nil, nil, fmt.Errorf("acquire worker SHM arena lease: %w", acquireErr)
		}
		defer func() {
			// Disarmed automatically on success when handedOffLease clears arenaLease/arenaSet.
			closeArenaOnError()
		}()
		arenaSet = arenaLease.Set
		d.allocMutex.Lock()
		for {
			availPhysMem, err := GetAvailableMemory()
			if err != nil {
				d.LogWarn("[W-%d] Memory check failed: %v", id, err)
				break
			}
			totalStemsNeeded := uint64(estimatedSize) * uint64(len(stems))
			requiredMem := totalStemsNeeded + (2 * 1024 * 1024 * 1024)
			if availPhysMem > requiredMem {
				break
			}
			d.LogInfo("[W-%d] Waiting for memory for all stems (%d MB total)... (Avail: %d MB)", id, totalStemsNeeded/1024/1024, availPhysMem/1024/1024)

			d.allocMutex.Unlock()
			if err := waitForExecutionDelay(ctxDemucs, 3*time.Second); err != nil {
				closeArenaOnError()
				return "", 0, nil, nil, nil, fmt.Errorf("memory wait cancelled: %w", err)
			}
			d.allocMutex.Lock()
		}

		retryCount := currentCfg.ShmRetryCount
		if retryCount <= 0 {
			retryCount = 5
		}
		retryDelaySec := currentCfg.ShmRetryDelaySec
		if retryDelaySec <= 0 {
			retryDelaySec = 8
		}

		for attempt := 1; attempt <= retryCount; attempt++ {
			allocError = nil
			for _, stem := range stems {
				if _, err := arenaSet.GetOrCreateArena(stem, estimatedSize); err != nil {
					allocError = fmt.Errorf("Failed to allocate/reuse SHM arena for %s (attempt %d/%d): %w", stem, attempt, retryCount, err)
					break
				}
			}
			if allocError == nil {
				break
			}

			if attempt < retryCount {
				d.LogWarn("[W-%d] SHM arena allocation limit hit (attempt %d/%d): %v. Throttling queue & sleeping %d seconds...", id, attempt, retryCount, allocError, retryDelaySec)
				d.allocMutex.Unlock()
				if err := waitForExecutionDelay(ctxDemucs, time.Duration(retryDelaySec)*time.Second); err != nil {
					closeArenaOnError()
					return "", 0, nil, nil, nil, fmt.Errorf("SHM retry delay cancelled: %w", err)
				}
				d.allocMutex.Lock()
			}
		}
		d.allocMutex.Unlock()

		if d.statsTracker != nil {
			d.statsTracker.RecordShmAllocDuration(time.Since(shmAllocStart))
			d.statsTracker.RecordStageDuration("shm_alloc", time.Since(shmAllocStart))
		}

		if allocError != nil {
			closeArenaOnError()
			return "", 0, nil, nil, nil, allocError
		}

		tagsMap = arenaSet.GetTagsMap()
	}

	// Actual GPU/Demucs execution capacity is acquired only after the backing
	// storage has been successfully prepared. A task waiting on SHM therefore
	// cannot consume an execution slot.
	d.LogInfo("[W-%d] [IO Monad] Waiting for Adaptive Demucs execution slot (limit: %d)...", id, d.demucsScheduler.GetLimit())
	if err := d.demucsScheduler.AcquireWithContext(ctxDemucs); err != nil {
		closeArenaOnError()
		return "", 0, nil, nil, nil, fmt.Errorf("%w: failed to acquire Demucs slot (timeout/cancelled): %w", ErrRetryableTimeout, err)
	}
	demucsReleased := false
	defer func() {
		if !demucsReleased {
			d.demucsScheduler.Release()
		}
	}()

	executionLease, err := d.reserveExecutionAdmission()
	if err != nil {
		closeArenaOnError()
		return "", 0, nil, nil, nil, fmt.Errorf("%w: failed to reserve CPU/GPU/Demucs execution lease: %w", ErrRetryableTimeout, err)
	}
	leaseReleased := false
	defer func() {
		if !leaseReleased {
			d.releaseExecutionAdmission(executionLease)
		}
	}()

	endSampleParam := task.EndSample
	if endSampleParam == 0 {
		endSampleParam = -1
	}
	demucsStageStart := time.Now()

	if d.demucsPool == nil {
		closeArenaOnError()
		return "", 0, nil, nil, nil, fmt.Errorf("Demucs daemon pool is not initialized")
	}
	demucsClient, dErr := d.demucsPool.Acquire(ctxDemucs)
	if dErr != nil {
		closeArenaOnError()
		return "", 0, nil, nil, nil, fmt.Errorf("failed to acquire Demucs daemon for separation: %w", dErr)
	}
	if demucsClient == nil {
		closeArenaOnError()
		return "", 0, nil, nil, nil, fmt.Errorf("acquired nil Demucs daemon client")
	}
	demucsClientReleased := false
	defer func() {
		if !demucsClientReleased {
			d.demucsPool.Release(demucsClient)
		}
	}()

	generation := demucsWavefrontGeneration.Add(1)
	requestID := fmt.Sprintf("demucs-%d-%d", generation, time.Now().UnixNano())
	lifecycle, waveCtx, err := NewStemLifecycle(ctxPost, requestID, generation, stems, 2, 1)
	if err != nil {
		closeArenaOnError()
		return "", 0, nil, nil, nil, fmt.Errorf("create stem lifecycle: %w", err)
	}
	defer lifecycle.Close()
	cpuConsumers := GetCPUConsumerCount(task, d.GetConfig().NumWorkers, len(stems))
	wavefrontCtx := withLiveRetirementTracker(waveCtx, retireTracker)
	wavefront, err := newStemWavefront(wavefrontCtx, len(stems), lifecycle, cpuConsumers, func(ctx context.Context, lane FeatureLane, payload ExtractAllPayload) (*DaemonResponse, error) {
		return d.executeFeatureLane(ctx, lane, payload)
	})
	if err != nil {
		closeArenaOnError()
		return "", 0, nil, nil, nil, err
	}
	wavefront.parent = ctxPost

	var cpuResp, gpuResp *DaemonResponse
	var waveHash string
	var waveSR int
	var waveErr error
	var wavefrontTeardownOnce sync.Once
	finishWavefront := func(cause error) {
		wavefrontTeardownOnce.Do(func() {
			if cause != nil {
				wavefront.Fail(cause)
			}
			wavefront.CloseInput()
			cpuResp, gpuResp, waveHash, waveSR, waveErr = wavefront.Wait()
		})
	}
	defer func() {
		finishWavefront(ErrStemTransition)
	}()

	if d.gpuArbiter == nil {
		arbiterErr := fmt.Errorf("GPU arbiter is unavailable")
		finishWavefront(arbiterErr)
		closeArenaOnError()
		return "", 0, nil, nil, nil, joinWavefrontCause(arbiterErr, waveErr)
	}
	gpuArbiterReleased := false
	if err := d.gpuArbiter.AcquireDemucs(ctxDemucs); err != nil {
		finishWavefront(err)
		closeArenaOnError()
		// Preserve context cancellation versus our own internal timeouts.
		if errors.Is(err, context.DeadlineExceeded) {
			return "", 0, nil, nil, nil, joinWavefrontCause(fmt.Errorf("%w: failed to acquire GPU arbiter: %w", ErrRetryableTimeout, err), waveErr)
		}
		return "", 0, nil, nil, nil, joinWavefrontCause(err, waveErr)
	}
	defer func() {
		if !gpuArbiterReleased {
			d.gpuArbiter.Release()
		}
	}()

	d.LogInfo("[DemucsQueue] Processing next queued item: %q", taskQueueLabel(task))
	downstreamLogged := false
	sepResp, sepErr := demucsClient.SeparateWithEvents(ctxDemucs, DemucsSeparatePayload{
		RequestID:      requestID,
		FlacPath:       task.FlacPath,
		ShmTags:        tagsMap,
		StorageMode:    string(storageMode),
		TempDir:        cacheDir,
		StartSample:    task.StartSample,
		EndSample:      endSampleParam,
		UseDml:         false,
		Generation:     generation,
		RequestedStems: stems,
	}, func(event DemucsStemReadyEvent) error {
		if err := freezeStemForWavefront(storageMode, arenaSet, event.Stem); err != nil {
			return err
		}
		if err := wavefront.Publish(event); err != nil {
			return err
		}
		if !downstreamLogged {
			d.LogInfo("[DemucsQueue] Stem ready; parallel feature processing started, DB ingest follows: %q", taskQueueLabel(task))
			downstreamLogged = true
		}
		return nil
	})

	if d.statsTracker != nil {
		d.statsTracker.RecordStageDuration("demucs", time.Since(demucsStageStart))
	}

	if !gpuArbiterReleased {
		if sepErr == nil && downstreamLogged {
			if wavefront.WaitFirstGPUFeatureQueued(ctxDemucs) {
				d.gpuArbiter.RegisterPostDemucsFeatureTurn()
			}
		}
		d.gpuArbiter.Release()
		gpuArbiterReleased = true
	}
	d.demucsPool.Release(demucsClient)
	demucsClientReleased = true
	if !demucsReleased {
		d.demucsScheduler.Release()
		demucsReleased = true
	}
	if !leaseReleased {
		d.releaseExecutionAdmission(executionLease)
		leaseReleased = true
	}

	finishWavefront(sepErr)
	d.scheduleIdleGPUDaemonTrim()

	if sepErr != nil {
		closeArenaOnError()
		return "", 0, nil, nil, nil, combineDemucsWavefrontError(sepErr, waveErr)
	}

	if waveErr != nil {
		closeArenaOnError()
		return "", 0, nil, nil, nil, fmt.Errorf("post-inference stem wavefront failed: %w", waveErr)
	}

	if sepResp == nil {
		closeArenaOnError()
		return "", 0, nil, nil, nil, fmt.Errorf("Demucs daemon separation returned nil response")
	}
	if sepResp.AudioHash != waveHash || sepResp.SR != waveSR {
		closeArenaOnError()
		return "", 0, nil, nil, nil, fmt.Errorf("final Demucs response does not match stem transfer metadata")
	}
	if err := wavefront.ValidateFinalStems(sepResp.Stems); err != nil {
		closeArenaOnError()
		return "", 0, nil, nil, nil, err
	}
	joinedFeatures, err := joinFeatureLaneResponses(cpuResp, gpuResp)
	if err != nil {
		closeArenaOnError()
		return "", 0, nil, nil, nil, fmt.Errorf("join stem wavefront features: %w", err)
	}
	d.recordFeatureLaneStats(demucsStageStart, cpuResp, gpuResp)

	if d.statsTracker != nil && sepResp.Profile != nil {
		for step, dur := range sepResp.Profile {
			d.statsTracker.RecordPythonStepDuration("demucs", step, dur)
		}
	}

	demucsSR := sepResp.SR
	if demucsSR == 0 {
		demucsSR = 44100
	}

	if storageMode == StorageModeSHM && arenaSet != nil {
		if err := arenaSet.VerifyIntegrity(stems); err != nil {
			closeArenaOnError()
			return "", 0, nil, nil, nil, fmt.Errorf("SHM integrity verification failed: %w", err)
		}
	}

	// Handoff ownership of arenaLease to the caller now that all checks passed.
	handedOffLease := arenaLease
	arenaLease = nil
	arenaSet = nil
	return sepResp.AudioHash, demucsSR, sepResp.Stems, handedOffLease, joinedFeatures, nil
}

// executeMixOnlyStage decodes the original mix into one bounded backing area
// and runs the feature lanes without invoking Demucs inference.
func (d *Dispatcher) executeMixOnlyStage(id int, task TaskPayload, storageMode StorageMode, cacheDir string, cfg Config) (audioHash string, features *FeatureOutputs, resultArenaLease *ArenaLease, retErr error) {
	timeout := ComputeAdaptiveTimeoutPure(task, cfg.FeatureExtractTimeoutSec, cfg.AdaptiveTimeoutRatio, cfg.MaxAdaptiveTimeoutSec)
	ctx, cancel := context.WithTimeout(d.currentExecutionContext(), timeout)
	defer cancel()

	var arenaLease *ArenaLease
	var arenaSet *WorkerArenaSet
	var arenaCleanupErr error
	leaseHandedOff := false
	closeOnError := func() {
		if arenaSet != nil {
			arenaCleanupErr = errors.Join(arenaCleanupErr, arenaSet.UnfreezeAll())
			arenaSet = nil
		}
		if arenaLease != nil {
			arenaCleanupErr = errors.Join(arenaCleanupErr, arenaLease.CloseAndRelease())
			arenaLease = nil
		}
	}
	defer func() {
		if !leaseHandedOff {
			closeOnError()
		}
		retErr = errors.Join(retErr, arenaCleanupErr)
	}()
	tags := map[string]string{}
	if storageMode == StorageModeSHM {
		ratio := cfg.ShmExpansionRatio
		if ratio <= 0 {
			ratio = 3.5
		}
		var err error
		arenaLease, err = d.arenaPool.AcquireArenaLease(id)
		if err != nil {
			return "", nil, nil, fmt.Errorf("acquire mix worker SHM arena lease: %w", err)
		}
		arenaSet = arenaLease.Set
		if _, err := arenaSet.GetOrCreateArena("mix", uint32(EstimateShmSizeForTaskWithRatio(task, ratio))); err != nil {
			closeOnError()
			return "", nil, nil, fmt.Errorf("allocate mix SHM: %w", err)
		}
		tags = arenaSet.GetTagsMap()
	}
	if d.demucsPool == nil {
		closeOnError()
		return "", nil, nil, fmt.Errorf("decoder daemon pool is not initialized")
	}
	client, err := d.demucsPool.Acquire(ctx)
	if err != nil {
		closeOnError()
		return "", nil, nil, fmt.Errorf("acquire decoder daemon: %w", err)
	}
	if client == nil {
		closeOnError()
		return "", nil, nil, fmt.Errorf("acquired nil decoder daemon client")
	}
	d.LogInfo("[DemucsQueue] Processing next queued item: %q", taskQueueLabel(task))
	endSample := task.EndSample
	if endSample == 0 {
		endSample = -1
	}
	response, err := client.DecodeMix(ctx, DemucsSeparatePayload{
		FlacPath: task.FlacPath, ShmTags: tags, StorageMode: string(storageMode), TempDir: cacheDir,
		StartSample: task.StartSample, EndSample: endSample,
	})
	d.demucsPool.Release(client)
	if err != nil {
		closeOnError()
		return "", nil, nil, fmt.Errorf("decode raw mix: %w", err)
	}
	if response == nil {
		closeOnError()
		return "", nil, nil, fmt.Errorf("decode raw mix returned nil response")
	}
	if storageMode == StorageModeSHM {
		if err := freezeStemForWavefront(storageMode, arenaSet, "mix"); err != nil {
			closeOnError()
			return "", nil, nil, err
		}
	}
	features, err = d.executeFeaturesStage(response.SR, response.AudioHash, response.Stems, &arenaLease, storageMode, task, cfg)
	if err != nil {
		if arenaLease == nil {
			// Ownership already transferred to async live-worker retirement waiter.
			arenaSet = nil
		}
		closeOnError()
		return "", nil, nil, err
	}
	leaseHandedOff = true
	return response.AudioHash, features, arenaLease, nil
}

func joinWavefrontCause(primary, waveErr error) error {
	if waveErr == nil || errors.Is(primary, waveErr) {
		return primary
	}
	return errors.Join(primary, waveErr)
}

func combineDemucsWavefrontError(sepErr, waveErr error) error {
	combined := fmt.Errorf("Demucs daemon separation failed: %w", sepErr)
	if waveErr != nil && !errors.Is(sepErr, waveErr) {
		return errors.Join(combined, fmt.Errorf("post-inference stem wavefront failed: %w", waveErr))
	}
	return combined
}
