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
	"strconv"
	"sync"
	"syscall"
	"time"

	"math"
	"runtime"

	"flac_analyzer/orchestrator/dispatcher"
	"flac_analyzer/orchestrator/metrics"
	"flac_analyzer/orchestrator/state"
	"flac_analyzer/orchestrator/sysinfo"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/sys/windows/svc/eventlog"
)


type Config struct {
	Orchestrator struct {
		NumWorkers            int     `toml:"num_workers"`
		MaxRamRatio           float64 `toml:"max_ram_ratio"`
		CpuWorkerRatio        float64 `toml:"cpu_worker_ratio"`
		EstimatedWorkerRamGB  float64 `toml:"estimated_worker_ram_gb"`
		MinAvailRamGB         float64 `toml:"min_avail_ram_gb"`
		DemucsConcurrentLimit int     `toml:"demucs_concurrent_limit"`
		ShmAllocationDelaySec int     `toml:"shm_allocation_delay_sec"`
		ShmExpansionRatio     float64 `toml:"shm_expansion_ratio"`
		ShmRetryCount         int     `toml:"shm_retry_count"`
		ShmRetryDelaySec      int     `toml:"shm_retry_delay_sec"`
		QueueDir              string  `toml:"queue_dir"`

		LogLevel              string  `toml:"log_level"`
		SkipDupByHash         *bool   `toml:"skip_dup_by_hash"`
		EnableVirtualLock     *bool   `toml:"enable_virtual_lock"`
		MinWorkingSetMB       int     `toml:"min_working_set_mb"`
		MaxWorkingSetMB       int     `toml:"max_working_set_mb"`
		GatekeeperRetryDelaySec int   `toml:"gatekeeper_retry_delay_sec"`
		ConfigWatchIntervalSec  int   `toml:"config_watch_interval_sec"`
		EnableDlqRetry          *bool `toml:"enable_dlq_retry"`
		DlqRetryIntervalSec     int   `toml:"dlq_retry_interval_sec"`
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

func fatalErrorLog(titleJP, descJP, hintJP, titleEN, descEN, hintEN string, err error) {
	log.Printf("==========================================================================")
	log.Printf(" ❌ 【エラー発生 / ERROR OCCURRED】 %s", titleJP)
	log.Printf(" --------------------------------------------------------------------------")
	log.Printf(" [JP] %s", descJP)
	if hintJP != "" {
		log.Printf(" 💡 [ヒント] %s", hintJP)
	}
	log.Printf(" --------------------------------------------------------------------------")
	log.Printf(" [EN] %s", descEN)
	if hintEN != "" {
		log.Printf(" 💡 [Hint] %s", hintEN)
	}
	if err != nil {
		log.Printf(" 🔍 [Details/詳細] %v", err)
	}
	log.Printf("==========================================================================")
	log.Printf("※ コンソールが即座に閉じるのを防ぐため、5秒間待機いたしますわ...")
	time.Sleep(5 * time.Second)
	log.Fatalf("Orchestrator terminated due to fatal error.")
}

func main() {
	var configPath string
	var logLevelStr string
	flag.StringVar(&configPath, "config", "", "Path to config.toml")
	flag.StringVar(&logLevelStr, "log-level", "", "Log level (debug, info, warn, error)")
	flag.Parse()

	if configPath == "" {
		candidates := []string{"config.toml", "../config.toml", "orchestrator/config.toml"}
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				configPath = candidate
				break
			}
		}
		if configPath == "" {
			configPath = "config.toml"
		}
	}

	// 1. Query system RAM & CPU for dynamic worker calculation
	memInfo, memErr := sysinfo.GetMemoryInfo()
	numCPU := runtime.NumCPU()

	var totalRamGB float64
	if memErr == nil && memInfo.TotalPhys > 0 {
		totalRamGB = float64(memInfo.TotalPhys) / (1024 * 1024 * 1024)
	} else {
		log.Printf("Warning: Failed to query system RAM (%v). Fallback to 32GB.", memErr)
		totalRamGB = 32.0
	}

	// Initialize Windows Event Log
	elog := setupEventLog()
	if elog != nil {
		defer elog.Close()
	}

	// 2. Load and validate config
	cfg, dispConfig, err := loadAndValidateConfig(configPath, totalRamGB, numCPU, logLevelStr, elog)
	if err != nil {
		fatalErrorLog(
			"設定ファイル読み込み・構文エラー",
			fmt.Sprintf("設定ファイル (%s) の読み込みまたは解析に失敗いたしましたわ！", configPath),
			"プロジェクトルートにある 'config.toml.example' をコピーして 'config.toml' を作成し、構文（UTF-8）をご確認くださいませ。",
			"Config file error",
			fmt.Sprintf("Failed to load or parse config file (%s).", configPath),
			"Please copy 'config.toml.example' to 'config.toml' and check TOML syntax (UTF-8).",
			err,
		)
	}

	// Auto-detect host hardware specs and update HARDWARE_SPECS.md
	specsPath := "HARDWARE_SPECS.md"
	if _, err := os.Stat(specsPath); os.IsNotExist(err) {
		if _, err := os.Stat("../HARDWARE_SPECS.md"); err == nil {
			specsPath = "../HARDWARE_SPECS.md"
		}
	}
	if err := sysinfo.UpdateHardwareSpecsFile(specsPath); err != nil {
		log.Printf("Warning: Failed to auto-detect hardware specs for HARDWARE_SPECS.md: %v", err)
	} else {
		log.Printf("Successfully auto-detected hardware specs and updated %s", specsPath)
	}

	// Enable Virtual Lock setting (default: true)
	enableVirtualLock := dispConfig.EnableVirtualLock
	minWS := cfg.Orchestrator.MinWorkingSetMB
	if minWS <= 0 {
		minWS = 512
	}
	maxWS := cfg.Orchestrator.MaxWorkingSetMB
	if maxWS <= 0 {
		calcMaxWS := int(totalRamGB * 1024 * 0.75)
		if calcMaxWS < 16384 {
			calcMaxWS = 16384
		}
		maxWS = calcMaxWS
	}

	if enableVirtualLock {
		// 物理RAMへの固着 (VirtualLock) 用にプロセスのワーキングセットサイズを拡張試行いたしますの
		if err := dispatcher.EnableProcessWorkingSetLock(minWS, maxWS); err != nil {
			log.Printf("[INFO] Working set expansion note: %v (using dynamic auto-expansion Working Set quotas)", err)
		} else {
			log.Printf("[INFO] Successfully expanded process working set quotas (Min: %d MB, Max: %d MB) for physical RAM locking.", minWS, maxWS)
		}
	} else {
		log.Printf("[INFO] VirtualLock disabled via config (enable_virtual_lock = false). Using standard shared memory.")
	}

	// Win32 Job Object の初期化（Chrome風プロセスグループ化 ＆ 自動一括クリーンアップ）
	if err := dispatcher.InitGlobalJob(); err != nil {
		log.Printf("[WARN] Failed to initialize Win32 Job Object: %v", err)
	}

	// 3. Initialize State DB
	dbPath := "orchestrator/orchestrator.db"
	if _, err := os.Stat("orchestrator/orchestrator.db"); os.IsNotExist(err) {
		if _, err := os.Stat("orchestrator.db"); err == nil {
			dbPath = "orchestrator.db"
		} else if _, err := os.Stat("orchestrator"); err == nil {
			dbPath = "orchestrator/orchestrator.db"
		} else {
			dbPath = "orchestrator.db"
		}
	}

	stateDB, err := state.InitDB(dbPath)
	if err != nil {
		fatalErrorLog(
			"状態DB (SQLite) 初期化失敗",
			fmt.Sprintf("タスク状態データベース (%s) の初期化に失敗いたしましたわ！", dbPath),
			"データベースファイルへの書き込み権限や、他プロセスによるロック（二重起動等）をご確認くださいませ。",
			"State DB (SQLite) initialization failed",
			fmt.Sprintf("Failed to initialize state database (%s).", dbPath),
			"Please check file write permissions and ensure another process is not locking the DB.",
			err,
		)
	}
	defer stateDB.Close()

	// 3.1 Reset any stale RUNNING/PENDING tasks from previous interrupted runs
	if resetCount, err := stateDB.ResetStaleTasks(); err != nil {
		log.Printf("Warning: Failed to reset stale tasks: %v", err)
	} else if resetCount > 0 {
		log.Printf("Reset %d interrupted/stale tasks to FAILED state for clean retry", resetCount)
	}

	// 4. Initialize Metrics Server
	go func() {
		log.Println("Starting Prometheus metrics server on :2112/metrics")
		if err := metrics.InitMetricsServer(":2112"); err != nil {
			fatalErrorLog(
				"メトリクスサーバー起動失敗",
				"Prometheus メトリクスサーバー (:2112) の起動に失敗いたしましたわ！",
				"ポート 2112 が他プロセスや Orchestrator の二重起動で使用されていないかご確認くださいませ。",
				"Metrics server failed",
				"Prometheus metrics server (:2112) failed to start.",
				"Please check if port 2112 is already bound by another process or an existing Orchestrator instance.",
				err,
			)
		}
	}()

	// 5. Initialize Dispatcher
	disp := dispatcher.NewDispatcher(*dispConfig, stateDB)
	disp.Start()
	log.Printf("Dispatcher started with %d workers (Demucs Limit: %d, MaxRamRatio: %.1f%%, VirtualLock: %v, LogLevel: %v)\n",
		dispConfig.NumWorkers, dispConfig.DemucsConcurrentLimit, dispConfig.MaxRamRatio*100, enableVirtualLock, dispConfig.LogLevel)

	// 5.1 Start DLQ Auto-Retry Scheduler (startup immediate run + periodic ticker)
	disp.StartDlqRetryScheduler(context.Background())

	// 5.2 Start Config File Watcher for dynamic hot-reloading (10 min / config_watch_interval_sec)
	watcherCtx, cancelWatcher := context.WithCancel(context.Background())
	defer cancelWatcher()
	startConfigFileWatcher(watcherCtx, configPath, disp, totalRamGB, numCPU, logLevelStr, elog, dispConfig.ConfigWatchIntervalSec)

	// 6. Setup Task Receiver and Admin Endpoints
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

	srv := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	go func() {
		log.Println("Listening for tasks on :8080/task (Admin: /reload, /config)")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatalErrorLog(
				"タスク受付 HTTP サーバー起動失敗",
				"タスク受付 HTTP サーバー (:8080) の起動に失敗いたしましたわ！",
				"ポート 8080 が他アプリケーションや既存の Orchestrator プロセスで使用されていないかご確認くださいませ。",
				"Task receiver HTTP server failed to start",
				"HTTP task receiver server (:8080) failed to start.",
				"Please check if port 8080 is already in use by another application or Orchestrator instance.",
				err,
			)
		}
	}()

	// 7. Graceful Shutdown
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

