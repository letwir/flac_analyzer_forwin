package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"flac_analyzer/orchestrator/dispatcher"
	"flac_analyzer/orchestrator/metrics"
	"flac_analyzer/orchestrator/state"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/sys/windows/svc/eventlog"
)

type Config struct {
	Orchestrator struct {
		NumWorkers            int    `toml:"num_workers"`
		DemucsConcurrentLimit int    `toml:"demucs_concurrent_limit"`
		ShmAllocationDelaySec int    `toml:"shm_allocation_delay_sec"`
		QueueDir              string `toml:"queue_dir"`
		LogLevel              string `toml:"log_level"`
		SkipDupByHash         *bool  `toml:"skip_dup_by_hash"`
	} `toml:"orchestrator"`
	PythonEnv map[string]string `toml:"python_env"`
}

func setupEventLog() *eventlog.Log {
	const sourceName = "FlacAnalyzerOrchestrator"
	// イベントソースのインストールを試みます
	// 失敗してもすでに登録済み、または権限不足の可能性があります
	_ = eventlog.InstallAsEventCreate(sourceName, eventlog.Error|eventlog.Warning|eventlog.Info)

	elog, err := eventlog.Open(sourceName)
	if err != nil {
		log.Printf("Warning: Failed to open Windows event log (maybe run as non-admin?): %v\n", err)
		return nil
	}
	return elog
}

func main() {
	var configPath string
	var logLevelStr string
	flag.StringVar(&configPath, "config", "../config.toml", "Path to config.toml")
	flag.StringVar(&logLevelStr, "log-level", "", "Log level (debug, info, warn, error)")
	flag.Parse()

	// 1. Load config
	var cfg Config
	cfgBytes, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalf("Failed to read config file: %v", err)
	}
	if err := toml.Unmarshal(cfgBytes, &cfg); err != nil {
		log.Fatalf("Failed to parse config file: %v", err)
	}

	// Set defaults
	if cfg.Orchestrator.NumWorkers <= 0 {
		cfg.Orchestrator.NumWorkers = 4
	}
	if cfg.Orchestrator.DemucsConcurrentLimit <= 0 {
		cfg.Orchestrator.DemucsConcurrentLimit = 1
	}

	// Determine Log Level
	targetLogLevelStr := "info"
	if logLevelStr != "" {
		targetLogLevelStr = logLevelStr
	} else if cfg.Orchestrator.LogLevel != "" {
		targetLogLevelStr = cfg.Orchestrator.LogLevel
	}
	logLevel := dispatcher.ParseLogLevel(targetLogLevelStr)

	// 2. Initialize State DB
	dbPath := "orchestrator.db"
	stateDB, err := state.InitDB(dbPath)
	if err != nil {
		log.Fatalf("Failed to initialize state DB: %v", err)
	}
	defer stateDB.Close()

	// 2.1 Reset any stale RUNNING/PENDING tasks from previous interrupted runs
	if resetCount, err := stateDB.ResetStaleTasks(); err != nil {
		log.Printf("Warning: Failed to reset stale tasks: %v", err)
	} else if resetCount > 0 {
		log.Printf("Reset %d interrupted/stale tasks to FAILED state for clean retry", resetCount)
	}

	// 3. Initialize Metrics Server
	go func() {
		log.Println("Starting Prometheus metrics server on :2112/metrics")
		if err := metrics.InitMetricsServer(":2112"); err != nil {
			log.Fatalf("Metrics server failed: %v", err)
		}
	}()

	// Initialize Windows Event Log
	elog := setupEventLog()
	if elog != nil {
		defer elog.Close()
	}

	// Skip duplicate by hash setting (default: true)
	skipDup := true
	if cfg.Orchestrator.SkipDupByHash != nil {
		skipDup = *cfg.Orchestrator.SkipDupByHash
	}

	// 4. Initialize Dispatcher
	dispConfig := dispatcher.Config{
		NumWorkers:            cfg.Orchestrator.NumWorkers,
		DemucsConcurrentLimit: cfg.Orchestrator.DemucsConcurrentLimit,
		ShmAllocationDelaySec: cfg.Orchestrator.ShmAllocationDelaySec,
		QueueDir:              cfg.Orchestrator.QueueDir,
		PythonEnv:             cfg.PythonEnv,
		LogLevel:              logLevel,
		EventLog:              elog,
		SkipDupByHash:         skipDup,
	}
	disp := dispatcher.NewDispatcher(dispConfig, stateDB)
	disp.Start()
	log.Printf("Dispatcher started with %d workers (Demucs Limit: %d, LogLevel: %s)\n", dispConfig.NumWorkers, dispConfig.DemucsConcurrentLimit, targetLogLevelStr)

	// 5. Setup Task Receiver Endpoint
	mux := http.NewServeMux()
	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var payload dispatcher.TaskPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		// 1. Inspect CUE / FLAC tags automatically
		cueRes, err := disp.InspectCue(payload.FlacPath)
		if err != nil || cueRes == nil || len(cueRes.Tracks) == 0 {
			log.Printf("CUE inspect warning for %s (fallback to single track): %v", payload.FlacPath, err)
			shouldRun, dbErr := stateDB.CheckOrInsertWithForce(payload.FlacPath, payload.TrackNumber, payload.Force)
			if dbErr != nil {
				log.Printf("DB error for %s: %v", payload.FlacPath, dbErr)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			if !shouldRun {
				w.WriteHeader(http.StatusOK)
				fmt.Fprintln(w, "Skipped: Already processed or in progress")
				return
			}
			disp.Enqueue(payload)
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintln(w, "Task accepted (1 track)")
			return
		}

		// 2. Expand into track-level tasks
		enqueuedCount := 0
		skippedCount := 0

		for _, tr := range cueRes.Tracks {
			taskItem := payload
			taskItem.TrackNumber = tr.TrackNumber
			taskItem.StartSample = tr.StartSample
			taskItem.EndSample = tr.EndSample
			taskItem.Title = tr.Title
			taskItem.Artist = tr.Artist
			taskItem.Album = cueRes.Album
			taskItem.AlbumArtist = cueRes.AlbumArtist

			shouldRun, dbErr := stateDB.CheckOrInsertWithForce(taskItem.FlacPath, taskItem.TrackNumber, taskItem.Force)
			if dbErr != nil {
				log.Printf("DB error for %s track %d: %v", taskItem.FlacPath, taskItem.TrackNumber, dbErr)
				continue
			}

			if !shouldRun {
				skippedCount++
				continue
			}

			disp.Enqueue(taskItem)
			enqueuedCount++
		}

		if enqueuedCount == 0 && skippedCount > 0 {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "Skipped: All %d tracks already processed or in progress\n", skippedCount)
			return
		}

		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, "Task accepted (%d tracks enqueued, %d skipped)\n", enqueuedCount, skippedCount)
	})

	srv := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	go func() {
		log.Println("Listening for tasks on :8080/task")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// 6. Graceful Shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("Shutting down Orchestrator...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	disp.Stop()
	log.Println("Shutdown complete.")
}
