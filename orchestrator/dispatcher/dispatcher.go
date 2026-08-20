package dispatcher

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"flac_analyzer/orchestrator/metrics"
	"flac_analyzer/orchestrator/state"
	"flac_analyzer/orchestrator/sysinfo"

	_ "github.com/lib/pq"
)

type LogLevel int

const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelWarn
	LevelError
)

func ParseLogLevel(s string) LogLevel {
	switch strings.ToLower(s) {
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

func (l LogLevel) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "unknown"
	}
}

func (l LogLevel) MarshalJSON() ([]byte, error) {
	return json.Marshal(l.String())
}

type EventLogger interface {
	Info(eid uint32, msg string) error
	Warning(eid uint32, msg string) error
	Error(eid uint32, msg string) error
}

type TaskPayload struct {
	FlacPath     string `json:"flacPath"`
	FileSize     int64  `json:"fileSize"`
	TargetScript string `json:"targetScript"`
	TrackNumber  int    `json:"trackNumber"`
	StartSample  int64  `json:"startSample"`
	EndSample    int64  `json:"endSample"`
	Title        string `json:"title"`
	Artist       string `json:"artist"`
	Album        string `json:"album"`
	AlbumArtist  string `json:"albumArtist"`
	Force        bool   `json:"force"`
}

type Config struct {
	NumWorkers            int
	MaxRamRatio           float64
	EstimatedWorkerRamGB  float64
	MinAvailRamGB         float64
	MinAvailDiskGB        float64
	DemucsConcurrentLimit        int
	DemucsDaemonCapacity         int
	DemucsDualGpuUtilThreshold   float64
	DemucsDualMinVramGB          float64
	ShmAllocationDelaySec        int
	ShmExpansionRatio            float64
	ShmRetryCount                int
	ShmRetryDelaySec             int
	QueueDir                     string
	DatabaseURL                  string
	PythonEnv                    map[string]string
	LogLevel                     LogLevel
	EventLog                     EventLogger
	SkipDupByHash                bool
	EnableVirtualLock            bool
	GatekeeperRetryDelaySec      int
	ConfigWatchIntervalSec       int
	EnableDlqRetry               bool
	DlqRetryIntervalSec          int
	MaxGpuUtilizationRatio       float64
	MinAvailVramGB               float64
	EstimatedDemucsVramGB        float64
	EnableGpuThrottle            bool
	DBTimeoutSec                 int
}


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

const (
	ColorReset        = "\033[0m"
	ColorLevel1Dim    = "\033[2;37m" // Dim Gray (Identity / Check) - 最暗
	ColorLevel2Blue   = "\033[34m"   // Blue (SHM Allocation) - 暗め
	ColorLevel3Purple = "\033[35m"   // Magenta (Demucs Isolation) - 中暗
	ColorLevel4Cyan   = "\033[36m"   // Cyan (Feature Extract) - 中明
	ColorLevel5Green  = "\033[32m"   // Green (DB Ingestion) - 明
	ColorLevel6Bright = "\033[1;97m" // Bold Bright White (Tag & Complete) - 最光
	ColorWarn         = "\033[1;33m" // Bold Yellow (WARN専用)
	ColorError        = "\033[1;31m" // Bold Red (ERROR専用)

	// Legacy alias compatibility
	ColorRed    = "\033[1;31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[1;33m"
	ColorBlue   = "\033[34m"
	ColorCyan   = "\033[36m"
	ColorPurple = "\033[35m"
)

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

// CheckHashExistsInPostgres queries PostgreSQL directly in Go to check if audio_hash already exists.
func (d *Dispatcher) CheckHashExistsInPostgres(trackHash string) (bool, error) {
	if d.pgDB == nil || trackHash == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var exists int
	err := d.pgDB.QueryRowContext(ctx, "SELECT 1 FROM raw.library_flac WHERE audio_hash = $1 LIMIT 1", trackHash).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return exists == 1, nil
}

// GetConfig returns a thread-safe copy of the current configuration.
func (d *Dispatcher) GetConfig() Config {
	d.configMu.RLock()
	defer d.configMu.RUnlock()

	copiedEnv := make(map[string]string, len(d.config.PythonEnv))
	for k, v := range d.config.PythonEnv {
		copiedEnv[k] = v
	}
	cfg := d.config
	cfg.PythonEnv = copiedEnv
	return cfg
}

