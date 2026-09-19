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

func (d *Dispatcher) executeFeatureLane(
	ctx context.Context,
	lane FeatureLane,
	payload ExtractAllPayload,
) (*DaemonResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s feature lane cancelled before acquire: %w", lane, err)
	}
	ctxAcquire, cancelAcquire := context.WithTimeout(ctx, 120*time.Second)
	defer cancelAcquire()

	pool := d.cpuDaemonPool
	if lane == FeatureLaneGPU {
		pool = d.gpuDaemonPool

		select {
		case d.gpuArbiter <- struct{}{}:
		case <-ctxAcquire.Done():
			return nil, fmt.Errorf("acquire GPU arbiter: %w", ctxAcquire.Err())
		}
		defer func() { <-d.gpuArbiter }()
	}
	if pool == nil {
		return nil, fmt.Errorf("%s worker daemon pool is unavailable", lane)
	}
	client, err := pool.Acquire(ctxAcquire)
	if err != nil {
		return nil, fmt.Errorf("acquire %s worker daemon: %w", lane, err)
	}
	defer pool.Release(client)
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s feature lane cancelled after acquire: %w", lane, err)
	}

	switch lane {
	case FeatureLaneCPU:
		return client.ExtractCPU(ctx, payload)
	case FeatureLaneGPU:
		return client.ExtractGPU(ctx, payload)
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

// executeFeaturesStage runs one CPU and one GPU daemon lane concurrently,
// then joins them into the established FeatureOutputs contract.
func (d *Dispatcher) executeFeaturesStage(
	demucsSR int,
	trackHash string,
	demucsStems map[string]StemInfo,
	arenaSet *WorkerArenaSet,
	storageMode StorageMode,
	task TaskPayload,
	currentCfg Config,
) (_ *FeatureOutputs, err error) {
	if d.cpuDaemonPool == nil || d.gpuDaemonPool == nil {
		return nil, fmt.Errorf("CPU and GPU worker daemon pools are required")
	}
	if arenaSet != nil {
		defer func() { _ = arenaSet.UnfreezeAll() }()
	}

	start := time.Now()
	timeoutDur := ComputeAdaptiveTimeoutPure(
		task,
		currentCfg.FeatureExtractTimeoutSec,
		currentCfg.AdaptiveTimeoutRatio,
		currentCfg.MaxAdaptiveTimeoutSec,
	)
	ctx, cancel := context.WithTimeout(d.currentExecutionContext(), timeoutDur)
	defer cancel()
	payload := ExtractAllPayload{SR: demucsSR, TrackHash: trackHash, Stems: demucsStems}

	cpuResp, gpuResp, err := runFeatureLanes(
		ctx,
		func(laneCtx context.Context) (*DaemonResponse, error) {
			return d.executeFeatureLane(laneCtx, FeatureLaneCPU, payload)
		},
		func(laneCtx context.Context) (*DaemonResponse, error) {
			return d.executeFeatureLane(laneCtx, FeatureLaneGPU, payload)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("feature extraction failed: %w", err)
	}
	d.recordFeatureLaneStats(start, cpuResp, gpuResp)
	return joinFeatureLaneResponses(cpuResp, gpuResp)
}
