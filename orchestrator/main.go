package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
)

type TaskPayload struct {
	FlacPath     string `json:"flacPath"`
	FileSize     int64  `json:"fileSize"`
	TargetScript string `json:"targetScript"`
}

const numWorkers = 4

func worker(id int, taskQueue <-chan TaskPayload, wg *sync.WaitGroup, noDB bool) {
	defer wg.Done()
	
	// uuidgen replacement: simple pseudo-uuid or timestamp-based ID generator for tags
	stems := []string{"mix", "bass", "drums", "vocals", "other"}

	for task := range taskQueue {
		log.Printf("[Worker %d] Starting processing: %s\n", id, task.FlacPath)
		
		// 1. Calculate Estimated Size
		estimatedSize := EstimateShmSize(task.FileSize)
		
		// 2. Pre-allocate Shared Memory
		shmMap := make(map[string]*SharedMemory)
		tagsMap := make(map[string]string)
		
		var allocError error
		// Simple unique ID based on pointer/address and timestamp could work,
		// but since it's Windows Local\ namespace, filepath.Base(task.FlacPath) + stem + workerID is okay too.
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
		
		if allocError != nil {
			log.Printf("[Worker %d] SHM Allocation Error: %v\n", id, allocError)
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

		cmdDemucs := exec.Command(absPythonPath, "demucs_worker.py", "--flac-path", task.FlacPath, "--shm-tags", string(tagsJson))
		cmdDemucs.Dir = ".."
		
		var outBuf bytes.Buffer
		cmdDemucs.Stdout = &outBuf
		cmdDemucs.Stderr = os.Stderr
		
		log.Printf("[Worker %d] Running Demucs worker...\n", id)
		if err := cmdDemucs.Run(); err != nil {
			log.Printf("[Worker %d] Demucs processing failed: %v\n", id, err)
			for _, shm := range shmMap {
				shm.Close()
			}
			continue
		}
		
		demucsMetaJson := outBuf.String()
		log.Printf("[Worker %d] Demucs completed successfully.\n", id)
		
		// 4. Freeze Shared Memory (WORM)
		for stem, shm := range shmMap {
			if err := shm.Freeze(); err != nil {
				log.Printf("[Worker %d] Failed to freeze SHM %s: %v\n", id, stem, err)
			}
		}
		
		// 5. Run Librosa Worker
		cmdLibrosa := exec.Command(absPythonPath, "librosa_worker.py", "--shm-metadata", demucsMetaJson)
		cmdLibrosa.Dir = ".."
		
		var libOutBuf bytes.Buffer
		cmdLibrosa.Stdout = &libOutBuf
		cmdLibrosa.Stderr = os.Stderr
		
		log.Printf("[Worker %d] Running Librosa worker...\n", id)
		if err := cmdLibrosa.Run(); err != nil {
			log.Printf("[Worker %d] Librosa processing failed: %v\n", id, err)
			for _, shm := range shmMap {
				shm.Close()
			}
			continue
		}
		
		log.Printf("[Worker %d] Successfully processed entire pipeline: %s\n", id, task.FlacPath)
		
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