// UpdateConfig dynamically updates the configuration at runtime, adjusting semaphores and logging.
// It returns a diff map describing what was changed.
func (d *Dispatcher) UpdateConfig(newCfg Config) map[string]string {
	d.configMu.Lock()
	defer d.configMu.Unlock()

	oldCfg := d.config
	diff := make(map[string]string)

	if oldCfg.DemucsConcurrentLimit != newCfg.DemucsConcurrentLimit {
		diff["demucs_concurrent_limit"] = fmt.Sprintf("%d -> %d", oldCfg.DemucsConcurrentLimit, newCfg.DemucsConcurrentLimit)
		d.demucsSemaphore.SetLimit(newCfg.DemucsConcurrentLimit)
	}
	if oldCfg.LogLevel != newCfg.LogLevel {
		diff["log_level"] = fmt.Sprintf("%s -> %s", oldCfg.LogLevel, newCfg.LogLevel)
		d.logLevel = newCfg.LogLevel
	}
	if oldCfg.SkipDupByHash != newCfg.SkipDupByHash {
		diff["skip_dup_by_hash"] = fmt.Sprintf("%v -> %v", oldCfg.SkipDupByHash, newCfg.SkipDupByHash)
		d.skipDupByHash = newCfg.SkipDupByHash
	}
	if oldCfg.MaxRamRatio != newCfg.MaxRamRatio {
		diff["max_ram_ratio"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.MaxRamRatio, newCfg.MaxRamRatio)
	}
	if oldCfg.EstimatedWorkerRamGB != newCfg.EstimatedWorkerRamGB {
		diff["estimated_worker_ram_gb"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.EstimatedWorkerRamGB, newCfg.EstimatedWorkerRamGB)
	}
	if oldCfg.MinAvailRamGB != newCfg.MinAvailRamGB {
		diff["min_avail_ram_gb"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.MinAvailRamGB, newCfg.MinAvailRamGB)
	}
	if oldCfg.ShmAllocationDelaySec != newCfg.ShmAllocationDelaySec {
		diff["shm_allocation_delay_sec"] = fmt.Sprintf("%d -> %d", oldCfg.ShmAllocationDelaySec, newCfg.ShmAllocationDelaySec)
	}
	if oldCfg.ShmExpansionRatio != newCfg.ShmExpansionRatio {
		diff["shm_expansion_ratio"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.ShmExpansionRatio, newCfg.ShmExpansionRatio)
	}
	if oldCfg.ShmRetryCount != newCfg.ShmRetryCount {
		diff["shm_retry_count"] = fmt.Sprintf("%d -> %d", oldCfg.ShmRetryCount, newCfg.ShmRetryCount)
	}
	if oldCfg.ShmRetryDelaySec != newCfg.ShmRetryDelaySec {
		diff["shm_retry_delay_sec"] = fmt.Sprintf("%d -> %d", oldCfg.ShmRetryDelaySec, newCfg.ShmRetryDelaySec)
	}
	if oldCfg.QueueDir != newCfg.QueueDir {
		diff["queue_dir"] = fmt.Sprintf("%s -> %s", oldCfg.QueueDir, newCfg.QueueDir)
	}
	if oldCfg.GatekeeperRetryDelaySec != newCfg.GatekeeperRetryDelaySec {
		diff["gatekeeper_retry_delay_sec"] = fmt.Sprintf("%d -> %d", oldCfg.GatekeeperRetryDelaySec, newCfg.GatekeeperRetryDelaySec)
	}
	if oldCfg.ConfigWatchIntervalSec != newCfg.ConfigWatchIntervalSec {
		diff["config_watch_interval_sec"] = fmt.Sprintf("%d -> %d", oldCfg.ConfigWatchIntervalSec, newCfg.ConfigWatchIntervalSec)
	}
	if oldCfg.EnableDlqRetry != newCfg.EnableDlqRetry {
		diff["enable_dlq_retry"] = fmt.Sprintf("%v -> %v", oldCfg.EnableDlqRetry, newCfg.EnableDlqRetry)
	}
	if oldCfg.DlqRetryIntervalSec != newCfg.DlqRetryIntervalSec {
		diff["dlq_retry_interval_sec"] = fmt.Sprintf("%d -> %d", oldCfg.DlqRetryIntervalSec, newCfg.DlqRetryIntervalSec)
	}
	if oldCfg.DemucsDaemonCapacity != newCfg.DemucsDaemonCapacity {
		diff["demucs_daemon_capacity"] = fmt.Sprintf("%d -> %d", oldCfg.DemucsDaemonCapacity, newCfg.DemucsDaemonCapacity)
	}
	if oldCfg.DemucsDualGpuUtilThreshold != newCfg.DemucsDualGpuUtilThreshold {
		diff["demucs_dual_gpu_util_threshold"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.DemucsDualGpuUtilThreshold, newCfg.DemucsDualGpuUtilThreshold)
	}
	if oldCfg.DemucsDualMinVramGB != newCfg.DemucsDualMinVramGB {
		diff["demucs_dual_min_vram_gb"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.DemucsDualMinVramGB, newCfg.DemucsDualMinVramGB)
	}
	if oldCfg.MaxGpuUtilizationRatio != newCfg.MaxGpuUtilizationRatio {
		diff["max_gpu_utilization_ratio"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.MaxGpuUtilizationRatio, newCfg.MaxGpuUtilizationRatio)
	}
	if oldCfg.MinAvailVramGB != newCfg.MinAvailVramGB {
		diff["min_avail_vram_gb"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.MinAvailVramGB, newCfg.MinAvailVramGB)
	}
	if oldCfg.EstimatedDemucsVramGB != newCfg.EstimatedDemucsVramGB {
		diff["estimated_demucs_vram_gb"] = fmt.Sprintf("%.2f -> %.2f", oldCfg.EstimatedDemucsVramGB, newCfg.EstimatedDemucsVramGB)
	}
	if oldCfg.EnableGpuThrottle != newCfg.EnableGpuThrottle {
		diff["enable_gpu_throttle"] = fmt.Sprintf("%v -> %v", oldCfg.EnableGpuThrottle, newCfg.EnableGpuThrottle)
	}
	if oldCfg.DBTimeoutSec != newCfg.DBTimeoutSec {
		diff["db_timeout_sec"] = fmt.Sprintf("%d -> %d", oldCfg.DBTimeoutSec, newCfg.DBTimeoutSec)
	}

	d.config = newCfg
	return diff
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
		log.Printf("%s[WARN] %s%s\n", ColorYellow, msg, ColorReset)
	}
	if d.eventLog != nil {
		_ = d.eventLog.Warning(1001, msg)
	}
}

func (d *Dispatcher) LogError(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	if d.getLogLevel() <= LevelError {
		log.Printf("%s[ERROR] %s%s\n", ColorRed, msg, ColorReset)
	}
	if d.eventLog != nil {
		_ = d.eventLog.Error(1002, msg)
	}
	metrics.AnalyzerErrorsTotal.Inc()
}

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

