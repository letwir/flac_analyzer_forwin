package dispatcher

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"flac_analyzer/orchestrator/metrics"
	"flac_analyzer/orchestrator/state"
	"flac_analyzer/orchestrator/sysinfo"
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
	DemucsConcurrentLimit int
	ShmAllocationDelaySec int
	QueueDir              string
	PythonEnv             map[string]string
	LogLevel              LogLevel
	EventLog              EventLogger
	SkipDupByHash         bool
}

type Dispatcher struct {
	config                 Config
	db                     *state.DB
	taskQueue              chan TaskPayload
	allocMutex             sync.Mutex
	demucsSemaphore        chan struct{}
	tensorSemaphore        chan struct{}
	wg                     sync.WaitGroup
	logLevel               LogLevel
	eventLog               EventLogger
	skipDupByHash          bool
	activeInFlightRamBytes uint64
	inFlightMutex          sync.Mutex
}

const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorCyan   = "\033[36m"
	ColorPurple = "\033[35m"
)

func NewDispatcher(cfg Config, db *state.DB) *Dispatcher {
	return &Dispatcher{
		config:                 cfg,
		db:                     db,
		taskQueue:              make(chan TaskPayload, 1000),
		demucsSemaphore:        make(chan struct{}, cfg.DemucsConcurrentLimit),
		tensorSemaphore:        make(chan struct{}, 1),
		logLevel:               cfg.LogLevel,
		eventLog:               cfg.EventLog,
		skipDupByHash:          cfg.SkipDupByHash,
		activeInFlightRamBytes: 0,
	}
}

func (d *Dispatcher) LogDebug(format string, v ...interface{}) {
	if d.logLevel <= LevelDebug {
		log.Printf(format, v...)
	}
}

func (d *Dispatcher) LogInfo(format string, v ...interface{}) {
	if d.logLevel <= LevelInfo {
		log.Printf(format, v...)
	}
}

func (d *Dispatcher) LogWarn(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	if d.logLevel <= LevelWarn {
		log.Printf("%s[WARN] %s%s\n", ColorYellow, msg, ColorReset)
	}
	if d.eventLog != nil {
		_ = d.eventLog.Warning(1001, msg)
	}
}

func (d *Dispatcher) LogError(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	if d.logLevel <= LevelError {
		log.Printf("%s[ERROR] %s%s\n", ColorRed, msg, ColorReset)
	}
	if d.eventLog != nil {
		_ = d.eventLog.Error(1002, msg)
	}
	metrics.AnalyzerErrorsTotal.Inc()
}

func (d *Dispatcher) Start() {
	for i := 1; i <= d.config.NumWorkers; i++ {
		d.wg.Add(1)
		go d.worker(i)
	}
}

func (d *Dispatcher) Enqueue(task TaskPayload) error {
	metrics.AnalyzerQueueLength.Inc()
	d.taskQueue <- task
	return nil
}

func (d *Dispatcher) Stop() {
	close(d.taskQueue)
	d.wg.Wait()
}

