// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// Core Orchestrator Functor & Worker Coordination
package dispatcher

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"flac_analyzer/orchestrator/logger"
	"flac_analyzer/orchestrator/metrics"
	"flac_analyzer/orchestrator/state"

	_ "github.com/lib/pq"
)

// Dispatcher coordinates actor message passing, process pooling, and SHM zero-copy pipeline execution.
type Dispatcher struct {
	configMu               sync.RWMutex
	config                 Config
	db                     *state.DB
	pgDB                   *sql.DB
	taskQueue              chan TaskPayload
	ingestQueue            chan IngestPayload
	ingestWg               sync.WaitGroup
	ingestCtx              context.Context
	cancelIngest           context.CancelFunc
	allocMutex             sync.Mutex
	demucsSemaphore        *DynamicSemaphore
	tensorSemaphore        chan struct{}
	wg                     sync.WaitGroup
	logLevel               LogLevel
	eventLog               EventLogger
	skipDupByHash          bool
	activeInFlightRamBytes uint64
	inFlightMutex          sync.Mutex
	arenaPool              *ShmArenaPool
	statsTracker           *StatsTracker
	daemonPool             *WorkerDaemonPool
	demucsPool             *DemucsDaemonPool
	demucsScheduler        *AdaptiveDemucsScheduler
}

// NewDispatcher initializes all child worker pools, semaphores, and database connections.
// SideEffectFn: NewDispatcher
func NewDispatcher(cfg Config, db *state.DB) *Dispatcher {
	var pgConn *sql.DB
	if cfg.DatabaseURL != "" {
		if conn, err := sql.Open("postgres", cfg.DatabaseURL); err == nil {
			conn.SetMaxOpenConns(20)
			conn.SetMaxIdleConns(5)
			conn.SetConnMaxLifetime(5 * time.Minute)
			pgConn = conn
		} else {
			log.Printf("[WARN] [Dispatcher] Failed to open PostgreSQL connection (%v), will fallback", err)
		}
	}

	parentDir := findProjectRoot()
	pythonPath := "python.exe"
	venvPython := filepath.Join(parentDir, ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(venvPython); err == nil {
		pythonPath = venvPython
	} else {
		venvPythonUnix := filepath.Join(parentDir, ".venv", "bin", "python")
		if _, err := os.Stat(venvPythonUnix); err == nil {
			pythonPath = venvPythonUnix
		}
	}
	var envVars []string
	for k, v := range cfg.PythonEnv {
		envVars = append(envVars, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
	}
	daemonCap := cfg.NumWorkers
	if daemonCap <= 0 {
		daemonCap = 2
	}
	if daemonCap > 8 {
		daemonCap = 8
	}
	daemonPool := NewWorkerDaemonPool(daemonCap, pythonPath, parentDir, envVars, func(format string, v ...interface{}) {
		log.Printf(format, v...)
	})

	statsTracker := NewStatsTracker()

	// 常駐 Demucs デーモンプール (最大容量 2) & アダプティブ GPU スケジューラ
	demucsPool := NewDemucsDaemonPool(2, pythonPath, parentDir, envVars, func(format string, v ...interface{}) {
		log.Printf(format, v...)
	})
	demucsScheduler := NewAdaptiveDemucsScheduler(1, 2, 0.50, 4*1024*1024*1024, statsTracker)

	dbTimeout := cfg.DBTimeoutSec
	if dbTimeout <= 0 {
		dbTimeout = 20
	}
	cfg.DBTimeoutSec = dbTimeout

	ingestCtx, cancelIngest := context.WithCancel(context.Background())

	return &Dispatcher{
		config:                 cfg,
		db:                     db,
		pgDB:                   pgConn,
		taskQueue:              make(chan TaskPayload, 1000),
		ingestQueue:            make(chan IngestPayload, 1000),
		ingestCtx:              ingestCtx,
		cancelIngest:           cancelIngest,
		demucsSemaphore:        NewDynamicSemaphore(cfg.DemucsConcurrentLimit),
		tensorSemaphore:        make(chan struct{}, 1),
		logLevel:               cfg.LogLevel,
		eventLog:               cfg.EventLog,
		skipDupByHash:          cfg.SkipDupByHash,
		activeInFlightRamBytes: 0,
		arenaPool:              NewShmArenaPool(cfg.EnableVirtualLock),
		statsTracker:           statsTracker,
		daemonPool:             daemonPool,
		demucsPool:             demucsPool,
		demucsScheduler:        demucsScheduler,
	}
}

func (d *Dispatcher) getLogLevel() LogLevel {
	d.configMu.RLock()
	defer d.configMu.RUnlock()
	return d.logLevel
}

func (d *Dispatcher) LogDebug(format string, v ...interface{}) {
	if d.getLogLevel() <= LevelDebug {
		log.Printf(format, v...)
	}
}

func (d *Dispatcher) LogInfo(format string, v ...interface{}) {
	if d.getLogLevel() <= LevelInfo {
		log.Printf(format, v...)
	}
}

func (d *Dispatcher) LogWarn(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	if d.getLogLevel() <= LevelWarn {
		log.Printf("%s[WARN] %s%s\n", logger.ColorYellow, msg, logger.ColorReset)
	}
	if d.eventLog != nil {
		_ = d.eventLog.Warning(1001, msg)
	}
}

func (d *Dispatcher) LogError(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	if d.getLogLevel() <= LevelError {
		log.Printf("%s[ERROR] %s%s\n", logger.ColorRed, msg, logger.ColorReset)
	}
	if d.eventLog != nil {
		_ = d.eventLog.Error(1002, msg)
	}
	metrics.AnalyzerErrorsTotal.Inc()
}

// Start spawns background metric collectors, worker pools, and task consumers.
// SideEffectFn: Start
func (d *Dispatcher) Start() {
	if d.statsTracker != nil {
		d.statsTracker.StartSystemResourceCollector(context.Background(), d.config.QueueDir, 5*time.Second)
	}
	if d.demucsScheduler != nil {
		d.demucsScheduler.StartAdaptiveLoop(context.Background(), 2*time.Second)
	}
	if d.daemonPool != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_ = d.daemonPool.Prewarm(ctx, 2)
		}()
	}
	if d.demucsPool != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_ = d.demucsPool.Prewarm(ctx, 1)
		}()
	}

	// 独立した非同期 IngestWorker を起動いたしますわ（Compute と IO の完全分離）
	d.ingestWg.Add(1)
	go d.ingestWorker()

	for i := 1; i <= d.config.NumWorkers; i++ {
		d.wg.Add(1)
		go d.worker(i)
	}
}