func (d *Dispatcher) Stop() {
	// Phase 1: 解析ワーカーキューを閉じ、全解析ワーカーの完了を待機
	close(d.taskQueue)
	d.wg.Wait()

	// Phase 2: Ingest キューを閉じ、非同期 IngestWorker の完了を待機 (Advisory 1: 厳格な2段階シャットダウン)
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

func (d *Dispatcher) streamColoredLog(pipe io.ReadCloser, workerID int, role string, color string) {
	scanner := bufio.NewScanner(pipe)
	prefix := fmt.Sprintf("%s[W-%d] [%s] ", color, workerID, role)
	for scanner.Scan() {
		line := scanner.Text()

		// ONNX Runtime の内部 Fallback 警告などのノイズは通常ログではサイレント（DEBUG レベルのみ）にしますわ
		if strings.Contains(line, "running in Fallback mode") || strings.Contains(line, "onnxruntime::cuda::Conv") {
			if d.logLevel <= LevelDebug {
				d.LogDebug("[W-%d] [%s] %s", workerID, role, line)
			}
			continue
		}

		isError := strings.Contains(line, "[ERROR]") || strings.Contains(strings.ToLower(line), "error") || strings.Contains(strings.ToLower(line), "traceback")
		if isError {
			msg := fmt.Sprintf("[W-%d] [%s] %s", workerID, role, line)
			fmt.Printf("%s%s%s\n", ColorRed, msg, ColorReset)
			if d.eventLog != nil {
				_ = d.eventLog.Error(1003, msg)
			}
			metrics.AnalyzerErrorsTotal.Inc()
		} else {
			if d.logLevel <= LevelInfo {
				fmt.Printf("%s%s%s\n", prefix, line, ColorReset)
			}
		}
	}
}

func findProjectRoot() string {
	if exePath, err := os.Executable(); err == nil {
		dir := filepath.Dir(exePath)
		for i := 0; i < 4; i++ {
			if _, err := os.Stat(filepath.Join(dir, "config.toml")); err == nil {
				return dir
			}
			if _, err := os.Stat(filepath.Join(dir, "worker_cue.py")); err == nil {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if wd, err := os.Getwd(); err == nil {
		dir := wd
		for i := 0; i < 4; i++ {
			if _, err := os.Stat(filepath.Join(dir, "config.toml")); err == nil {
				return dir
			}
			if _, err := os.Stat(filepath.Join(dir, "worker_cue.py")); err == nil {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		return wd
	}
	return "."
}

func (d *Dispatcher) runPythonScript(scriptName string, args []string, workerID int, role, color string, captureStdout bool) (string, error) {
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

	scriptPath := filepath.Join(parentDir, scriptName)
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		zigScript := filepath.Join(parentDir, "zig", scriptName)
		if _, errZig := os.Stat(zigScript); errZig == nil {
			scriptPath = zigScript
		}
	}
	cmdArgs := append([]string{scriptPath}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, pythonPath, cmdArgs...)
	cmd.Dir = parentDir

	currentCfg := d.GetConfig()
	var envVars []string
	for k, v := range currentCfg.PythonEnv {
		envVars = append(envVars, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
	}
	cmd.Env = append(os.Environ(), envVars...)

	var outBuf bytes.Buffer
	if captureStdout {
		cmd.Stdout = &outBuf
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to get stderr pipe for %s: %w", role, err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start %s: %w", role, err)
	}

	if cmd.Process != nil {
		if err := AssignPidToJob(cmd.Process.Pid); err != nil {
			d.LogWarn("[W-%d] AssignPidToJob note: %v", workerID, err)
		}
	}

	d.streamColoredLog(stderrPipe, workerID, role, color)

	err = cmd.Wait()
	if err != nil {
		return "", fmt.Errorf("%s failed: %w", role, err)
	}

	return outBuf.String(), nil
}

func (d *Dispatcher) failTask(task TaskPayload, errMsg string) {
	d.LogError("[Dispatcher] Task Failed: %s (Track %d) -> %s", task.FlacPath, task.TrackNumber, errMsg)
	d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusFailed, errMsg)
	metrics.AnalyzerTasksTotal.WithLabelValues("error").Inc()
	metrics.AnalyzerActiveWorkers.Dec()
}

func cleanupCache(trackHash string) {
	if trackHash == "" {
		return
	}
	cacheDir := filepath.Join(os.TempDir(), "flac_analyzer_cache", trackHash)
	if _, err := os.Stat(cacheDir); err == nil {
		_ = os.RemoveAll(cacheDir)
	}
}

// cleanupQueueFiles removes intermediate JSON files generated for a task if it fails or aborts.
func cleanupQueueFiles(queueDir, trackHash, baseName string) {
	if queueDir == "" || trackHash == "" {
		return
	}
	outName := fmt.Sprintf("%s_%s.json", trackHash, baseName)
	outNameEss := fmt.Sprintf("%s_%s_essentia.json", trackHash, baseName)
	outNameTensor := fmt.Sprintf("%s_%s_tensor.json", trackHash, baseName)

	for _, name := range []string{outName, outNameEss, outNameTensor} {
		p := filepath.Join(queueDir, name)
		if _, err := os.Stat(p); err == nil {
			_ = os.Remove(p)
		}
	}
}

// PurgeOrphanedQueueAndCacheFiles cleans up old cache directories and stale intermediate JSON files.
func PurgeOrphanedQueueAndCacheFiles(queueDir string, maxAge time.Duration) {
	// 1. Purge Temp cache directory
	cacheRoot := filepath.Join(os.TempDir(), "flac_analyzer_cache")
	if entries, err := os.ReadDir(cacheRoot); err == nil {
		now := time.Now()
		for _, entry := range entries {
			if entry.IsDir() {
				dirPath := filepath.Join(cacheRoot, entry.Name())
				if info, err := entry.Info(); err == nil {
					if now.Sub(info.ModTime()) > maxAge {
						_ = os.RemoveAll(dirPath)
					}
				}
			}
		}
	}

	// 2. Purge stale queue JSON files
	if queueDir != "" {
		if entries, err := os.ReadDir(queueDir); err == nil {
			now := time.Now()
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
					filePath := filepath.Join(queueDir, entry.Name())
					if info, err := entry.Info(); err == nil {
						if now.Sub(info.ModTime()) > maxAge {
							_ = os.Remove(filePath)
						}
					}
				}
			}
		}
	}
}

// GatekeeperInput encapsulates all parameters required for pure pre-flight dispatch decisions.
type GatekeeperInput struct {
	AvailPhys         uint64
	InFlightRam       uint64
	EstimatedTaskRam  uint64
	MinAvailRam       uint64
	MemoryLoad        uint32
	AvailDisk         uint64
	MinAvailDisk      uint64
	GpuUtilization    float64
	AvailVram         uint64
	MinAvailVram      uint64
	EstimatedTaskVram uint64
	MaxGpuUtilization float64
	EnableGpuThrottle bool
	RetryDelay        time.Duration
}

// GatekeeperDecision encapsulates the decision result of EvaluateGoNoGoPure.
type GatekeeperDecision struct {
	IsGo                bool
	WaitDuration        time.Duration
	Reason              string
	EstimatedRamBytes   uint64
	EffectiveAvailBytes uint64
	RequiredBytes       uint64
	MemoryLoad          uint32
	AvailDiskBytes      uint64
	MinAvailDiskBytes   uint64
	GpuUtilization      float64
	AvailVramBytes      uint64
	RequiredVramBytes   uint64
	IsGpuBlock          bool
}

// EvaluateGoNoGoPure evaluates whether a task can be dispatched without side-effects (Pure Domain Morphism).
func EvaluateGoNoGoPure(in GatekeeperInput) GatekeeperDecision {
	retryDelay := in.RetryDelay
	if retryDelay <= 0 {
		retryDelay = 20 * time.Second
	}

	// 1. Disk Space Check (Storage Defense)
	if in.MinAvailDisk > 0 && in.AvailDisk < in.MinAvailDisk {
		return GatekeeperDecision{
			IsGo:              false,
			WaitDuration:      retryDelay,
			Reason:            fmt.Sprintf("Available Disk Space (%.2f GB) < Required MinAvailDisk (%.2f GB)", float64(in.AvailDisk)/(1024*1024*1024), float64(in.MinAvailDisk)/(1024*1024*1024)),
			EstimatedRamBytes: in.EstimatedTaskRam,
			MemoryLoad:        in.MemoryLoad,
			AvailDiskBytes:    in.AvailDisk,
			MinAvailDiskBytes: in.MinAvailDisk,
		}
	}

	// 2. RAM Capacity & MemoryLoad Check
	var effectiveAvailBytes uint64
	if in.AvailPhys > in.InFlightRam {
		effectiveAvailBytes = in.AvailPhys - in.InFlightRam
	} else {
		effectiveAvailBytes = 0
	}

	requiredBytes := in.EstimatedTaskRam + in.MinAvailRam
	if effectiveAvailBytes < requiredBytes {
		return GatekeeperDecision{
			IsGo:                false,
			WaitDuration:        retryDelay,
			Reason:              fmt.Sprintf("Effective Avail RAM (%d MB = Avail %d MB - InFlight %d MB) < Required (%d MB = Task %d MB + MinAvail %d MB)", effectiveAvailBytes/1024/1024, in.AvailPhys/1024/1024, in.InFlightRam/1024/1024, requiredBytes/1024/1024, in.EstimatedTaskRam/1024/1024, in.MinAvailRam/1024/1024),
			EstimatedRamBytes:   in.EstimatedTaskRam,
			EffectiveAvailBytes: effectiveAvailBytes,
			RequiredBytes:       requiredBytes,
			MemoryLoad:          in.MemoryLoad,
			AvailDiskBytes:      in.AvailDisk,
			MinAvailDiskBytes:   in.MinAvailDisk,
		}
	}

	if in.MemoryLoad >= 90 {
		return GatekeeperDecision{
			IsGo:                false,
			WaitDuration:        retryDelay,
			Reason:              fmt.Sprintf("System MemoryLoad too high (%d%% >= 90%%)", in.MemoryLoad),
			EstimatedRamBytes:   in.EstimatedTaskRam,
			EffectiveAvailBytes: effectiveAvailBytes,
			RequiredBytes:       requiredBytes,
			MemoryLoad:          in.MemoryLoad,
			AvailDiskBytes:      in.AvailDisk,
			MinAvailDiskBytes:   in.MinAvailDisk,
		}
	}

	// 3. GPU & VRAM Throttle Defense (Only evaluated if enabled)
	if in.EnableGpuThrottle {
		// GPU 負荷率判定 (例: Max 85%)
		if in.MaxGpuUtilization > 0 && in.GpuUtilization >= (in.MaxGpuUtilization*100) {
			return GatekeeperDecision{
				IsGo:                false,
				WaitDuration:        retryDelay,
				Reason:              fmt.Sprintf("GPU Utilization too high (%.1f%% >= %.1f%% threshold)", in.GpuUtilization, in.MaxGpuUtilization*100),
				EstimatedRamBytes:   in.EstimatedTaskRam,
				EffectiveAvailBytes: effectiveAvailBytes,
				RequiredBytes:       requiredBytes,
				MemoryLoad:          in.MemoryLoad,
				AvailDiskBytes:      in.AvailDisk,
				MinAvailDiskBytes:   in.MinAvailDisk,
				GpuUtilization:      in.GpuUtilization,
				AvailVramBytes:      in.AvailVram,
				IsGpuBlock:          true,
			}
		}

		// VRAM 空き容量判定
		if in.MinAvailVram > 0 && in.AvailVram != math.MaxUint64 {
			requiredVram := in.EstimatedTaskVram + in.MinAvailVram
			if in.AvailVram < requiredVram {
				return GatekeeperDecision{
					IsGo:                false,
					WaitDuration:        retryDelay,
					Reason:              fmt.Sprintf("Available VRAM (%.2f GB) < Required (%.2f GB = Task %.2f GB + MinAvail %.2f GB)", float64(in.AvailVram)/(1024*1024*1024), float64(requiredVram)/(1024*1024*1024), float64(in.EstimatedTaskVram)/(1024*1024*1024), float64(in.MinAvailVram)/(1024*1024*1024)),
					EstimatedRamBytes:   in.EstimatedTaskRam,
					EffectiveAvailBytes: effectiveAvailBytes,
					RequiredBytes:       requiredBytes,
					MemoryLoad:          in.MemoryLoad,
					AvailDiskBytes:      in.AvailDisk,
					MinAvailDiskBytes:   in.MinAvailDisk,
					GpuUtilization:      in.GpuUtilization,
					AvailVramBytes:      in.AvailVram,
					RequiredVramBytes:   requiredVram,
					IsGpuBlock:          true,
				}
			}
		}
	}

	return GatekeeperDecision{
		IsGo:                true,
		WaitDuration:        0,
		Reason:              "Approved",
		EstimatedRamBytes:   in.EstimatedTaskRam,
		EffectiveAvailBytes: effectiveAvailBytes,
		RequiredBytes:       requiredBytes,
		MemoryLoad:          in.MemoryLoad,
		AvailDiskBytes:      in.AvailDisk,
		MinAvailDiskBytes:   in.MinAvailDisk,
		GpuUtilization:      in.GpuUtilization,
		AvailVramBytes:      in.AvailVram,
	}
}

// EvaluateGoNoGo queries live system memory, disk, and GPU status and delegates the preflight decision to EvaluateGoNoGoPure.
func (d *Dispatcher) EvaluateGoNoGo(workerID int, task TaskPayload) (bool, time.Duration) {
	estimatedRam := EstimateDemucsTotalRamBytes(task)
	memInfo, err := sysinfo.GetMemoryInfo()
	if err != nil || memInfo == nil {
		return true, 0
	}

	d.inFlightMutex.Lock()
	inFlight := d.activeInFlightRamBytes
	d.inFlightMutex.Unlock()

	currentCfg := d.GetConfig()
	minAvailBytes := uint64(currentCfg.MinAvailRamGB * 1024 * 1024 * 1024)
	minAvailDiskBytes := uint64(currentCfg.MinAvailDiskGB * 1024 * 1024 * 1024)
	minAvailVramBytes := uint64(currentCfg.MinAvailVramGB * 1024 * 1024 * 1024)
	estimatedVramBytes := uint64(currentCfg.EstimatedDemucsVramGB * 1024 * 1024 * 1024)
	if estimatedVramBytes == 0 {
		estimatedVramBytes = uint64(1.0 * 1024 * 1024 * 1024)
	}

	retryDelay := time.Duration(currentCfg.GatekeeperRetryDelaySec) * time.Second
	if retryDelay <= 0 {
		retryDelay = 20 * time.Second
	}

	// Disk space check: inspect queue_dir, temp dir, and source file dir
	var availDisk uint64 = math.MaxUint64
	if minAvailDiskBytes > 0 {
		checkPaths := []string{currentCfg.QueueDir, os.TempDir()}
		if task.FlacPath != "" {
			checkPaths = append(checkPaths, filepath.Dir(task.FlacPath))
		}
		for _, p := range checkPaths {
			if p == "" {
				continue
			}
			if dInfo, dErr := sysinfo.GetDiskFreeSpace(p); dErr == nil && dInfo != nil {
				if dInfo.FreeBytesAvailable < availDisk {
					availDisk = dInfo.FreeBytesAvailable
				}
			}
		}
	}

	// GPU metrics lookup
	gpuMetrics := sysinfo.GetLatestGpuMetrics()
	var gpuUtil float64 = 0.0
	var availVram uint64 = math.MaxUint64
	if gpuMetrics != nil {
		gpuUtil = gpuMetrics.UtilizationPercent
		availVram = gpuMetrics.AvailableVramBytes
	}

	input := GatekeeperInput{
		AvailPhys:         memInfo.AvailPhys,
		InFlightRam:       inFlight,
		EstimatedTaskRam:  estimatedRam,
		MinAvailRam:       minAvailBytes,
		MemoryLoad:        memInfo.MemoryLoad,
		AvailDisk:         availDisk,
		MinAvailDisk:      minAvailDiskBytes,
		GpuUtilization:    gpuUtil,
		AvailVram:         availVram,
		MinAvailVram:      minAvailVramBytes,
		EstimatedTaskVram: estimatedVramBytes,
		MaxGpuUtilization: currentCfg.MaxGpuUtilizationRatio,
		EnableGpuThrottle: currentCfg.EnableGpuThrottle,
		RetryDelay:        retryDelay,
	}

	decision := EvaluateGoNoGoPure(input)
	if !decision.IsGo {
		if decision.IsGpuBlock && d.statsTracker != nil {
			d.statsTracker.RecordGpuWait(decision.WaitDuration)
		}
		d.LogWarn("[W-%d] [Gatekeeper: NOGO] %s. Delaying dispatch for %v...", workerID, decision.Reason, decision.WaitDuration)
		return false, decision.WaitDuration
	}

	if minAvailDiskBytes > 0 {
		d.LogInfo("[W-%d] [Gatekeeper: GO] Dispatch Approved (Task RAM: %d MB, Effective Avail RAM: %d MB [Avail: %d MB, InFlight: %d MB], GPU Util: %.1f%%, Min Avail Disk: %.2f GB)",
			workerID, decision.EstimatedRamBytes/1024/1024, decision.EffectiveAvailBytes/1024/1024, memInfo.AvailPhys/1024/1024, inFlight/1024/1024, gpuUtil, float64(availDisk)/(1024*1024*1024))
	} else {
		d.LogInfo("[W-%d] [Gatekeeper: GO] Dispatch Approved (Task RAM: %d MB, Effective Avail RAM: %d MB [Avail: %d MB, InFlight: %d MB], GPU Util: %.1f%%)",
			workerID, decision.EstimatedRamBytes/1024/1024, decision.EffectiveAvailBytes/1024/1024, memInfo.AvailPhys/1024/1024, inFlight/1024/1024, gpuUtil)
	}
	return true, 0
}

// TriggerDlqRetry executes retry_ingest.py to process any queued failed payloads in send_failed.db.
func (d *Dispatcher) TriggerDlqRetry(ctx context.Context) error {
	d.LogInfo("[DLQ] Triggering retry_ingest.py execution...")
	out, err := d.runPythonScript("retry_ingest.py", nil, 0, "DLQRetry", ColorYellow, true)
	if err != nil {
		d.LogWarn("[DLQ] retry_ingest.py execution note: %v (output: %s)", err, out)
		return err
	}
	cleanOut := strings.TrimSpace(out)
	if cleanOut != "" {
		d.LogInfo("[DLQ] retry_ingest.py output: %s", cleanOut)
	}
	return nil
}

// StartDlqRetryScheduler starts background periodic execution of retry_ingest.py according to config.
func (d *Dispatcher) StartDlqRetryScheduler(ctx context.Context) {
	go func() {
		cfg := d.GetConfig()
		if !cfg.EnableDlqRetry {
			d.LogInfo("[DLQ] Auto retry disabled via config (enable_dlq_retry = false)")
			return
		}

		// 1. Run immediately on startup in a separate goroutine
		go func() {
			_ = d.TriggerDlqRetry(ctx)
		}()

		// 2. Periodic ticker if interval > 0
		intervalSec := cfg.DlqRetryIntervalSec
		if intervalSec <= 0 {
			d.LogInfo("[DLQ] Periodic retry disabled (dlq_retry_interval_sec <= 0)")
			return
		}

		ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				currentCfg := d.GetConfig()
				if !currentCfg.EnableDlqRetry {
					continue
				}
				_ = d.TriggerDlqRetry(ctx)
			}
		}
	}()
}

