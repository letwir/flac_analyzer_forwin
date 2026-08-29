// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// Mor: (WorkerID, TaskPayload) -> PipelineExecution (IO Monad)
package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flac_analyzer/orchestrator/logger"
	"flac_analyzer/orchestrator/metrics"
	"flac_analyzer/orchestrator/state"
	"flac_analyzer/orchestrator/sysinfo"
)

// executeTaskPipeline executes the full sequential DSP pipeline for a single track:
// Gatekeeper -> HashCheck -> Demucs (SHM/Disk) -> Feature Extract (Daemon) -> Tagging -> Ingest Queue
// SideEffectFn: executeTaskPipeline (IO Monad)
func (d *Dispatcher) executeTaskPipeline(id int, task TaskPayload) {
	d.executeTaskPipelineWithMode(id, task, false)
}

func (d *Dispatcher) executeTaskPipelineWithMode(id int, task TaskPayload, synchronousIngest bool) {
	taskStartTime := time.Now()
	taskSuccess := false
	var arenaSet *WorkerArenaSet
	defer func() {
		// SHM arenas are owned by a worker for reuse, but retaining a long-track
		// arena after the task pins its pages and makes the RAM guard observe
		// low system availability even when no task is in flight.
		if arenaSet != nil {
			arenaSet.Close()
		}
	}()
	defer func() {
		if d.statsTracker != nil {
			d.statsTracker.RecordTaskCompletion(task.FlacPath, time.Since(taskStartTime), taskSuccess)
			d.statsTracker.SetQueueLength(len(d.taskQueue))
		}
	}()

	metrics.AnalyzerQueueLength.Dec()
	metrics.AnalyzerActiveWorkers.Inc()
	defer metrics.AnalyzerActiveWorkers.Dec()

	memInfo, _ := sysinfo.GetMemoryInfo()
	var availPhys uint64 = 0
	if memInfo != nil {
		availPhys = memInfo.AvailPhys
	}
	currentCfg := d.GetConfig()
	minAvailBytes := uint64(currentCfg.MinAvailRamGB * 1024 * 1024 * 1024)

	d.inFlightMutex.Lock()
	inFlight := d.activeInFlightRamBytes
	d.inFlightMutex.Unlock()

	storageMode, effectiveTaskRam, _ := DetermineStorageModePure(
		task,
		availPhys,
		inFlight,
		minAvailBytes,
		currentCfg.DiskModeRamThresholdRatio,
		currentCfg.EnableDiskModeFallback,
	)

	// Synchronize activeInFlightRamBytes with clamped effective RAM
	d.inFlightMutex.Lock()
	d.activeInFlightRamBytes += effectiveTaskRam
	d.inFlightMutex.Unlock()

	defer func() {
		d.inFlightMutex.Lock()
		if d.activeInFlightRamBytes >= effectiveTaskRam {
			d.activeInFlightRamBytes -= effectiveTaskRam
		} else {
			d.activeInFlightRamBytes = 0
		}
		d.inFlightMutex.Unlock()
	}()

	d.LogInfo("[W-%d] [IO Monad] Starting processing (%s mode): %s (Track %d)", id, storageMode, task.FlacPath, task.TrackNumber)
	d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusRunning, "")

	var trackHash string
	stems := []string{"mix", "bass", "drums", "vocals", "other", "guitar", "piano"}

	defer func() {
		cleanupCache(trackHash)
	}()

	// 1. Hash Calculation & Duplicate Detection
	if currentCfg.SkipDupByHash {
		isDup, computedHash, hashErr := d.checkDuplicateHash(id, task)
		if hashErr != nil {
			d.failTask(task, hashErr.Error())
			return
		}
		trackHash = computedHash
		if isDup {
			d.LogInfo("[W-%d] [IO Monad] Skip processing: Hash %s already exists in PostgreSQL", id, trackHash)
			d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusCompleted, "")
			metrics.AnalyzerTasksTotal.WithLabelValues("success").Inc()
			taskSuccess = true
			return
		}
	}

	cacheDir := filepath.Join(os.TempDir(), "flac_analyzer_cache", trackHash)
	if storageMode == StorageModeDisk {
		if err := os.MkdirAll(cacheDir, 0755); err != nil {
			d.failTask(task, fmt.Sprintf("Failed to create disk cache dir: %v", err))
			return
		}
		d.LogInfo("[W-%d] [DiskMode] Using SSD temp directory for stem spooling: %s", id, cacheDir)
	}

	// 2. Demucs Separation Stage (SHM/Disk)
	var demucsStems map[string]StemInfo
	var demucsSR int
	var computedHash string
	var demucsErr error
	computedHash, demucsSR, demucsStems, arenaSet, demucsErr = d.executeDemucsStage(id, task, storageMode, cacheDir, currentCfg, stems)
	if demucsErr != nil {
		d.failTask(task, demucsErr.Error())
		return
	}
	if trackHash == "" {
		trackHash = computedHash
	}

	// 3. Feature Extraction Stage (Daemon)
	feats, featErr := d.executeFeaturesStage(demucsSR, trackHash, demucsStems, arenaSet, storageMode, task, currentCfg)
	if featErr != nil {
		d.failTask(task, featErr.Error())
		return
	}
	// Feature extraction has closed its read handles, so release the producer
	// mappings before tagging and ingestion continue.
	if arenaSet != nil {
		arenaSet.Close()
		arenaSet = nil
	}

	// 4. Tagging & Queue Output Stage
	if err := d.executeTaggerStage(id, task, trackHash, feats); err != nil {
		d.failTask(task, err.Error())
		return
	}

	// 5. Decoupled Asynchronous DB Ingestion
	ingestPayload := IngestPayload{
		TrackHash:    trackHash,
		Task:         task,
		LibrosaJSON:  json.RawMessage(feats.LibOut),
		EssentiaJSON: json.RawMessage(feats.EssOut),
		TensorJSON:   json.RawMessage(feats.TensorOut),
	}

	if synchronousIngest {
		d.processIngestPayloadComplex(ingestPayload)
	} else {
		d.ingestQueue <- ingestPayload
	}
	taskSuccess = true
	d.LogInfo("[W-%d] Compute & tagging completed, dispatched to IngestWorker: %s (Track %d)", id, task.FlacPath, task.TrackNumber)
}

