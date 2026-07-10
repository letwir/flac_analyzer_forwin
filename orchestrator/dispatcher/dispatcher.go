package dispatcher

import (
	"bufio"
	"bytes"
	"encoding/json"
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
)

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
}

type Config struct {
	NumWorkers            int
	DemucsConcurrentLimit int
	ShmAllocationDelaySec int
	QueueDir              string
	PythonEnv             map[string]string
}

type Dispatcher struct {
	config          Config
	db              *state.DB
	taskQueue       chan TaskPayload
	allocMutex      sync.Mutex
	demucsSemaphore chan struct{}
	wg              sync.WaitGroup
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
		config:          cfg,
		db:              db,
		taskQueue:       make(chan TaskPayload, 1000),
		demucsSemaphore: make(chan struct{}, cfg.DemucsConcurrentLimit),
	}
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

func streamColoredLog(pipe io.ReadCloser, workerID int, role string, color string) {
	scanner := bufio.NewScanner(pipe)
	prefix := fmt.Sprintf("%s[W-%d] [%s] ", color, workerID, role)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "[ERROR]") || strings.Contains(strings.ToLower(line), "error") || strings.Contains(strings.ToLower(line), "traceback") {
			fmt.Printf("%s[W-%d] [%s] %s%s\n", ColorRed, workerID, role, line, ColorReset)
		} else {
			fmt.Printf("%s%s%s\n", prefix, line, ColorReset)
		}
	}
}

func (d *Dispatcher) runPythonScript(scriptName string, args []string, workerID int, role, color string, captureStdout bool) (string, error) {
	pythonPath := "python.exe"
	exePath, _ := os.Executable()
	parentDir := filepath.Dir(filepath.Dir(exePath))

	cmdArgs := append([]string{scriptName}, args...)
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

	stderrPipe, _ := cmd.StderrPipe()
	
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start %s: %w", role, err)
	}
	
	streamColoredLog(stderrPipe, workerID, role, color)
	
	err := cmd.Wait()
	if err != nil {
		return "", fmt.Errorf("%s failed: %w", role, err)
	}
	
	return outBuf.String(), nil
}

func (d *Dispatcher) failTask(task TaskPayload, errMsg string) {
	log.Printf("%s[Dispatcher] Task Failed: %s -> %s%s\n", ColorRed, task.FlacPath, errMsg, ColorReset)
	d.db.UpdateStatus(task.FlacPath, state.StatusFailed, errMsg)
	metrics.AnalyzerTasksTotal.WithLabelValues("error").Inc()
	metrics.AnalyzerActiveWorkers.Dec()
}