func (d *Dispatcher) worker(id int) {
	defer d.wg.Done()
	
	stems := []string{"mix", "bass", "drums", "vocals", "other", "guitar", "piano"}

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

		func(task TaskPayload) {
			taskStartTime := time.Now()
			taskSuccess := false
			defer func() {
				if d.statsTracker != nil {
					d.statsTracker.RecordTaskCompletion(task.FlacPath, time.Since(taskStartTime), taskSuccess)
					d.statsTracker.SetQueueLength(len(d.taskQueue))
				}
			}()

			metrics.AnalyzerQueueLength.Dec()
			metrics.AnalyzerActiveWorkers.Inc()
			
			estimatedRam := EstimateDemucsTotalRamBytes(task)
			d.inFlightMutex.Lock()
			d.activeInFlightRamBytes += estimatedRam
			d.inFlightMutex.Unlock()

			defer func() {
				d.inFlightMutex.Lock()
				if d.activeInFlightRamBytes >= estimatedRam {
					d.activeInFlightRamBytes -= estimatedRam
				} else {
					d.activeInFlightRamBytes = 0
				}
				d.inFlightMutex.Unlock()
			}()
			
			d.LogInfo("[W-%d] [IO Monad] Starting processing: %s (Track %d)", id, task.FlacPath, task.TrackNumber)
			d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusRunning, "")
			
			var trackHash string
			var endSampleParam int64

			// タスク完了時（成功・失敗・中断問わず）に一時キャッシュディレクトリを自動削除しますわ
			defer func() {
				cleanupCache(trackHash)
			}()
			
			if d.GetConfig().SkipDupByHash {
				hashStageStart := time.Now()
				isSingleTrack := task.StartSample == 0 && (task.EndSample <= 0 || task.EndSample == task.FileSize)
				var fastMD5 string
				var fastMD5Err error
				if isSingleTrack {
					fastMD5, fastMD5Err = ExtractFlacStreaminfoMD5(task.FlacPath)
				}

				if isSingleTrack && fastMD5Err == nil && fastMD5 != "" {
					trackHash = fastMD5
					d.LogDebug("[W-%d] [FastPath] Extracted STREAMINFO MD5 directly: %s", id, trackHash)
				} else {
					// 2.1 Calculate MD5 hash only via DemucsDaemonPool (Zero process spawn!)
					endSampleParam = task.EndSample
					if endSampleParam == 0 {
						endSampleParam = -1
					}
					ctxHash, cancelHash := context.WithTimeout(context.Background(), 120*time.Second)
					demucsClient, dErr := d.demucsPool.Acquire(ctxHash)
					if dErr != nil {
						cancelHash()
						d.failTask(task, fmt.Sprintf("Failed to acquire Demucs daemon for hash check: %v", dErr))
						return
					}
					hashResp, hashErr := demucsClient.CheckHash(ctxHash, DemucsCheckHashPayload{
						FlacPath:    task.FlacPath,
						StartSample: task.StartSample,
						EndSample:   endSampleParam,
					})
					cancelHash()
					d.demucsPool.Release(demucsClient)

					if hashErr != nil {
						d.failTask(task, fmt.Sprintf("Hash calculation failed via Demucs daemon: %v", hashErr))
						return
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

				// 2.2 Query PostgreSQL directly via Go (1ms check) with ingester fallback
				exists, dbErr := d.CheckHashExistsInPostgres(trackHash)
				if dbErr != nil {
					d.LogWarn("[W-%d] Go PostgreSQL check error, trying ingester fallback: %v", id, dbErr)
					checkOut, err := d.runPythonScript("ingester.py", []string{
						"--flac-path", task.FlacPath,
						"--json-path", "dummy",
						"--track-hash", trackHash,
						"--check-hash",
					}, id, "DBCheck", ColorGreen, true)
					if err == nil {
						cleanCheckOut := strings.TrimSpace(checkOut)
						var checkMeta struct {
							Exists bool `json:"exists"`
						}
						if parseErr := json.Unmarshal([]byte(cleanCheckOut), &checkMeta); parseErr == nil {
							exists = checkMeta.Exists
						}
					}
				}

				if exists {
					d.LogInfo("[W-%d] [IO Monad] Skip processing: Hash %s already exists in PostgreSQL", id, trackHash)
					d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusCompleted, "")
					metrics.AnalyzerTasksTotal.WithLabelValues("success").Inc()
					metrics.AnalyzerActiveWorkers.Dec()
					taskSuccess = true
					return
				}
			}

			currentCfg := d.GetConfig()
			ratio := currentCfg.ShmExpansionRatio
			if ratio <= 0 { ratio = 3.5 }
			estimatedSize := EstimateShmSizeForTaskWithRatio(task, ratio)

			d.LogInfo("[W-%d] [IO Monad] Waiting for Adaptive Demucs execution slot (limit: %d)...", id, d.demucsScheduler.GetLimit())
			d.demucsScheduler.Acquire()

			delaySec := currentCfg.ShmAllocationDelaySec
			if delaySec > 0 {
				time.Sleep(time.Duration(delaySec) * time.Second)
			}

			shmAllocStart := time.Now()
			arenaSet := d.arenaPool.GetWorkerArenaSet(id)
			var allocError error

			d.allocMutex.Lock()
			for {
				availPhys, err := GetAvailableMemory()
				if err != nil {
					d.LogWarn("[W-%d] Memory check failed: %v", id, err)
					break 
				}
				// 全ステム合計の共有メモリ割り当て予定容量 (len(stems)) + 作業用余力 2GB を確認
				totalStemsNeeded := uint64(estimatedSize) * uint64(len(stems))
				requiredMem := totalStemsNeeded + (2 * 1024 * 1024 * 1024) 
				if availPhys > requiredMem { break }
				d.LogInfo("[W-%d] Waiting for memory for all stems (%d MB total)... (Avail: %d MB)", id, totalStemsNeeded/1024/1024, availPhys/1024/1024)

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
					_, err := arenaSet.GetOrCreateArena(stem, estimatedSize)
					if err != nil {
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
				d.demucsScheduler.Release()
				_ = arenaSet.UnfreezeAll()
				d.failTask(task, allocError.Error())
				metrics.AnalyzerTasksTotal.WithLabelValues("oom_failed").Inc()
				return
			}

			tagsMap := arenaSet.GetTagsMap()

			// 3. Demucs via Resident DemucsDaemonPool
			endSampleParam = task.EndSample
			if endSampleParam == 0 {
				endSampleParam = -1
			}
			demucsStageStart := time.Now()
			ctxDemucs, cancelDemucs := context.WithTimeout(context.Background(), 300*time.Second)
			demucsClient, dErr := d.demucsPool.Acquire(ctxDemucs)
			if dErr != nil {
				cancelDemucs()
				d.demucsScheduler.Release()
				_ = arenaSet.UnfreezeAll()
				d.failTask(task, fmt.Sprintf("Failed to acquire Demucs daemon for separation: %v", dErr))
				return
			}

			sepResp, sepErr := demucsClient.Separate(ctxDemucs, DemucsSeparatePayload{
				FlacPath:    task.FlacPath,
				ShmTags:     tagsMap,
				StartSample: task.StartSample,
				EndSample:   endSampleParam,
				UseDml:      false,
			})
			cancelDemucs()
			d.demucsPool.Release(demucsClient)
			d.demucsScheduler.Release()

			if d.statsTracker != nil {
				d.statsTracker.RecordStageDuration("demucs", time.Since(demucsStageStart))
			}

			if sepErr != nil {
				_ = arenaSet.UnfreezeAll()
				d.failTask(task, fmt.Sprintf("Demucs daemon separation failed: %v", sepErr))
				return
			}

			trackHash = sepResp.AudioHash
			demucsSR := sepResp.SR
			if demucsSR == 0 {
				demucsSR = 44100
			}
			demucsStems := sepResp.Stems
			if d.statsTracker != nil && sepResp.Profile != nil {
				for step, dur := range sepResp.Profile {
					d.statsTracker.RecordPythonStepDuration("demucs", step, dur)
				}
			}

			// 4. Freeze Shared Memory (PAGE_READONLY)
			if err := arenaSet.FreezeAll(); err != nil {
				d.LogWarn("[Worker %d] Failed to freeze SHM arenas: %v", id, err)
			}

			// 4.5 Go In-Process SHM Integrity Verification (eliminates functor_precache.py python startup)
			if err := arenaSet.VerifyIntegrity(stems); err != nil {
				_ = arenaSet.UnfreezeAll()
				d.failTask(task, fmt.Sprintf("SHM integrity verification failed: %v", err))
				return
			}

			// 5. Feature Extraction via WorkerDaemonPool (Librosa, Tensor, Essentia in single in-memory call)
			daemonStageStart := time.Now()
			ctxAcquire, cancelAcquire := context.WithTimeout(context.Background(), 120*time.Second)
			daemonClient, daemonErr := d.daemonPool.Acquire(ctxAcquire)
			cancelAcquire()

			if daemonErr != nil {
				_ = arenaSet.UnfreezeAll()
				d.failTask(task, fmt.Sprintf("Failed to acquire worker daemon: %v", daemonErr))
				return
			}

			extractPayload := ExtractAllPayload{
				SR:        demucsSR,
				TrackHash: trackHash,
				Stems:     demucsStems,
			}
			ctxExtract, cancelExtract := context.WithTimeout(context.Background(), 90*time.Second)
			daemonResp, daemonExtractErr := daemonClient.ExtractAll(ctxExtract, extractPayload)
			cancelExtract()
			d.daemonPool.Release(daemonClient)

			if daemonExtractErr != nil {
				_ = arenaSet.UnfreezeAll()
				d.failTask(task, fmt.Sprintf("Worker daemon extraction failed: %v", daemonExtractErr))
				return
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
				_ = arenaSet.UnfreezeAll()
				d.failTask(task, fmt.Sprintf("Failed to marshal Librosa features: %v", err))
				return
			}
			tensorOutBytes, err := json.Marshal(daemonResp.Tensor)
			if err != nil {
				_ = arenaSet.UnfreezeAll()
				d.failTask(task, fmt.Sprintf("Failed to marshal Tensor features: %v", err))
				return
			}
			essOutBytes, err := json.Marshal(daemonResp.Essentia)
			if err != nil {
				_ = arenaSet.UnfreezeAll()
				d.failTask(task, fmt.Sprintf("Failed to marshal Essentia features: %v", err))
				return
			}

			libOut := string(libOutBytes)
			tensorOut := string(tensorOutBytes)
			essOut := string(essOutBytes)

			// 共有メモリアリーナを次回タスクのために Unfreeze (PAGE_READWRITE 復元) してプールへ維持しますわ！
			_ = arenaSet.UnfreezeAll()
			
			// 6. Write Output and Run Ingester
			baseName := filepath.Base(task.FlacPath)
			outName := fmt.Sprintf("%s_%s.json", trackHash, baseName)
			outNameEss := fmt.Sprintf("%s_%s_essentia.json", trackHash, baseName)
			outNameTensor := fmt.Sprintf("%s_%s_tensor.json", trackHash, baseName)
			
			parentDir := findProjectRoot()

			queueDir := d.GetConfig().QueueDir
			if queueDir == "" {
				if parentDir != "" {
					queueDir = filepath.Join(parentDir, "queue")
				} else {
					queueDir = filepath.Join("..", "queue")
				}
			} else if !filepath.IsAbs(queueDir) {
				if parentDir != "" {
					queueDir = filepath.Join(parentDir, queueDir)
				}
			}

			if absQueueDir, err := filepath.Abs(queueDir); err == nil {
				queueDir = absQueueDir
			}
			
			if err := os.MkdirAll(queueDir, 0755); err != nil {
				d.failTask(task, fmt.Sprintf("Failed to create queue dir: %v", err))
				return
			}
			
			outPath := filepath.Join(queueDir, outName)
			outPathEss := filepath.Join(queueDir, outNameEss)
			outPathTensor := filepath.Join(queueDir, outNameTensor)
			
			if err := os.WriteFile(outPathEss, []byte(essOut), 0644); err != nil {
				cleanupQueueFiles(queueDir, trackHash, baseName)
				d.failTask(task, fmt.Sprintf("Failed to write Essentia JSON: %v", err))
				return
			}
			if err := os.WriteFile(outPathTensor, []byte(tensorOut), 0644); err != nil {
				cleanupQueueFiles(queueDir, trackHash, baseName)
				d.failTask(task, fmt.Sprintf("Failed to write Tensor JSON: %v", err))
				return
			}
			if err := os.WriteFile(outPath, []byte(libOut), 0644); err != nil {
				cleanupQueueFiles(queueDir, trackHash, baseName)
				d.failTask(task, fmt.Sprintf("Failed to write Librosa JSON: %v", err))
				return
			}

			// 6.4 FLAC Tagger
			taggerArgs := []string{
				"--flac-path", task.FlacPath,
				"--json-path", outPath,
				"--predictions-json-path", outPathEss,
				"--tensor-json-path", outPathTensor,
			}
			if task.TrackNumber > 0 {
				taggerArgs = append(taggerArgs, "--prefix", fmt.Sprintf("CUE_TRACK%02d", task.TrackNumber))
			}
			taggerStart := time.Now()
			tagOut, tagErr := d.runPythonScript("flac_tagger.py", taggerArgs, id, "FlacTagger", ColorGreen, true)
			if d.statsTracker != nil {
				d.statsTracker.RecordStageDuration("flac_tagger", time.Since(taggerStart))
				parseAndRecordPythonProfile(d.statsTracker, "tagger", tagOut)
			}
			if tagErr != nil {
				d.LogWarn("[W-%d] FLAC tagger warned/failed for %s: %v", id, task.FlacPath, tagErr)
			}

			// Clean up intermediate JSON files immediately
			cleanupQueueFiles(queueDir, trackHash, baseName)

			// 6. Decoupled Asynchronous DB Ingestion (Compute と IO の完全分離)
			ingestPayload := IngestPayload{
				TrackHash:    trackHash,
				Task:         task,
				LibrosaJSON:  json.RawMessage(libOut),
				EssentiaJSON: json.RawMessage(essOut),
				TensorJSON:   json.RawMessage(tensorOut),
			}

			// Ingest キューへ送信（メインワーカーはブロックされず即座に次タスクへ遷移）
			d.ingestQueue <- ingestPayload
			metrics.AnalyzerActiveWorkers.Dec()
			taskSuccess = true
			d.LogInfo("[W-%d] Compute & tagging completed, dispatched to IngestWorker: %s (Track %d)", id, task.FlacPath, task.TrackNumber)
		}(task)
	}
}