var reloadMutex sync.Mutex

func reloadConfiguration(disp *dispatcher.Dispatcher, configPath string, totalRamGB float64, numCPU int, explicitLogLevel string, elog dispatcher.EventLogger) (map[string]string, error) {
	reloadMutex.Lock()
	defer reloadMutex.Unlock()

	_, newDispConfig, err := loadAndValidateConfig(configPath, totalRamGB, numCPU, explicitLogLevel, elog)
	if err != nil {
		return nil, err
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

func startConfigFileWatcher(ctx context.Context, configPath string, disp *dispatcher.Dispatcher, totalRamGB float64, numCPU int, explicitLogLevel string, elog dispatcher.EventLogger, intervalSec int) {
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

func loadAndValidateConfig(configPath string, totalRamGB float64, numCPU int, explicitLogLevel string, elog dispatcher.EventLogger) (*Config, *dispatcher.Config, error) {
	var cfg Config
	cfgBytes, err := os.ReadFile(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read config file (%s): %w", configPath, err)
	}
	if err := toml.Unmarshal(cfgBytes, &cfg); err != nil {
		return nil, nil, fmt.Errorf("failed to parse TOML syntax (%s): %w", configPath, err)
	}

	// Set defaults for dynamic scaling
	if cfg.Orchestrator.MaxRamRatio <= 0 {
		cfg.Orchestrator.MaxRamRatio = 0.625
	}
	if cfg.Orchestrator.CpuWorkerRatio <= 0 {
		cfg.Orchestrator.CpuWorkerRatio = 0.80
	}
	if cfg.Orchestrator.EstimatedWorkerRamGB <= 0 {
		cfg.Orchestrator.EstimatedWorkerRamGB = 1.75
	}
	if cfg.Orchestrator.MinAvailRamGB <= 0 {
		cfg.Orchestrator.MinAvailRamGB = 1.75
	}
	if cfg.Orchestrator.DemucsConcurrentLimit <= 0 {
		cfg.Orchestrator.DemucsConcurrentLimit = 1
	}
	if cfg.Orchestrator.ShmExpansionRatio <= 0 {
		cfg.Orchestrator.ShmExpansionRatio = 3.5
	}
	if cfg.Orchestrator.ShmRetryCount <= 0 {
		cfg.Orchestrator.ShmRetryCount = 5
	}
	if cfg.Orchestrator.ShmRetryDelaySec <= 0 {
		cfg.Orchestrator.ShmRetryDelaySec = 8
	}

	effectiveRamRatio := cfg.Orchestrator.MaxRamRatio
	if effectiveRamRatio > 0.95 {
		effectiveRamRatio = 0.95
	}
	targetRamGB := totalRamGB * effectiveRamRatio
	ramBasedWorkers := int(math.Floor(targetRamGB / cfg.Orchestrator.EstimatedWorkerRamGB))
	if ramBasedWorkers < 1 {
		ramBasedWorkers = 1
	}

	hardCeilingRamGB := totalRamGB * 0.95
	hardCeilingWorkers := int(math.Floor(hardCeilingRamGB / cfg.Orchestrator.EstimatedWorkerRamGB))

	cpuBasedWorkers := int(math.Floor(float64(numCPU) * cfg.Orchestrator.CpuWorkerRatio))
	if cpuBasedWorkers < 1 {
		cpuBasedWorkers = 1
	}

	if cfg.Orchestrator.NumWorkers <= 0 {
		cfg.Orchestrator.NumWorkers = ramBasedWorkers
		if cpuBasedWorkers < cfg.Orchestrator.NumWorkers {
			cfg.Orchestrator.NumWorkers = cpuBasedWorkers
		}
	} else {
		if cfg.Orchestrator.NumWorkers > ramBasedWorkers {
			cfg.Orchestrator.NumWorkers = ramBasedWorkers
		}
		if cfg.Orchestrator.NumWorkers > hardCeilingWorkers {
			cfg.Orchestrator.NumWorkers = hardCeilingWorkers
		}
	}

	targetLogLevelStr := "info"
	if explicitLogLevel != "" {
		targetLogLevelStr = explicitLogLevel
	} else if cfg.Orchestrator.LogLevel != "" {
		targetLogLevelStr = cfg.Orchestrator.LogLevel
	}
	logLevel := dispatcher.ParseLogLevel(targetLogLevelStr)

	enableVirtualLock := true
	if cfg.Orchestrator.EnableVirtualLock != nil {
		enableVirtualLock = *cfg.Orchestrator.EnableVirtualLock
	}

	skipDup := true
	if cfg.Orchestrator.SkipDupByHash != nil {
		skipDup = *cfg.Orchestrator.SkipDupByHash
	}

	gatekeeperRetryDelay := cfg.Orchestrator.GatekeeperRetryDelaySec
	if gatekeeperRetryDelay <= 0 {
		gatekeeperRetryDelay = 20
	}

	configWatchInterval := cfg.Orchestrator.ConfigWatchIntervalSec
	if configWatchInterval <= 0 {
		configWatchInterval = 600
	}

	enableDlqRetry := true
	if cfg.Orchestrator.EnableDlqRetry != nil {
		enableDlqRetry = *cfg.Orchestrator.EnableDlqRetry
	}

	dlqRetryInterval := cfg.Orchestrator.DlqRetryIntervalSec
	if dlqRetryInterval < 0 {
		dlqRetryInterval = 600
	} else if cfg.Orchestrator.DlqRetryIntervalSec == 0 && cfg.Orchestrator.EnableDlqRetry == nil {
		dlqRetryInterval = 600
	}

	resolvedPythonEnv := resolvePythonEnv(cfg.PythonEnv, numCPU, cfg.Orchestrator.NumWorkers)

	dispConfig := &dispatcher.Config{
		NumWorkers:            cfg.Orchestrator.NumWorkers,
		MaxRamRatio:           effectiveRamRatio,
		EstimatedWorkerRamGB:  cfg.Orchestrator.EstimatedWorkerRamGB,
		MinAvailRamGB:         cfg.Orchestrator.MinAvailRamGB,
		DemucsConcurrentLimit: cfg.Orchestrator.DemucsConcurrentLimit,
		ShmAllocationDelaySec: cfg.Orchestrator.ShmAllocationDelaySec,
		ShmExpansionRatio:     cfg.Orchestrator.ShmExpansionRatio,
		ShmRetryCount:         cfg.Orchestrator.ShmRetryCount,
		ShmRetryDelaySec:      cfg.Orchestrator.ShmRetryDelaySec,
		QueueDir:              cfg.Orchestrator.QueueDir,

		PythonEnv:               resolvedPythonEnv,
		LogLevel:                logLevel,
		EventLog:                elog,
		SkipDupByHash:           skipDup,
		EnableVirtualLock:       enableVirtualLock,
		GatekeeperRetryDelaySec: gatekeeperRetryDelay,
		ConfigWatchIntervalSec:  configWatchInterval,
		EnableDlqRetry:          enableDlqRetry,
		DlqRetryIntervalSec:     dlqRetryInterval,
	}

	return &cfg, dispConfig, nil
}

// resolvePythonEnv derives environment variable mappings deterministically without mutating input
func resolvePythonEnv(raw map[string]string, numCPU, numWorkers int) map[string]string {
	resolved := make(map[string]string)
	for k, v := range raw {
		if v != "0" {
			resolved[k] = v
			continue
		}
		threads := 1
		if numWorkers > 0 {
			threads = numCPU / numWorkers
		}
		if threads < 1 {
			threads = 1
		}
		resolved[k] = strconv.Itoa(threads)
	}
	return resolved
}

