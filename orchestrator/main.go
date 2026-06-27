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
	TargetScript string `json:"targetScript"`
}

const numWorkers = 4

func worker(id int, taskQueue <-chan TaskPayload, wg *sync.WaitGroup, noDB bool) {
	defer wg.Done()
	for task := range taskQueue {
		var script string = task.TargetScript
		if script == "" {
			script = "main.py"
		}
		log.Printf("[Worker %d] Starting processing: %s (Script: %s)\n", id, task.FlacPath, script)
		cmd := exec.Command("python", script, task.FlacPath, "--resume")
		
		// Set working directory to project root
		cmd.Dir = ".."
		
		var outBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = os.Stderr
		
		if err := cmd.Run(); err != nil {
			log.Printf("[Worker %d] Error processing %s: %v\n", id, task.FlacPath, err)
		} else {
			log.Printf("[Worker %d] Successfully processed: %s\n", id, task.FlacPath)
			if noDB {
				baseName := filepath.Base(task.FlacPath)
				outName := fmt.Sprintf("%s.json", baseName)
				outPath := filepath.Join("..", "testFLAC", outName)
				if err := os.WriteFile(outPath, outBuf.Bytes(), 0644); err != nil {
					log.Printf("[Worker %d] Failed to write local JSON: %v\n", id, err)
				} else {
					log.Printf("[Worker %d] Saved local JSON to: %s\n", id, outPath)
				}
			} else {
				// TODO: Implement PostgreSQL UPSERT using outBuf.Bytes()
				log.Printf("[Worker %d] PostgreSQL UPSERT not implemented yet. Ignored %d bytes of JSON.\n", id, outBuf.Len())
			}
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