type FlexibleString string

func (fs *FlexibleString) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*fs = FlexibleString(s)
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*fs = FlexibleString(strings.Join(arr, " / "))
		return nil
	}
	*fs = FlexibleString(string(data))
	return nil
}

func (fs FlexibleString) String() string {
	return string(fs)
}

type CueInspectTrack struct {
	TrackNumber int            `json:"track_number"`
	StartSample int64          `json:"start_sample"`
	EndSample   int64          `json:"end_sample"`
	Title       FlexibleString `json:"title"`
	Artist      FlexibleString `json:"artist"`
}

type CueInspectResult struct {
	Status      string            `json:"status"`
	Filepath    string            `json:"filepath"`
	Album       FlexibleString    `json:"album"`
	AlbumArtist FlexibleString    `json:"album_artist"`
	Tracks      []CueInspectTrack `json:"tracks"`
}

func (d *Dispatcher) InspectCue(flacPath string) (*CueInspectResult, error) {
	out, err := d.runPythonScript("worker_cue.py", []string{
		"--flac-path", flacPath,
	}, 0, "CueInspect", ColorCyan, true)
	if err != nil {
		return nil, err
	}
	var res CueInspectResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		return nil, fmt.Errorf("failed to parse CueInspect JSON: %w (raw: %s)", err, out)
	}
	return &res, nil
}

type pythonProfileEnvelope struct {
	Profile map[string]float64 `json:"profile"`
}

func parseAndRecordPythonProfile(st *StatsTracker, component, jsonStr string) {
	if st == nil || jsonStr == "" {
		return
	}
	var env pythonProfileEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonStr)), &env); err == nil && env.Profile != nil {
		for step, durSec := range env.Profile {
			st.RecordPythonStepDuration(component, step, durSec)
		}
	}
}