func (d *Dispatcher) streamColoredLog(pipe io.ReadCloser, workerID int, role string, color string) {
	scanner := bufio.NewScanner(pipe)
	prefix := fmt.Sprintf("%s[W-%d] [%s] ", color, workerID, role)
	for scanner.Scan() {
		line := scanner.Text()
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
	cmdArgs := append([]string{scriptPath}, args...)
	cmd := exec.Command(pythonPath, cmdArgs...)
	cmd.Dir = parentDir

	var envVars []string
	for k, v := range d.config.PythonEnv {
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

func (d *Dispatcher) EvaluateGoNoGo(workerID int, task TaskPayload) (bool, time.Duration) {
	estimatedRam := EstimateDemucsTotalRamBytes(task)
	memInfo, err := sysinfo.GetMemoryInfo()
	if err != nil || memInfo == nil {
		return true, 0
	}

	d.inFlightMutex.Lock()
	inFlight := d.activeInFlightRamBytes
	d.inFlightMutex.Unlock()

	minAvailBytes := uint64(d.config.MinAvailRamGB * 1024 * 1024 * 1024)
	var maxUsableBytes uint64
	if d.config.MaxRamRatio > 0 {
		maxUsableBytes = uint64(float64(memInfo.TotalPhys) * d.config.MaxRamRatio)
	} else {
		maxUsableBytes = uint64(float64(memInfo.TotalPhys) * 0.95)
	}
	usedBytes := memInfo.TotalPhys - memInfo.AvailPhys

	if memInfo.AvailPhys < (estimatedRam + minAvailBytes) {
		d.LogWarn("[W-%d] [Gatekeeper: NOGO] Available RAM (%d MB) < Estimated Demucs RAM (%d MB) + MinAvail (%d MB). Delaying dispatch...",
			workerID, memInfo.AvailPhys/1024/1024, estimatedRam/1024/1024, minAvailBytes/1024/1024)
		return false, 2 * time.Second
	}

	if (usedBytes + inFlight + estimatedRam) > maxUsableBytes {
		d.LogWarn("[W-%d] [Gatekeeper: NOGO] Projected RAM (Used %d MB + InFlight %d MB + Task %d MB) > MaxAllowed (%d MB). Delaying dispatch...",
			workerID, usedBytes/1024/1024, inFlight/1024/1024, estimatedRam/1024/1024, maxUsableBytes/1024/1024)
		return false, 3 * time.Second
	}

	if memInfo.MemoryLoad >= 90 {
		d.LogWarn("[W-%d] [Gatekeeper: NOGO] System MemoryLoad too high (%d%%). Delaying dispatch...", workerID, memInfo.MemoryLoad)
		return false, 4 * time.Second
	}

	d.LogInfo("[W-%d] [Gatekeeper: GO] Dispatch Approved (Estimated Demucs RAM: %d MB, AvailPhys: %d MB)",
		workerID, estimatedRam/1024/1024, memInfo.AvailPhys/1024/1024)
	return true, 0
}

func (d *Dispatcher) worker(id int) {
	defer d.wg.Done()
	
	stems := []string{"mix", "bass", "drums", "vocals", "other", "guitar", "piano"}

	for task := range d.taskQueue {
		// Gatekeeper Pre-flight Decision (CUE/FLAC Demucs RAM Estimation)
		for {
			isGo, waitDur := d.EvaluateGoNoGo(id, task)
			if !isGo {
				time.Sleep(waitDur)
				continue
			}
			break
		}

		func(task TaskPayload) {
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
			
			if d.skipDupByHash {
				// 2.1 Calculate MD5 hash only (Lightweight decoding)
				endSampleParam = task.EndSample
				if endSampleParam == 0 {
					endSampleParam = -1
				}
				hashOut, err := d.runPythonScript("worker_demucs.py", []string{
					"--flac-path", task.FlacPath,
					"--shm-tags", "{}",
					"--start-sample", fmt.Sprintf("%d", task.StartSample),
					"--end-sample", fmt.Sprintf("%d", endSampleParam),
					"--check-hash-only",
				}, id, "HashCheck", ColorCyan, true)
				
				if err != nil {
					d.failTask(task, fmt.Sprintf("Hash calculation failed: %v", err))
					return
				}
				
				cleanHashOut := strings.TrimSpace(hashOut)
				var hashMeta struct {
					Status    string `json:"status"`
					AudioHash string `json:"audio_hash"`
				}
				if err := json.Unmarshal([]byte(cleanHashOut), &hashMeta); err != nil || hashMeta.AudioHash == "" {
					d.failTask(task, fmt.Sprintf("Failed to parse calculated hash (output: %s): %v", cleanHashOut, err))
					return
				}
				trackHash = hashMeta.AudioHash
				
				// 2.2 Query PostgreSQL via ingester.py --check-hash
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
					if parseErr := json.Unmarshal([]byte(cleanCheckOut), &checkMeta); parseErr != nil {
						d.LogWarn("[W-%d] DB check JSON parse failed for hash %s: %v (raw output: %s)", id, trackHash, parseErr, cleanCheckOut)
					} else if checkMeta.Exists {
						d.LogInfo("[W-%d] [IO Monad] Skip processing: Hash %s already exists in PostgreSQL", id, trackHash)
						d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusCompleted, "")
						metrics.AnalyzerTasksTotal.WithLabelValues("success").Inc()
						metrics.AnalyzerActiveWorkers.Dec()
						return
					}
				} else {
					d.LogWarn("[W-%d] DB check failed (will proceed anyway): %v", id, err)
				}
			}
			
			estimatedSize := EstimateShmSizeForTask(task)
			
			d.LogInfo("[W-%d] [IO Monad] Waiting for Demucs execution slot...", id)
			d.demucsSemaphore <- struct{}{}
			metrics.AnalyzerDemucsSlotsInUse.Inc()
			
			delaySec := d.config.ShmAllocationDelaySec
			if delaySec <= 0 { delaySec = 2 }
			time.Sleep(time.Duration(delaySec) * time.Second)
			
			shmMap := make(map[string]*SharedMemory)
			tagsMap := make(map[string]string)
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
			
			baseTag := fmt.Sprintf("Local\\FlacShm_W%d_%d", id, task.FileSize)
			for _, stem := range stems {
				tagName := fmt.Sprintf("%s_%s", baseTag, stem)
				tagsMap[stem] = tagName
				shm, err := NewSharedMemory(tagName, estimatedSize)
				if err != nil {
					allocError = fmt.Errorf("Failed to allocate SHM for %s: %v", stem, err)
					break
				}
				shmMap[stem] = shm
			}
			time.Sleep(2 * time.Second)
			d.allocMutex.Unlock()
			
			if allocError != nil {
				<-d.demucsSemaphore
				metrics.AnalyzerDemucsSlotsInUse.Dec()
				for _, shm := range shmMap { shm.Close() }
				d.failTask(task, allocError.Error())
				metrics.AnalyzerTasksTotal.WithLabelValues("oom_failed").Inc()
				return
			}
			
			tagsJson, err := json.Marshal(tagsMap)
			if err != nil {
				<-d.demucsSemaphore
				metrics.AnalyzerDemucsSlotsInUse.Dec()
				for _, shm := range shmMap { shm.Close() }
				d.failTask(task, fmt.Sprintf("Failed to marshal tagsMap: %v", err))
				return
			}
			
			// 3. Demucs
			endSampleParam = task.EndSample
			if endSampleParam == 0 {
				endSampleParam = -1
			}
			demucsOut, err := d.runPythonScript("worker_demucs.py", []string{
				"--flac-path", task.FlacPath, 
				"--shm-tags", string(tagsJson), 
				"--start-sample", fmt.Sprintf("%d", task.StartSample), 
				"--end-sample", fmt.Sprintf("%d", endSampleParam),
			}, id, "Demucs", ColorCyan, true)
			
			<-d.demucsSemaphore
			metrics.AnalyzerDemucsSlotsInUse.Dec()
			
			if err != nil {
				for _, shm := range shmMap { shm.Close() }
				d.failTask(task, err.Error())
				return
			}
			
			var demucsMeta struct {
				Status    string `json:"status"`
				AudioHash string `json:"audio_hash"`
			}
			if err := json.Unmarshal([]byte(demucsOut), &demucsMeta); err != nil || demucsMeta.Status != "success" || demucsMeta.AudioHash == "" {
				for _, shm := range shmMap { shm.Close() }
				d.failTask(task, "Demucs metadata invalid")
				return
			}
			trackHash = demucsMeta.AudioHash
			
			// 4. Freeze Shared Memory
			for stem, shm := range shmMap {
				if err := shm.Freeze(); err != nil {
					d.LogWarn("[Worker %d] Failed to freeze SHM %s: %v", id, stem, err)
				}
			}

			// 4.5 Precache Functor
			precacheOut, err := d.runPythonScript("functor_precache.py", []string{
				"--shm-metadata", demucsOut,
				"--track-hash", trackHash,
			}, id, "Precache", ColorCyan, true)
			
			if err != nil {
				for _, shm := range shmMap { shm.Close() }
				d.failTask(task, err.Error())
				return
			}
			
			// 5. Parallel Feature Extraction (Librosa, Tensor, Essentia)
			var wg sync.WaitGroup
			var workerErr error
			var errOnce sync.Once

			setWorkerErr := func(e error) {
				errOnce.Do(func() {
					workerErr = e
				})
			}

			var libOut, tensorOut, essOut string

			wg.Add(3)
			go func() {
				defer wg.Done()
				out, err := d.runPythonScript("worker_librosa.py", []string{
					"--shm-metadata", precacheOut,
					"--track-hash", trackHash,
				}, id, "Librosa", ColorBlue, true)
				if err != nil {
					setWorkerErr(fmt.Errorf("Librosa failed: %w", err))
					return
				}
				libOut = out
			}()

			go func() {
				defer wg.Done()

				// Tensor (ONNX/PyTorch) Exclusive Execution Lock to prevent VRAM spikes across parallel workers
				d.tensorSemaphore <- struct{}{}
				defer func() {
					time.Sleep(150 * time.Millisecond) // VRAM GC cleanup margin
					<-d.tensorSemaphore
				}()

				out, err := d.runPythonScript("worker_tensor.py", []string{
					"--shm-metadata", precacheOut,
					"--track-hash", trackHash,
				}, id, "Tensor", ColorPurple, true)
				if err != nil {
					setWorkerErr(fmt.Errorf("Tensor failed: %w", err))
					return
				}
				tensorOut = out
			}()

			go func() {
				defer wg.Done()
				out, err := d.runPythonScript("worker_essentia.py", []string{
					"--shm-metadata", precacheOut,
					"--track-hash", trackHash,
				}, id, "Essentia", ColorBlue, true)
				if err != nil {
					setWorkerErr(fmt.Errorf("Essentia failed: %w", err))
					return
				}
				essOut = out
			}()

			wg.Wait()

			if workerErr != nil {
				for _, shm := range shmMap { shm.Close() }
				d.failTask(task, workerErr.Error())
				return
			}
			
			for _, shm := range shmMap { shm.Close() }
			
			// 6. Write Output and Run Ingester
			baseName := filepath.Base(task.FlacPath)
			outName := fmt.Sprintf("%s_%s.json", trackHash, baseName)
			outNameEss := fmt.Sprintf("%s_%s_essentia.json", trackHash, baseName)
			outNameTensor := fmt.Sprintf("%s_%s_tensor.json", trackHash, baseName)
			
			parentDir := findProjectRoot()

			queueDir := d.config.QueueDir
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
				d.failTask(task, fmt.Sprintf("Failed to write Essentia JSON: %v", err))
				return
			}
			if err := os.WriteFile(outPathTensor, []byte(tensorOut), 0644); err != nil {
				d.failTask(task, fmt.Sprintf("Failed to write Tensor JSON: %v", err))
				return
			}
			if err := os.WriteFile(outPath, []byte(libOut), 0644); err != nil {
				d.failTask(task, fmt.Sprintf("Failed to write Librosa JSON: %v", err))
				return
			}
			
			// 6.5 Ingester
			// Ingester handles DB upsert and DLQ logic
			_, err = d.runPythonScript("ingester.py", []string{
				"--flac-path", task.FlacPath,
				"--json-path", outPath,
				"--predictions-json-path", outPathEss,
				"--tensor-json-path", outPathTensor,
				"--track-hash", trackHash,
				"--track-number", fmt.Sprintf("%d", task.TrackNumber),
				"--title", task.Title,
				"--artist", task.Artist,
				"--album", task.Album,
				"--album-artist", task.AlbumArtist,
			}, id, "Ingester", ColorGreen, true)
			
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
					d.LogWarn("[W-%d] DLQ fallback detected (Exit code 2) for %s (Track %d). Scheduled retry in 10 minutes.", id, task.FlacPath, task.TrackNumber)
					d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusRunning, "DLQ fallback: Retry scheduled in 10m")
					metrics.AnalyzerActiveWorkers.Dec()

					go func(t TaskPayload) {
						time.Sleep(10 * time.Minute)
						d.LogInfo("[DLQ-Retry] Running retry_ingest.py for %s (Track %d)...", t.FlacPath, t.TrackNumber)
						_, retryErr := d.runPythonScript("retry_ingest.py", []string{}, 0, "RetryIngest", ColorYellow, true)
						if retryErr == nil {
							d.LogInfo("[DLQ-Retry] Retry succeeded for %s (Track %d)", t.FlacPath, t.TrackNumber)
							d.db.UpdateStatus(t.FlacPath, t.TrackNumber, state.StatusCompleted, "")
							metrics.AnalyzerTasksTotal.WithLabelValues("success").Inc()
						} else {
							d.LogError("[DLQ-Retry] Retry failed for %s (Track %d): %v. Keeping in DLQ and marking FAILED.", t.FlacPath, t.TrackNumber, retryErr)
							d.db.UpdateStatus(t.FlacPath, t.TrackNumber, state.StatusFailed, fmt.Sprintf("DLQ retry failed after 10m: %v", retryErr))
							metrics.AnalyzerTasksTotal.WithLabelValues("error").Inc()
						}
					}(task)
					return
				}

				d.failTask(task, fmt.Sprintf("Ingester failed: %v", err))
				return
			}
			
			d.LogInfo("[W-%d] Successfully processed entire pipeline: %s (Track %d)", id, task.FlacPath, task.TrackNumber)
			d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusCompleted, "")
			metrics.AnalyzerTasksTotal.WithLabelValues("success").Inc()
			metrics.AnalyzerActiveWorkers.Dec()
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
