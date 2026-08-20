// Package main provides the top-level orchestration entrypoint, HTTP natural transformation, and lifecycle.
// Natural Transformation: HTTP Requests -> Dispatcher Ingest Queue / Config Hot Reload
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"flac_analyzer/orchestrator/config"
	"flac_analyzer/orchestrator/dispatcher"
	"flac_analyzer/orchestrator/logger"
	"flac_analyzer/orchestrator/state"
)

var reloadMutex sync.Mutex

// reloadConfiguration reloads the TOML configuration from disk and applies changes dynamically.
// SideEffectFn: reloadConfiguration
func reloadConfiguration(
	disp *dispatcher.Dispatcher,
	configPath string,
	totalRamGB float64,
	numCPU int,
	explicitLogLevel string,
	elog logger.EventLogger,
) (map[string]string, error) {
	reloadMutex.Lock()
	defer reloadMutex.Unlock()

	_, newDispConfig, err := config.LoadFromFile(configPath, totalRamGB, numCPU, explicitLogLevel, elog)
	if err != nil {
		return nil, fmt.Errorf("reload failed: %w", err)
	}

	diff := disp.UpdateConfig(*newDispConfig)
	if len(diff) > 0 {
		log.Printf("==========================================================================")
		log.Printf(" 🔄 [Config Reload] Configuration reloaded successfully (%d changes)", len(diff))
		for k, v := range diff {
			log.Printf("    * %s: %s", k, v)
		}
		log.Printf("==========================================================================")
	} else {
		log.Printf("[Config Reload] Config reloaded (no parameter changes detected)")
	}
	return diff, nil
}

// startConfigFileWatcher monitors config.toml on disk and auto-reloads upon atomic file modifications.
// SideEffectFn: startConfigFileWatcher
func startConfigFileWatcher(
	ctx context.Context,
	configPath string,
	disp *dispatcher.Dispatcher,
	totalRamGB float64,
	numCPU int,
	explicitLogLevel string,
	elog logger.EventLogger,
	intervalSec int,
) {
	if intervalSec <= 0 {
		intervalSec = 600
	}
	go func() {
		var lastModTime time.Time
		if fi, err := os.Stat(configPath); err == nil {
			lastModTime = fi.ModTime()
		}

		ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fi, err := os.Stat(configPath)
				if err != nil {
					continue
				}
				if !lastModTime.IsZero() && fi.ModTime().After(lastModTime) {
					lastModTime = fi.ModTime()
					// Small debounce sleep for atomic editor saves
					time.Sleep(300 * time.Millisecond)
					log.Printf("[FileWatcher] Detected change in %s, reloading configuration...", configPath)
					if _, err := reloadConfiguration(disp, configPath, totalRamGB, numCPU, explicitLogLevel, elog); err != nil {
						log.Printf("[WARN] [FileWatcher] Config reload failed: %v", err)
					}
				} else if lastModTime.IsZero() {
					lastModTime = fi.ModTime()
				}
			}
		}
	}()
}

// setupTaskServer constructs and routes all administrative and task receiver HTTP endpoints.
// SideEffectFn: setupTaskServer
func setupTaskServer(
	disp *dispatcher.Dispatcher,
	stateDB *state.DB,
	configPath string,
	totalRamGB float64,
	numCPU int,
	logLevelStr string,
	elog logger.EventLogger,
) *http.Server {
	mux := http.NewServeMux()
	cueInspectSem := make(chan struct{}, 8)

	// POST /task
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

		// 1. Inspect CUE / FLAC tags automatically (throttled by semaphore)
		cueInspectSem <- struct{}{}
		cueRes, err := disp.InspectCue(payload.FlacPath)
		<-cueInspectSem

		if err != nil || cueRes == nil || len(cueRes.Tracks) == 0 {
			warnMsg := "CUE not present or failed to parse"
			if err != nil {
				warnMsg = fmt.Sprintf("CUE inspect warning: %v", err)
			}
			log.Printf("Fallback to single track processing for %s: %s", payload.FlacPath, warnMsg)
			cueRes = &dispatcher.CueInspectResult{
				Status:   "fallback",
				Filepath: payload.FlacPath,
				Tracks: []dispatcher.CueInspectTrack{
					{
						TrackNumber: 1,
						StartSample: 0,
						EndSample:   0,
						Title:       dispatcher.FlexibleString(payload.Title),
						Artist:      dispatcher.FlexibleString(payload.Artist),
					},
				},
			}
		}

		// 2. Expand into track-level tasks
		disp.RegisterFileTracks(payload.FlacPath, len(cueRes.Tracks))
		enqueuedCount := 0
		skippedCount := 0

		for _, tr := range cueRes.Tracks {
			taskItem := payload
			taskItem.TrackNumber = tr.TrackNumber
			taskItem.StartSample = tr.StartSample
			taskItem.EndSample = tr.EndSample
			taskItem.Title = tr.Title.String()
			taskItem.Artist = tr.Artist.String()
			taskItem.Album = cueRes.Album.String()
			taskItem.AlbumArtist = cueRes.AlbumArtist.String()

			shouldRun, dbErr := stateDB.CheckOrInsertWithForce(taskItem.FlacPath, taskItem.TrackNumber, taskItem.Force)
			if dbErr != nil {
				log.Printf("DB error for %s track %d: %v", taskItem.FlacPath, taskItem.TrackNumber, dbErr)
				continue
			}

			if !shouldRun {
				skippedCount++
				continue
			}

			_ = disp.Enqueue(taskItem)
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

	// POST /reload (Manual Dynamic Config Reload)
	mux.HandleFunc("/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		diff, err := reloadConfiguration(disp, configPath, totalRamGB, numCPU, logLevelStr, elog)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "error",
				"message": err.Error(),
			})
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":         "success",
			"message":        "Configuration reloaded successfully",
			"changes":        diff,
			"current_config": disp.GetConfig(),
		})
	})

	// GET /config (Inspect current active configuration)
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(disp.GetConfig())
	})

	return &http.Server{
		Addr:         ":8080",
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}
