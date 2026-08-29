// Package main provides the top-level orchestration entrypoint, HTTP natural transformation, and lifecycle.
// Morphism Composition & Application Lifecycle (IO Monad)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"flac_analyzer/orchestrator/config"
	"flac_analyzer/orchestrator/dispatcher"
	"flac_analyzer/orchestrator/logger"
	"flac_analyzer/orchestrator/metrics"
	"flac_analyzer/orchestrator/state"
	"flac_analyzer/orchestrator/sysinfo"
)

func main() {
	os.Exit(run())
}

func run() (exitCode int) {
	var configPath string
	var logLevelStr string
	var singleFile string
	var forceSingle bool
	var unregSingle bool
	var checkOnly bool
	flag.StringVar(&configPath, "config", "", "Path to config.toml")
	flag.StringVar(&logLevelStr, "log-level", "", "Log level (debug, info, warn, error)")
	flag.StringVar(&singleFile, "single-file", "", "Analyze one FLAC/CUE file sequentially and exit")
	flag.BoolVar(&forceSingle, "force", false, "Re-analyze completed tracks in single-file mode")
	flag.BoolVar(&unregSingle, "unreg", false, "Run only tracks absent from completed SQLite/PostgreSQL registration")
	flag.BoolVar(&checkOnly, "check-only", false, "Perform -unreg preflight without claims or analysis")
	flag.Parse()
	if err := validateSingleModeOptions(singleFile, forceSingle, unregSingle, checkOnly); err != nil {
		log.Printf("Invalid command options: %v", err)
		return 2
	}

	releaseLock, err := acquireLifecycleLock()
	if err != nil {
		log.Printf("Cannot start orchestrator: %v", err)
		return 1
	}
	defer func() {
		if err := releaseLock(); err != nil {
			log.Printf("Lifecycle lock cleanup failed: %v", err)
			exitCode = 1
		}
	}()

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
	elog := logger.SetupEventLog()
	if elog != nil {
		defer elog.Close()
	}

	// 2. Load and validate config via pure functor
	rawCfg, dispConfig, err := config.LoadFromFile(configPath, totalRamGB, numCPU, logLevelStr, elog)
	if err != nil {
		logger.FatalErrorLog(
			"設定ファイル読み込み・構文エラー",
			fmt.Sprintf("設定ファイル (%s) の読み込みまたは解析に失敗いたしましたわ！", configPath),
			"プロジェクトルートにある 'config.toml.example' をコピーして 'config.toml' を作成し、構文（UTF-8）をご確認くださいませ。",
			"Config file error",
			fmt.Sprintf("Failed to load or parse config file (%s).", configPath),
			"Please copy 'config.toml.example' to 'config.toml' and check TOML syntax (UTF-8).",
			err,
		)
	}

	if checkOnly {
		return runUnregCheckOnly(singleFile, *dispConfig)
	}

	// Auto-detect host hardware specs and update HARDWARE_SPECS.md
	specsPath := "HARDWARE_SPECS.md"
	if _, err := os.Stat(specsPath); os.IsNotExist(err) {
		if _, err := os.Stat("../HARDWARE_SPECS.md"); err == nil {
			specsPath = "../HARDWARE_SPECS.md"
		}
	}
	if singleFile == "" {
		if err := sysinfo.UpdateHardwareSpecsFile(specsPath); err != nil {
			log.Printf("Warning: Failed to auto-detect hardware specs for HARDWARE_SPECS.md: %v", err)
		} else {
			log.Printf("Successfully auto-detected hardware specs and updated %s", specsPath)
		}
	}

	// Enable Virtual Lock setting
	enableVirtualLock := dispConfig.EnableVirtualLock
	minWS := rawCfg.Orchestrator.MinWorkingSetMB
	if minWS <= 0 {
		minWS = 512
	}
	maxWS := rawCfg.Orchestrator.MaxWorkingSetMB
	if maxWS <= 0 {
		calcMaxWS := int(totalRamGB * 1024 * 0.75)
		if calcMaxWS < 16384 {
			calcMaxWS = 16384
		}
		maxWS = calcMaxWS
	}

	if enableVirtualLock {
		if err := dispatcher.EnableProcessWorkingSetLock(minWS, maxWS); err != nil {
			log.Printf("[INFO] Working set expansion note: %v (using dynamic auto-expansion Working Set quotas)", err)
		} else {
			log.Printf("[INFO] Successfully expanded process working set quotas (Min: %d MB, Max: %d MB) for physical RAM locking.", minWS, maxWS)
		}
	} else {
		log.Printf("[INFO] VirtualLock disabled via config (enable_virtual_lock = false). Using standard shared memory.")
	}

	// Win32 Job Object の初期化
	if err := dispatcher.InitGlobalJob(); err != nil {
		log.Printf("[WARN] Failed to initialize Win32 Job Object: %v", err)
	}

	// 3. Initialize State DB
	dbPath := stateDBPath()

	stateDB, err := state.InitDB(dbPath)
	if err != nil {
		logger.FatalErrorLog(
			"状態DB (SQLite) 初期化失敗",
			fmt.Sprintf("タスク状態データベース (%s) の初期化に失敗いたしましたわ！", dbPath),
			"データベースファイルへの書き込み権限や、他プロセスによるロック（二重起動等）をご確認くださいませ。",
			"State DB (SQLite) initialization failed",
			fmt.Sprintf("Failed to initialize state database (%s).", dbPath),
			"Please check file write permissions and ensure another process is not locking the DB.",
			err,
		)
	}
	defer func() {
		if err := stateDB.Close(); err != nil {
			log.Printf("State DB close failed: %v", err)
			exitCode = 1
		}
	}()

	// 3.1 Reset stale tasks from previous interrupted runs
	if singleFile == "" {
		if resetCount, err := stateDB.ResetStaleTasks(); err != nil {
			log.Printf("Warning: Failed to reset stale tasks: %v", err)
		} else if resetCount > 0 {
			log.Printf("Recovered %d interrupted/stale tasks for durable retry", resetCount)
		}

		// 3.2 Purge orphaned cache directories and stale queue JSON files
		dispatcher.PurgeOrphanedQueueAndCacheFiles(dispConfig.QueueDir, 1*time.Hour)
	}

	if singleFile != "" {
		dispConfig.NumWorkers = 1
		dispConfig.DemucsConcurrentLimit = 1
		disp := dispatcher.NewDispatcher(*dispConfig, stateDB)
		payload, err := dispatcher.NewSingleFilePayload(singleFile, forceSingle)
		if err != nil {
			log.Printf("Single-file input error: %v", err)
			disp.Stop()
			return 1
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		releaseExecutionContext := disp.BindSingleExecutionContext(ctx)
		defer releaseExecutionContext()
		failed := false
		tasks, err := disp.ExpandSingleFile(payload)
		if err != nil {
			log.Printf("Single-file CUE inspection failed: %v", err)
			disp.Stop()
			return 1
		}
		if unregSingle {
			preflight, err := disp.FilterUnregisteredSingleTasks(ctx, tasks)
			if err != nil {
				log.Printf("Unregistered preflight failed: %v", err)
				disp.Stop()
				return 1
			}
			log.Printf(
				"Unregistered preflight complete: eligible=%d sqlite_skipped=%d postgres_skipped=%d",
				len(preflight.Eligible),
				preflight.SQLiteSkipped,
				preflight.PostgreSQLSkipped,
			)
			tasks = preflight.Eligible
		}
		for _, task := range tasks {
			if err := ctx.Err(); err != nil {
				failed = true
				log.Printf("Single-file batch cancelled before track %d: %v", task.TrackNumber, err)
				break
			}
			executed, runErr := disp.RunSingleTask(ctx, task)
			if runErr != nil {
				failed = true
				log.Printf("Single task failed: %s track %d: %v", task.FlacPath, task.TrackNumber, runErr)
			} else if !executed {
				log.Printf("Single task skipped (already completed): %s track %d", task.FlacPath, task.TrackNumber)
			}
		}
		disp.Stop()
		if failed {
			return 1
		}
		return 0
	}

	// 4. Initialize Metrics Server
	go func() {
		log.Println("Starting Prometheus metrics server on :2112/metrics")
		if err := metrics.InitMetricsServer(":2112"); err != nil {
			logger.FatalErrorLog(
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

	// 5.1 Start DLQ Auto-Retry Scheduler
	disp.StartDlqRetryScheduler(context.Background())

	// 5.2 Start Config File Watcher
	watcherCtx, cancelWatcher := context.WithCancel(context.Background())
	defer cancelWatcher()
	startConfigFileWatcher(watcherCtx, configPath, disp, totalRamGB, numCPU, logLevelStr, elog, dispConfig.ConfigWatchIntervalSec)

	// 6. Setup Task Receiver and Admin HTTP Server
	srv := setupTaskServer(disp, configPath, totalRamGB, numCPU, logLevelStr, elog)

	go func() {
		log.Println("Listening for tasks on :8080/task (Admin: /reload, /config)")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.FatalErrorLog(
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
	return 0
}

func validateSingleModeOptions(singleFile string, force, unreg, checkOnly bool) error {
	if unreg && singleFile == "" {
		return errors.New("-unreg requires -single-file")
	}
	if unreg && force {
		return errors.New("-unreg and -force cannot be combined")
	}
	if checkOnly && !unreg {
		return errors.New("-check-only requires -unreg")
	}
	return nil
}

func stateDBPath() string {
	if _, err := os.Stat("orchestrator/orchestrator.db"); err == nil {
		return "orchestrator/orchestrator.db"
	}
	if _, err := os.Stat("orchestrator.db"); err == nil {
		return "orchestrator.db"
	}
	if _, err := os.Stat("orchestrator"); err == nil {
		return "orchestrator/orchestrator.db"
	}
	return "orchestrator.db"
}

func runUnregCheckOnly(singleFile string, cfg dispatcher.Config) (exitCode int) {
	stateDB, err := state.OpenReadOnly(stateDBPath())
	if err != nil {
		log.Printf("Unregistered check-only could not open SQLite state read-only: %v", err)
		return 1
	}
	defer func() {
		if err := stateDB.Close(); err != nil {
			log.Printf("Read-only SQLite close failed: %v", err)
			exitCode = 1
		}
	}()

	payload, err := dispatcher.NewSingleFilePayload(singleFile, false)
	if err != nil {
		log.Printf("Single-file input error: %v", err)
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	preflight, err := dispatcher.CheckUnregisteredSingleFile(ctx, cfg, stateDB, payload)
	if err != nil {
		log.Printf("Unregistered check-only failed: %v", err)
		return 1
	}
	log.Printf(
		"Unregistered check-only complete: eligible=%d sqlite_skipped=%d postgres_skipped=%d",
		len(preflight.Eligible),
		preflight.SQLiteSkipped,
		preflight.PostgreSQLSkipped,
	)
	for _, task := range preflight.Eligible {
		log.Printf("Eligible unregistered track: %s track %d", task.FlacPath, task.TrackNumber)
	}
	return 0
}
