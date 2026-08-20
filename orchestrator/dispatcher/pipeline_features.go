// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// Mor: (DemucsSR, TrackHash, Stems) -> (LibrosaJSON, TensorJSON, EssentiaJSON, Error)
package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// FeatureOutputs encapsulates serialized JSON outputs from worker daemon.
type FeatureOutputs struct {
	LibOut    string
	TensorOut string
	EssOut    string
}

// executeFeaturesStage executes zero-copy feature extraction via WorkerDaemonPool.
// SideEffectFn: executeFeaturesStage (IO Monad)
func (d *Dispatcher) executeFeaturesStage(
	demucsSR int,
	trackHash string,
	demucsStems map[string]StemInfo,
	arenaSet *WorkerArenaSet,
	storageMode StorageMode,
	task TaskPayload,
	currentCfg Config,
) (*FeatureOutputs, error) {
	daemonStageStart := time.Now()
	timeoutDur := ComputeAdaptiveTimeoutPure(
		task,
		currentCfg.FeatureExtractTimeoutSec,
		currentCfg.AdaptiveTimeoutRatio,
		currentCfg.MaxAdaptiveTimeoutSec,
	)

	ctxAcquire, cancelAcquire := context.WithTimeout(context.Background(), 120*time.Second)
	daemonClient, daemonErr := d.daemonPool.Acquire(ctxAcquire)
	cancelAcquire()

	if daemonErr != nil {
		if arenaSet != nil {
			_ = arenaSet.UnfreezeAll()
		}
		return nil, fmt.Errorf("failed to acquire worker daemon: %w", daemonErr)
	}

	extractPayload := ExtractAllPayload{
		SR:        demucsSR,
		TrackHash: trackHash,
		Stems:     demucsStems,
	}
	ctxExtract, cancelExtract := context.WithTimeout(context.Background(), timeoutDur)
	defer cancelExtract()

	daemonResp, daemonExtractErr := daemonClient.ExtractAll(ctxExtract, extractPayload)
	d.daemonPool.Release(daemonClient)

	if daemonExtractErr != nil {
		if arenaSet != nil {
			_ = arenaSet.UnfreezeAll()
		}
		return nil, fmt.Errorf("worker daemon extraction failed: %w", daemonExtractErr)
	}

	daemonStageDuration := time.Since(daemonStageStart)
	if d.statsTracker != nil {
		d.statsTracker.RecordStageDuration("daemon_extract", daemonStageDuration)
		if daemonResp.Profile != nil {
			if libSec, ok := daemonResp.Profile["librosa_sec"]; ok {
				d.statsTracker.RecordStageDuration("librosa", time.Duration(libSec*float64(time.Second)))
			}
			if tenSec, ok := daemonResp.Profile["tensor_sec"]; ok {
				d.statsTracker.RecordStageDuration("tensor", time.Duration(tenSec*float64(time.Second)))
			}
			if essSec, ok := daemonResp.Profile["essentia_sec"]; ok {
				d.statsTracker.RecordStageDuration("essentia", time.Duration(essSec*float64(time.Second)))
			}
		}
	}

	libOutBytes, err := json.Marshal(daemonResp.Librosa)
	if err != nil {
		if arenaSet != nil {
			_ = arenaSet.UnfreezeAll()
		}
		return nil, fmt.Errorf("failed to marshal Librosa features: %w", err)
	}
	tensorOutBytes, err := json.Marshal(daemonResp.Tensor)
	if err != nil {
		if arenaSet != nil {
			_ = arenaSet.UnfreezeAll()
		}
		return nil, fmt.Errorf("failed to marshal Tensor features: %w", err)
	}
	essOutBytes, err := json.Marshal(daemonResp.Essentia)
	if err != nil {
		if arenaSet != nil {
			_ = arenaSet.UnfreezeAll()
		}
		return nil, fmt.Errorf("failed to marshal Essentia features: %w", err)
	}

	if storageMode == StorageModeSHM && arenaSet != nil {
		_ = arenaSet.UnfreezeAll()
	}

	return &FeatureOutputs{
		LibOut:    string(libOutBytes),
		TensorOut: string(tensorOutBytes),
		EssOut:    string(essOutBytes),
	}, nil
}