func (d *Dispatcher) worker(id int) {
	defer d.wg.Done()
	
	stems := []string{"mix", "bass", "drums", "vocals", "other", "guitar", "piano"}

	for task := range d.taskQueue {
		metrics.AnalyzerQueueLength.Dec()
		metrics.AnalyzerActiveWorkers.Inc()
		
		log.Printf("%s[W-%d] [IO Monad] Starting processing: %s%s\n", ColorGreen, id, task.FlacPath, ColorReset)
		d.db.UpdateStatus(task.FlacPath, state.StatusRunning, "")
		
		estimatedSize := EstimateShmSize(task.FileSize)
		
		log.Printf("%s[W-%d] [IO Monad] Waiting for Demucs execution slot...%s\n", ColorCyan, id, ColorReset)
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
				log.Printf("%s[W-%d] Memory check failed: %v%s\n", ColorYellow, id, err, ColorReset)
				break 
			}
			requiredMem := uint64(estimatedSize) + (4 * 1024 * 1024 * 1024) 
			if availPhys > requiredMem { break }
			log.Printf("%s[W-%d] Waiting for memory... (Avail: %d MB)%s\n", ColorYellow, id, availPhys/1024/1024, ColorReset)
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
			continue
		}
		
		tagsJson, _ := json.Marshal(tagsMap)
		
		// 3. Demucs
		demucsOut, err := d.runPythonScript("worker_demucs.py", []string{
			"--flac-path", task.FlacPath, 
			"--shm-tags", string(tagsJson), 
			"--start-sample", fmt.Sprintf("%d", task.StartSample), 
			"--end-sample", fmt.Sprintf("%d", task.EndSample),
		}, id, "Demucs", ColorCyan, true)
		
		<-d.demucsSemaphore
		metrics.AnalyzerDemucsSlotsInUse.Dec()
		
		if err != nil {
			for _, shm := range shmMap { shm.Close() }
			d.failTask(task, err.Error())
			continue
		}
		
		var demucsMeta struct {
			Status    string `json:"status"`
			AudioHash string `json:"audio_hash"`
		}
		if err := json.Unmarshal([]byte(demucsOut), &demucsMeta); err != nil || demucsMeta.Status != "success" || demucsMeta.AudioHash == "" {
			for _, shm := range shmMap { shm.Close() }
			d.failTask(task, "Demucs metadata invalid")
			continue
		}
		trackHash := demucsMeta.AudioHash
		
		// 4. Freeze Shared Memory
		for stem, shm := range shmMap {
			if err := shm.Freeze(); err != nil {
				log.Printf("[Worker %d] Failed to freeze SHM %s: %v\n", id, stem, err)
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
			continue
		}
		
		// 5. Librosa
		libOut, err := d.runPythonScript("worker_librosa.py", []string{
			"--shm-metadata", precacheOut,
			"--track-hash", trackHash,
		}, id, "Librosa", ColorBlue, true)
		
		if err != nil {
			for _, shm := range shmMap { shm.Close() }
			d.failTask(task, err.Error())
			continue
		}
		
		// 5.3 Tensor
		tensorOut, err := d.runPythonScript("worker_tensor.py", []string{
			"--shm-metadata", precacheOut,
			"--track-hash", trackHash,
		}, id, "Tensor", ColorPurple, true)
		
		if err != nil {
			for _, shm := range shmMap { shm.Close() }
			d.failTask(task, err.Error())
			continue
		}
		
		// 5.5 Essentia
		essOut, err := d.runPythonScript("worker_essentia.py", []string{
			"--shm-metadata", precacheOut,
			"--track-hash", trackHash,
		}, id, "Essentia", ColorBlue, true)
		
		if err != nil {
			for _, shm := range shmMap { shm.Close() }
			d.failTask(task, err.Error())
			continue
		}
		
		for _, shm := range shmMap { shm.Close() }
		
		// 6. Write Output and Run Ingester
		baseName := filepath.Base(task.FlacPath)
		outName := fmt.Sprintf("%s_%s.json", trackHash, baseName)
		outNameEss := fmt.Sprintf("%s_%s_essentia.json", trackHash, baseName)
		outNameTensor := fmt.Sprintf("%s_%s_tensor.json", trackHash, baseName)
		
		queueDir := d.config.QueueDir
		if queueDir == "" { queueDir = filepath.Join("..", "queue") }
		
		os.MkdirAll(queueDir, 0755)
		
		outPath := filepath.Join(queueDir, outName)
		outPathEss := filepath.Join(queueDir, outNameEss)
		outPathTensor := filepath.Join(queueDir, outNameTensor)
		
		os.WriteFile(outPathEss, []byte(essOut), 0644)
		os.WriteFile(outPathTensor, []byte(tensorOut), 0644)
		os.WriteFile(outPath, []byte(libOut), 0644)
		
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
			// Even if ingester fails Postgres, the DLQ logic inside ingester.py will handle saving it to SQLite.
			// But for Go orchestrator state, should we mark it as FAILED or COMPLETED? 
			// DLQ means it's safely stored for later, but it didn't fully complete. 
			// Let's mark it as FAILED so it shows in the error count, but we know it's in DLQ.
			d.failTask(task, "Ingester failed (Sent to DLQ)")
			continue
		}
		
		log.Printf("%s[W-%d] Successfully processed entire pipeline: %s%s\n", ColorGreen, id, task.FlacPath, ColorReset)
		d.db.UpdateStatus(task.FlacPath, state.StatusCompleted, "")
		metrics.AnalyzerTasksTotal.WithLabelValues("success").Inc()
		metrics.AnalyzerActiveWorkers.Dec()
	}
}