// Enqueue puts a new TaskPayload into the dispatcher queue.
func (d *Dispatcher) Enqueue(task TaskPayload) error {
	metrics.AnalyzerQueueLength.Inc()
	d.taskQueue <- task
	if d.statsTracker != nil {
		d.statsTracker.SetQueueLength(len(d.taskQueue))
	}
	return nil
}

// RegisterFileTracks registers the number of tracks expected for a FLAC file to measure overall file duration.
func (d *Dispatcher) RegisterFileTracks(filePath string, totalTracks int) {
	if d.statsTracker != nil {
		d.statsTracker.RegisterFileTracks(filePath, totalTracks)
	}
}

// GetStatsTracker returns the internal stats tracker instance.
func (d *Dispatcher) GetStatsTracker() *StatsTracker {
	return d.statsTracker
}

// Stop initiates a clean 3-phase shutdown sequence.
// SideEffectFn: Stop
func (d *Dispatcher) Stop() {
	// Phase 1: 解析ワーカーキューを閉じ、全解析ワーカーの完了を待機
	close(d.taskQueue)
	d.wg.Wait()

	// Phase 2: Ingest キューを閉じ、非同期 IngestWorker の完了を待機
	close(d.ingestQueue)
	d.ingestWg.Wait()
	d.cancelIngest()

	// Phase 3: 全ての後続処理完了後にプールおよび DB コネクションを破棄
	if d.daemonPool != nil {
		_ = d.daemonPool.Close()
	}
	if d.demucsPool != nil {
		_ = d.demucsPool.Close()
	}
	if d.arenaPool != nil {
		d.arenaPool.Close()
	}
	if d.pgDB != nil {
		_ = d.pgDB.Close()
	}
}

func (d *Dispatcher) failTask(task TaskPayload, errMsg string) {
	d.LogError("[Dispatcher] Task Failed: %s (Track %d) -> %s", task.FlacPath, task.TrackNumber, errMsg)
	d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusFailed, errMsg)
	metrics.AnalyzerTasksTotal.WithLabelValues("error").Inc()
}

func (d *Dispatcher) worker(id int) {
	defer d.wg.Done()

	for task := range d.taskQueue {
		// Gatekeeper Pre-flight Decision (CUE/FLAC Demucs RAM Estimation)
		gatekeeperStartTime := time.Now()
		for {
			isGo, waitDur := d.EvaluateGoNoGo(id, task)
			if !isGo {
				time.Sleep(waitDur)
				continue
			}
			break
		}
		if d.statsTracker != nil {
			d.statsTracker.RecordGatekeeperWait(time.Since(gatekeeperStartTime))
		}

		// Execute full sequential DSP pipeline step
		d.executeTaskPipeline(id, task)
	}
}
