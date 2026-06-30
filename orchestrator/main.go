package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
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
)

type TaskPayload struct {
	FlacPath     string `json:"flacPath"`
	FileSize     int64  `json:"fileSize"`
	TargetScript string `json:"targetScript"`
}

const numWorkers = 4

var (
	allocMutex    sync.Mutex
	demucsSemaphore = make(chan struct{}, 1) // Demucs の同時実行数を厳密に制限するセマフォ (ONNX OOM対策)
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

func computeMD5(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
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

func worker(id int, taskQueue <-chan TaskPayload, wg *sync.WaitGroup, noDB bool) {
	defer wg.Done()
	
	// uuidgen replacement: simple pseudo-uuid or timestamp-based ID generator for tags
	stems := []string{"mix", "bass", "drums", "vocals", "other", "guitar", "piano"}

	for task := range taskQueue {
		// Calculate MD5 hash
		trackHash, err := computeMD5(task.FlacPath)
		if err != nil {
			log.Printf("%s[W-%d] [IO Monad] MD5 Error: %v%s\n", ColorRed, id, err, ColorReset)
			continue
		}
		
		log.Printf("%s[W-%d] [IO Monad] Starting processing: %s (MD5: %s)%s\n", ColorGreen, id, task.FlacPath, trackHash, ColorReset)
		
		// 1. Calculate Estimated Size
		estimatedSize := EstimateShmSize(task.FileSize)
		
		log.Printf("%s[W-%d] [IO Monad] Waiting for Demucs execution slot (No RAM allocated yet)...%s\n", ColorCyan, id, ColorReset)
		// OOMを防ぐため、SHMを確保する「前」にDemucs推論の同時実行枠を確保する
		demucsSemaphore <- struct{}{}
		
		// 旦那様の提案通り、他ワーカーのメモリ展開がOSに反映されるまで遅延
		time.Sleep(2 * time.Second)
		
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
		
		// Get absolute path to python.exe
		absPythonPath, _ := filepath.Abs(filepath.Join("..", ".venv", "Scripts", "python.exe"))

		cmdDemucs := exec.Command(absPythonPath, "demucs_worker.py", "--flac-path", task.FlacPath, "--shm-tags", string(tagsJson), "--track-hash", trackHash)
		cmdDemucs.Dir = ".."
		
		var outBuf bytes.Buffer
		cmdDemucs.Stdout = &outBuf
		stderrDemucs, _ := cmdDemucs.StderrPipe()
		
		log.Printf("%s[W-%d] [IO Monad] Running Demucs worker...%s\n", ColorCyan, id, ColorReset)
		
		if err := cmdDemucs.Start(); err != nil {
			log.Printf("%s[W-%d] [IO Monad] Demucs start failed: %v%s\n", ColorRed, id, err, ColorReset)
			heavyTaskLock.Unlock()
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
		
		// 4. Freeze Shared Memory (WORM)
		for stem, shm := range shmMap {
			if err := shm.Freeze(); err != nil {
				log.Printf("[Worker %d] Failed to freeze SHM %s: %v\n", id, stem, err)
			}
		}
		
		// 5. Run Librosa Worker
		log.Printf("%s[W-%d] [IO Monad] Running Librosa worker...%s\n", ColorPurple, id, ColorReset)
		
		cmdLibrosa := exec.Command(absPythonPath, "librosa_worker.py", "--shm-metadata", demucsMetaJson, "--track-hash", trackHash)
		cmdLibrosa.Dir = ".."
		
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
		
		log.Printf("%s[W-%d] [IO Monad] Successfully processed entire pipeline: %s%s\n", ColorGreen, id, task.FlacPath, ColorReset)
		
		// 6. Output Handling
		if noDB {
			baseName := filepath.Base(task.FlacPath)
			outName := fmt.Sprintf("%s.json", baseName)
			outPath := filepath.Join("..", "testFLAC", outName)
			if err := os.WriteFile(outPath, libOutBuf.Bytes(), 0644); err != nil {
				log.Printf("[Worker %d] Failed to write local JSON: %v\n", id, err)
			} else {
				log.Printf("[Worker %d] Saved local JSON to: %s\n", id, outPath)
			}
		} else {
			// TODO: Implement PostgreSQL UPSERT using libOutBuf.Bytes()
			log.Printf("[Worker %d] PostgreSQL UPSERT not implemented yet. Ignored %d bytes of JSON.\n", id, libOutBuf.Len())
		}
		
		// 7. Cleanup
		for _, shm := range shmMap {
			shm.Close()
		}
	}
}

func main() {
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

		// Enqueue task
		taskQueue <- payload
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
