package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
)

type TaskPayload struct {
	FlacPath     string `json:"flacPath"`
	TargetScript string `json:"targetScript"`
}

const numWorkers = 4

func worker(id int, taskQueue <-chan TaskPayload, wg *sync.WaitGroup) {
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
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		
		if err := cmd.Run(); err != nil {
			log.Printf("[Worker %d] Error processing %s: %v\n", id, task.FlacPath, err)
		} else {
			log.Printf("[Worker %d] Successfully processed: %s\n", id, task.FlacPath)
		}
	}
}

func main() {
	taskQueue := make(chan TaskPayload, 1000)
	var wg sync.WaitGroup

	// Start worker pool
	for i := 1; i <= numWorkers; i++ {
		wg.Add(1)
		go worker(i, taskQueue, &wg)
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
