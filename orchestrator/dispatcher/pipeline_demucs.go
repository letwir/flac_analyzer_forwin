// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// Mor: (Task, StorageMode, CacheDir) -> (TrackHash, DemucsSR, DemucsStems, ArenaSet, Error)
package dispatcher

import (
	"context"
	"fmt"
	"time"
)

// executeDemucsStage allocates SHM arenas (if SHM mode) and executes Demucs source separation via DemucsDaemonPool.
// SideEffectFn: executeDemucsStage (IO Monad)
func (d *Dispatcher) executeDemucsStage(
	id int,
	task TaskPayload,
	storageMode StorageMode,
	cacheDir string,
	currentCfg Config,
	stems []string,
) (string, int, map[string]StemInfo, *WorkerArenaSet, error) {
	timeoutDur := ComputeAdaptiveTimeoutPure(
		task,
		currentCfg.DemucsTimeoutSec,
		currentCfg.AdaptiveTimeoutRatio,
		currentCfg.MaxAdaptiveTimeoutSec,
	)
	ctxDemucs, cancelDemucs := context.WithTimeout(context.Background(), timeoutDur)
	defer cancelDemucs()

	d.LogInfo("[W-%d] [IO Monad] Waiting for Adaptive Demucs execution slot (limit: %d)...", id, d.demucsScheduler.GetLimit())
	if err := d.demucsScheduler.AcquireWithContext(ctxDemucs); err != nil {
		return "", 0, nil, nil, fmt.Errorf("failed to acquire Demucs slot (timeout/cancelled): %w", err)
	}
	defer d.demucsScheduler.Release()

	if delaySec := currentCfg.ShmAllocationDelaySec; delaySec > 0 {
		time.Sleep(time.Duration(delaySec) * time.Second)
	}

	var arenaSet *WorkerArenaSet
	var tagsMap map[string]string
	var allocError error

	if storageMode == StorageModeSHM {
		ratio := currentCfg.ShmExpansionRatio
		if ratio <= 0 {
			ratio = 3.5
		}
		estimatedSize := uint32(EstimateShmSizeForTaskWithRatio(task, ratio))

		shmAllocStart := time.Now()
		arenaSet = d.arenaPool.GetWorkerArenaSet(id)

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
			time.Sleep(3 * time.Second)
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
					allocError = fmt.Errorf("Failed to allocate/reuse SHM arena for %s (attempt %d/%d): %v", stem, attempt, retryCount, err)
					break
				}
			}
			if allocError == nil {
				break
			}

			if attempt < retryCount {
				d.LogWarn("[W-%d] SHM arena allocation limit hit (attempt %d/%d): %v. Throttling queue & sleeping %d seconds...", id, attempt, retryCount, allocError, retryDelaySec)
				d.allocMutex.Unlock()
				time.Sleep(time.Duration(retryDelaySec) * time.Second)
				d.allocMutex.Lock()
			}
		}
		d.allocMutex.Unlock()

		if d.statsTracker != nil {
			d.statsTracker.RecordShmAllocDuration(time.Since(shmAllocStart))
			d.statsTracker.RecordStageDuration("shm_alloc", time.Since(shmAllocStart))
		}

		if allocError != nil {
			_ = arenaSet.UnfreezeAll()
			return "", 0, nil, nil, allocError
		}

		tagsMap = arenaSet.GetTagsMap()
	}

	endSampleParam := task.EndSample
	if endSampleParam == 0 {
		endSampleParam = -1
	}
	demucsStageStart := time.Now()

	demucsClient, dErr := d.demucsPool.Acquire(ctxDemucs)
	if dErr != nil {
		if arenaSet != nil {
			_ = arenaSet.UnfreezeAll()
		}
		return "", 0, nil, nil, fmt.Errorf("failed to acquire Demucs daemon for separation: %w", dErr)
	}

	sepResp, sepErr := demucsClient.Separate(ctxDemucs, DemucsSeparatePayload{
		FlacPath:    task.FlacPath,
		ShmTags:     tagsMap,
		StorageMode: string(storageMode),
		TempDir:     cacheDir,
		StartSample: task.StartSample,
		EndSample:   endSampleParam,
		UseDml:      false,
	})
	d.demucsPool.Release(demucsClient)

	if d.statsTracker != nil {
		d.statsTracker.RecordStageDuration("demucs", time.Since(demucsStageStart))
	}

	if sepErr != nil {
		if arenaSet != nil {
			_ = arenaSet.UnfreezeAll()
		}
		return "", 0, nil, nil, fmt.Errorf("Demucs daemon separation failed: %w", sepErr)
	}

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
		if err := arenaSet.FreezeAll(); err != nil {
			d.LogWarn("[Worker %d] Failed to freeze SHM arenas: %v", id, err)
		}
		if err := arenaSet.VerifyIntegrity(stems); err != nil {
			_ = arenaSet.UnfreezeAll()
			return "", 0, nil, nil, fmt.Errorf("SHM integrity verification failed: %w", err)
		}
	}

	return sepResp.AudioHash, demucsSR, sepResp.Stems, arenaSet, nil
}