// checkDuplicateHash determines if the track audio hash already exists in PostgreSQL.
func (d *Dispatcher) checkDuplicateHash(id int, task TaskPayload) (bool, string, error) {
	hashStageStart := time.Now()
	isSingleTrack := task.StartSample == 0 && (task.EndSample <= 0 || task.EndSample == task.FileSize)
	var trackHash string

	if isSingleTrack {
		if fastMD5, err := ExtractFlacStreaminfoMD5(task.FlacPath); err == nil && fastMD5 != "" {
			trackHash = fastMD5
			d.LogDebug("[W-%d] [FastPath] Extracted STREAMINFO MD5 directly: %s", id, trackHash)
		}
	}

	if trackHash == "" {
		endSampleParam := task.EndSample
		if endSampleParam == 0 {
			endSampleParam = -1
		}
		ctxHash, cancelHash := context.WithTimeout(d.currentExecutionContext(), 120*time.Second)
		defer cancelHash()

		demucsClient, dErr := d.demucsPool.Acquire(ctxHash)
		if dErr != nil {
			return false, "", fmt.Errorf("failed to acquire Demucs daemon for hash check: %w", dErr)
		}
		hashResp, hashErr := demucsClient.CheckHash(ctxHash, DemucsCheckHashPayload{
			FlacPath:    task.FlacPath,
			StartSample: task.StartSample,
			EndSample:   endSampleParam,
		})
		d.demucsPool.Release(demucsClient)

		if hashErr != nil {
			return false, "", fmt.Errorf("hash calculation failed via Demucs daemon: %w", hashErr)
		}

		trackHash = hashResp.AudioHash
		if d.statsTracker != nil && hashResp.Profile != nil {
			for step, dur := range hashResp.Profile {
				d.statsTracker.RecordPythonStepDuration("demucs", step, dur)
			}
		}
	}

	if d.statsTracker != nil {
		d.statsTracker.RecordStageDuration("hash_check", time.Since(hashStageStart))
	}

	exists, dbErr := d.CheckHashExistsInPostgres(trackHash)
	if dbErr != nil {
		d.LogWarn("[W-%d] Go PostgreSQL check error, trying ingester fallback: %v", id, dbErr)
		checkOut, err := d.runPythonScript("ingester.py", []string{
			"--flac-path", task.FlacPath,
			"--json-path", "dummy",
			"--track-hash", trackHash,
			"--check-hash",
		}, id, "DBCheck", logger.ColorGreen, true)
		if err == nil {
			var checkMeta struct {
				Exists bool `json:"exists"`
			}
			if parseErr := json.Unmarshal([]byte(strings.TrimSpace(checkOut)), &checkMeta); parseErr == nil {
				exists = checkMeta.Exists
			}
		}
	}

	return exists, trackHash, nil
}
