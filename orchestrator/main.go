package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type TrackSlice struct {
	TrackNumber int    `json:"track_number"`
	StartSample int64  `json:"start_sample"`
	EndSample   int64  `json:"end_sample"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
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
}

type Config struct {
	Orchestrator struct {
		NumWorkers            int    `toml:"num_workers"`
		DemucsConcurrentLimit int    `toml:"demucs_concurrent_limit"`
		ShmAllocationDelaySec int    `toml:"shm_allocation_delay_sec"`
		QueueDir              string `toml:"queue_dir"`
		TestFlacDir           string `toml:"test_flac_dir"`
	} `toml:"orchestrator"`
	PythonEnv map[string]string `toml:"python_env"`
}

var (
	globalConfig  Config
	allocMutex    sync.Mutex
	demucsSemaphore chan struct{}
)

// ANSI Colors
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorCyan   = "\033[36m"
	ColorGray   = "\033[90m"
	ColorPurple = "\033[35m"
)



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

func worker(id int, taskQueue <-chan TaskPayload, wg *sync.WaitGroup, noDB bool) {
	defer wg.Done()
	
	// uuidgen replacement: simple pseudo-uuid or timestamp-based ID generator for tags
	stems := []string{"mix", "bass", "drums", "vocals", "other", "guitar", "piano"}

	for task := range taskQueue {
		log.Printf("%s[W-%d] [IO Monad] Starting processing: %s%s\n", ColorGreen, id, task.FlacPath, ColorReset)
		
		// 1. Calculate Estimated Size
		estimatedSize := EstimateShmSize(task.FileSize)
		
		log.Printf("%s[W-%d] [IO Monad] Waiting for Demucs execution slot (No RAM allocated yet)...%s\n", ColorCyan, id, ColorReset)
		// OOMを防ぐため、SHMを確保する「前」にDemucs推論の同時実行枠を確保する
		demucsSemaphore <- struct{}{}
		
		// 旦那様の提案通り、他ワーカーのメモリ展開がOSに反映されるまで遅延
		delaySec := globalConfig.Orchestrator.ShmAllocationDelaySec
		if delaySec <= 0 {
			delaySec = 2 // default fallback
		}
		time.Sleep(time.Duration(delaySec) * time.Second)
		
		// Memory Throttling and Allocation (Protected by Mutex to prevent race conditions)
		shmMap := make(map[string]*SharedMemory)
		tagsMap := make(map[string]string)
		var allocError error
		
		allocMutex.Lock()
		for {
			availPhys, err := GetAvailableMemory()
			if err != nil {
				log.Printf("%s[W-%d] [IO Monad] Memory check failed: %v%s\n", ColorYellow, id, err, ColorReset)
				break // fallback
			}
			requiredMem := uint64(estimatedSize) + (4 * 1024 * 1024 * 1024) // 4GB margin (Dynamic RAM evaluation)
			if availPhys > requiredMem {
				break
			}
			log.Printf("%s[W-%d] [IO Monad] Waiting for memory... (Avail: %d MB)%s\n", ColorYellow, id, availPhys/1024/1024, ColorReset)
			allocMutex.Unlock()
			time.Sleep(3 * time.Second)
			allocMutex.Lock()
		}
		
		// 2. Pre-allocate Shared Memory while holding the lock
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
		
		// 確保後、Pythonがメモリを食い始める前に他ワーカーが突っ込んでこないようにさらに少し遅延
		time.Sleep(2 * time.Second)
		allocMutex.Unlock()
		
		if allocError != nil {
			log.Printf("%s[W-%d] [IO Monad] SHM Allocation Error: %v%s\n", ColorRed, id, allocError, ColorReset)
			<-demucsSemaphore // エラー時も枠を解放
			// Cleanup what was allocated
			for _, shm := range shmMap {
				shm.Close()
			}
			continue
		}
		
		// 3. Run Demucs Worker
		tagsJson, _ := json.Marshal(tagsMap)
		
		// Use python.exe from the active environment (PATH)
		pythonPath := "python.exe"
		
		exePath, _ := os.Executable()
		parentDir := filepath.Dir(filepath.Dir(exePath))

		cmdDemucs := exec.Command(pythonPath, "worker_demucs.py", "--flac-path", task.FlacPath, "--shm-tags", string(tagsJson), "--start-sample", fmt.Sprintf("%d", task.StartSample), "--end-sample", fmt.Sprintf("%d", task.EndSample))
		cmdDemucs.Dir = parentDir
		
		var envVars []string
		for k, v := range globalConfig.PythonEnv {
			envVars = append(envVars, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}
		cmdDemucs.Env = append(os.Environ(), envVars...)
		
		var outBuf bytes.Buffer
		cmdDemucs.Stdout = &outBuf
		stderrDemucs, _ := cmdDemucs.StderrPipe()
		
		log.Printf("%s[W-%d] [IO Monad] Running Demucs worker...%s\n", ColorCyan, id, ColorReset)
		
		if err := cmdDemucs.Start(); err != nil {
			log.Printf("%s[W-%d] [IO Monad] Demucs start failed: %v%s\n", ColorRed, id, err, ColorReset)
			<-demucsSemaphore
			for _, shm := range shmMap {
				shm.Close()
			}
			continue
		}
		
		streamColoredLog(stderrDemucs, id, "Demucs", ColorCyan)
		
		errDemucs := cmdDemucs.Wait()
		<-demucsSemaphore // 実行枠を解放
		
		if errDemucs != nil {
			log.Printf("%s[W-%d] [IO Monad] Demucs processing failed or crashed (OOM/Type Error). Track marked as Failed. %v%s\n", ColorRed, id, errDemucs, ColorReset)
			for _, shm := range shmMap {
				shm.Close()
			}
			continue
		}
		
		demucsMetaJson := outBuf.String()
		log.Printf("%s[W-%d] [IO Monad] Demucs completed successfully.%s\n", ColorGreen, id, ColorReset)
		
		var demucsMeta struct {
			Status    string `json:"status"`
			AudioHash string `json:"audio_hash"`
		}
		if err := json.Unmarshal([]byte(demucsMetaJson), &demucsMeta); err != nil || demucsMeta.Status != "success" || demucsMeta.AudioHash == "" {
			log.Printf("%s[W-%d] [IO Monad] Failed to parse demucs metadata or missing audio_hash: %v%s\n", ColorRed, id, err, ColorReset)
			for _, shm := range shmMap {
				shm.Close()
			}
			continue
		}
		trackHash := demucsMeta.AudioHash
		
		// 4. Freeze Shared Memory (WORM)
		for stem, shm := range shmMap {
			if err := shm.Freeze(); err != nil {
				log.Printf("[Worker %d] Failed to freeze SHM %s: %v\n", id, stem, err)
			}
		}
		
		// 5. Run Librosa Worker
		log.Printf("%s[W-%d] [IO Monad] Running Librosa worker...%s\n", ColorPurple, id, ColorReset)
		
		cmdLibrosa := exec.Command(pythonPath, "worker_librosa.py", "--shm-metadata", demucsMetaJson, "--track-hash", trackHash)
		cmdLibrosa.Dir = parentDir
		cmdLibrosa.Env = append(os.Environ(), envVars...)
		
		var libOutBuf bytes.Buffer
		cmdLibrosa.Stdout = &libOutBuf
		stderrLibrosa, _ := cmdLibrosa.StderrPipe()
		
		if err := cmdLibrosa.Start(); err != nil {
			log.Printf("%s[W-%d] [IO Monad] Librosa start failed: %v%s\n", ColorRed, id, err, ColorReset)
			for _, shm := range shmMap {
				shm.Close()
			}
			continue
		}
		
		streamColoredLog(stderrLibrosa, id, "Librosa", ColorBlue)
		
		if errLibrosa := cmdLibrosa.Wait(); errLibrosa != nil {
			log.Printf("%s[W-%d] [IO Monad] Librosa processing failed: %v%s\n", ColorRed, id, errLibrosa, ColorReset)
			for _, shm := range shmMap {
				shm.Close()
			}
			continue
		}
		
		// 5.5 Run Essentia Worker
		log.Printf("%s[W-%d] [IO Monad] Running Essentia worker...%s\n", ColorPurple, id, ColorReset)
		
		cmdEssentia := exec.Command(pythonPath, "worker_essentia.py", "--shm-metadata", demucsMetaJson, "--track-hash", trackHash)
		cmdEssentia.Dir = parentDir
		cmdEssentia.Env = append(os.Environ(), envVars...)
		
		var essOutBuf bytes.Buffer
		cmdEssentia.Stdout = &essOutBuf
		stderrEssentia, _ := cmdEssentia.StderrPipe()
		
		if err := cmdEssentia.Start(); err != nil {
			log.Printf("%s[W-%d] [IO Monad] Essentia start failed: %v%s\n", ColorRed, id, err, ColorReset)
			for _, shm := range shmMap {
				shm.Close()
			}
			continue
		}
		
		streamColoredLog(stderrEssentia, id, "Essentia", ColorBlue)
		
		if errEssentia := cmdEssentia.Wait(); errEssentia != nil {
			log.Printf("%s[W-%d] [IO Monad] Essentia processing failed: %v%s\n", ColorRed, id, errEssentia, ColorReset)
			for _, shm := range shmMap {
				shm.Close()
			}
			continue
		}
		
		log.Printf("%s[W-%d] [IO Monad] Successfully processed entire pipeline: %s (Track %d)%s\n", ColorGreen, id, task.FlacPath, task.TrackNumber, ColorReset)
		
		// 6. Output Handling
		baseName := filepath.Base(task.FlacPath)
		outName := fmt.Sprintf("%s_%s.json", trackHash, baseName)
		outNameEss := fmt.Sprintf("%s_%s_essentia.json", trackHash, baseName)
		
		queueDir := globalConfig.Orchestrator.QueueDir
		if queueDir == "" {
			queueDir = filepath.Join("..", "queue")
		}
		outPath := filepath.Join(queueDir, outName)
		outPathEss := filepath.Join(queueDir, outNameEss)
		
		// Create queue dir if it doesn't exist
		os.MkdirAll(queueDir, 0755)
		
		os.WriteFile(outPathEss, essOutBuf.Bytes(), 0644)
		if err := os.WriteFile(outPath, libOutBuf.Bytes(), 0644); err != nil {
			log.Printf("[Worker %d] Failed to write local JSON: %v\n", id, err)
		} else {
			log.Printf("[Worker %d] Saved local JSON to: %s\n", id, outPath)
			
			// 6.5 Spawn ingester.py asynchronously
			ingesterCmd := exec.Command(pythonPath, "../ingester.py",
				"--flac-path", task.FlacPath,
				"--json-path", outPath,
				"--predictions-json-path", outPathEss,
				"--track-hash", trackHash,
				"--track-number", fmt.Sprintf("%d", task.TrackNumber),
				"--title", task.Title,
				"--artist", task.Artist,
				"--album", task.Album,
				"--album-artist", task.AlbumArtist,
			)
			// Pass environment variables to ingester.py
			ingesterCmd.Env = append(os.Environ(), envVars...)
			
			// Detach from parent to avoid blocking or zombie processes, but capture output via a goroutine
			stdoutPipe, errOut := ingesterCmd.StdoutPipe()
			stderrPipe, errErr := ingesterCmd.StderrPipe()
			if errOut == nil && errErr == nil {
				if err := ingesterCmd.Start(); err != nil {
					log.Printf("[Worker %d] Failed to start ingester.py: %v\n", id, err)
				} else {
					log.Printf("[Worker %d] Started ingester.py (PID %d) for %s\n", id, ingesterCmd.Process.Pid, trackHash)
					go func(cmd *exec.Cmd, out io.ReadCloser, err io.ReadCloser) {
						go streamColoredLog(out, id, "Ingester", ColorGray)
						go streamColoredLog(err, id, "Ingester-Err", ColorRed)
						cmd.Wait()
					}(ingesterCmd, stdoutPipe, stderrPipe)
				}
			} else {
				log.Printf("[Worker %d] Failed to create pipes for ingester.py\n", id)
			}
		}
		
		// 7. Cleanup
		for _, shm := range shmMap {
			shm.Close()
		}
	}
}

func setUTF8Console() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	setConsoleOutputCP := kernel32.NewProc("SetConsoleOutputCP")
	setConsoleOutputCP.Call(65001)
}

func main() {
	setUTF8Console()
	
	// Load config.toml
	exePath, _ := os.Executable()
	parentDir := filepath.Dir(filepath.Dir(exePath))
	configPath := filepath.Join(parentDir, "config.toml")
	
	tomlData, err := os.ReadFile(configPath)
	if err == nil {
		err = toml.Unmarshal(tomlData, &globalConfig)
		if err != nil {
			log.Printf("Warning: Failed to parse config.toml: %v\n", err)
		} else {
			log.Printf("Loaded configuration from %s\n", configPath)
		}
	} else {
		log.Printf("Warning: Could not read config.toml from %s: %v\n", configPath, err)
	}

	numWorkers := globalConfig.Orchestrator.NumWorkers
	if numWorkers <= 0 {
		numWorkers = 4
	}
	
	limit := globalConfig.Orchestrator.DemucsConcurrentLimit
	if limit <= 0 {
		limit = 1
	}
	demucsSemaphore = make(chan struct{}, limit)
	
	noDB := flag.Bool("no-db", false, "Disable PostgreSQL UPSERT and output JSON locally for testing")
	flag.Parse()

	taskQueue := make(chan TaskPayload, 1000)
	var wg sync.WaitGroup

	// Start worker pool
	for i := 1; i <= numWorkers; i++ {
		wg.Add(1)
		go worker(i, taskQueue, &wg, *noDB)
	}

	http.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var payload TaskPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
			return
		}

		if payload.FlacPath == "" {
			http.Error(w, "flacPath is required", http.StatusBadRequest)
			return
		}

		// Call extract_cue.py to get tracks
		exePath, _ := os.Executable()
		parentDir := filepath.Dir(filepath.Dir(exePath))
		cueCmd := exec.Command("python.exe", "extract_cue.py", payload.FlacPath)
		cueCmd.Dir = parentDir
		
		var envVars []string
		for k, v := range globalConfig.PythonEnv {
			envVars = append(envVars, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}
		cueCmd.Env = append(os.Environ(), envVars...)
		
		cueOut, err := cueCmd.Output()
		if err != nil {
			log.Printf("Failed to extract CUE for %s: %v", payload.FlacPath, err)
			// fallback: enqueue as one huge track
			payload.EndSample = -1 // flag for entire file
			taskQueue <- payload
		} else {
			var result struct {
				Status       string         `json:"status"`
				Slices       []TrackSlice   `json:"slices"`
				Tags         map[string]any `json:"tags"`
				TotalSamples int64          `json:"total_samples"`
			}
			if err := json.Unmarshal(cueOut, &result); err == nil && result.Status == "success" {
				
				extractStringOpt := func(m map[string]any, k string) string {
					if m == nil {
						return ""
					}
					if val, ok := m[k]; ok {
						if s, ok := val.(string); ok {
							return s
						}
						// If it's a list (from mutagen), try to take the first element
						if l, ok := val.([]any); ok && len(l) > 0 {
							if s, ok := l[0].(string); ok {
								return s
							}
						}
					}
					return ""
				}

				album := extractStringOpt(result.Tags, "album")
				albumArtist := extractStringOpt(result.Tags, "albumartist")
				if albumArtist == "" {
					albumArtist = extractStringOpt(result.Tags, "album artist")
				}

				for _, slice := range result.Slices {
					t := payload // copy
					t.TrackNumber = slice.TrackNumber
					t.StartSample = slice.StartSample
					t.EndSample = slice.EndSample
					t.Title = slice.Title
					t.Artist = slice.Artist
					t.Album = album
					t.AlbumArtist = albumArtist
					taskQueue <- t
				}
			} else {
				// fallback
				payload.EndSample = -1
				taskQueue <- payload
			}
		}

		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, "Task accepted: %s\n", payload.FlacPath)
	})

	server := &http.Server{Addr: ":8080"}

	// Start server in background
	go func() {
		log.Println("Go Orchestrator started. Listening on :8080...")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Listen error: %v\n", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down orchestrator...")

	close(taskQueue)
	wg.Wait()

	if err := server.Shutdown(context.Background()); err != nil {
		log.Fatalf("Server Shutdown Failed:%+v", err)
	}
	log.Println("Orchestrator stopped gracefully.")
}
