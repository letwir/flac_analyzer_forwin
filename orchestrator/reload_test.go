package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flac_analyzer/orchestrator/dispatcher"
	"flac_analyzer/orchestrator/state"
)

func TestConfigReload_DynamicUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	dbPath := filepath.Join(tmpDir, "test.db")

	initialConfig := `
[orchestrator]
num_workers = 2
demucs_concurrent_limit = 1
max_ram_ratio = 0.50
estimated_worker_ram_gb = 2.0
min_avail_ram_gb = 2.0
shm_allocation_delay_sec = 2
shm_expansion_ratio = 3.5
shm_retry_count = 5
shm_retry_delay_sec = 8
queue_dir = "./queue"
log_level = "info"
skip_dup_by_hash = true
enable_virtual_lock = false

[python_env]
omp_num_threads = "1"
`
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("failed to write initial config: %v", err)
	}

	totalRamGB := 32.0
	numCPU := 8

	_, dispConfig, err := loadAndValidateConfig(configPath, totalRamGB, numCPU, "", nil)
	if err != nil {
		t.Fatalf("loadAndValidateConfig failed: %v", err)
	}

	stateDB, err := state.InitDB(dbPath)
	if err != nil {
		t.Fatalf("failed to init state db: %v", err)
	}
	defer stateDB.Close()

	disp := dispatcher.NewDispatcher(*dispConfig, stateDB)

	currentCfg := disp.GetConfig()
	if currentCfg.DemucsConcurrentLimit != 1 {
		t.Fatalf("expected demucs limit 1, got %d", currentCfg.DemucsConcurrentLimit)
	}
	if currentCfg.LogLevel != dispatcher.LevelInfo {
		t.Fatalf("expected log level info, got %v", currentCfg.LogLevel)
	}

	// 1. Update config file: demucs_concurrent_limit -> 3, log_level -> "debug", skip_dup_by_hash -> false
	updatedConfig := `
[orchestrator]
num_workers = 2
demucs_concurrent_limit = 3
max_ram_ratio = 0.60
estimated_worker_ram_gb = 2.5
min_avail_ram_gb = 3.0
shm_allocation_delay_sec = 1
shm_expansion_ratio = 4.0
shm_retry_count = 3
shm_retry_delay_sec = 5
queue_dir = "./custom_queue"
log_level = "debug"
skip_dup_by_hash = false
enable_virtual_lock = false

[python_env]
omp_num_threads = "2"
`
	if err := os.WriteFile(configPath, []byte(updatedConfig), 0644); err != nil {
		t.Fatalf("failed to write updated config: %v", err)
	}

	// 2. Perform reloadConfiguration
	diff, err := reloadConfiguration(disp, configPath, totalRamGB, numCPU, "", nil)
	if err != nil {
		t.Fatalf("reloadConfiguration failed: %v", err)
	}

	if diff["demucs_concurrent_limit"] != "1 -> 3" {
		t.Errorf("expected diff demucs_concurrent_limit '1 -> 3', got %q", diff["demucs_concurrent_limit"])
	}
	if diff["log_level"] != "info -> debug" {
		t.Errorf("expected diff log_level 'info -> debug', got %q", diff["log_level"])
	}
	if diff["skip_dup_by_hash"] != "true -> false" {
		t.Errorf("expected diff skip_dup_by_hash 'true -> false', got %q", diff["skip_dup_by_hash"])
	}

	// Verify dispatcher state after reload
	reloadedCfg := disp.GetConfig()
	if reloadedCfg.DemucsConcurrentLimit != 3 {
		t.Errorf("expected reloaded demucs limit 3, got %d", reloadedCfg.DemucsConcurrentLimit)
	}
	if reloadedCfg.LogLevel != dispatcher.LevelDebug {
		t.Errorf("expected reloaded log level debug, got %v", reloadedCfg.LogLevel)
	}
	if reloadedCfg.SkipDupByHash != false {
		t.Errorf("expected reloaded skip_dup_by_hash false, got true")
	}
	if reloadedCfg.QueueDir != "./custom_queue" {
		t.Errorf("expected queue_dir './custom_queue', got %q", reloadedCfg.QueueDir)
	}
	if reloadedCfg.PythonEnv["omp_num_threads"] != "2" {
		t.Errorf("expected python_env omp_num_threads '2', got %q", reloadedCfg.PythonEnv["omp_num_threads"])
	}
}

func TestConfigReload_FileWatcher(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	dbPath := filepath.Join(tmpDir, "test_watcher.db")

	initialConfig := `
[orchestrator]
num_workers = 2
demucs_concurrent_limit = 1
log_level = "info"
`
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("failed to write initial config: %v", err)
	}

	totalRamGB := 32.0
	numCPU := 8

	_, dispConfig, err := loadAndValidateConfig(configPath, totalRamGB, numCPU, "", nil)
	if err != nil {
		t.Fatalf("loadAndValidateConfig failed: %v", err)
	}

	stateDB, err := state.InitDB(dbPath)
	if err != nil {
		t.Fatalf("failed to init state db: %v", err)
	}
	defer stateDB.Close()

	disp := dispatcher.NewDispatcher(*dispConfig, stateDB)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startConfigFileWatcher(ctx, configPath, disp, totalRamGB, numCPU, "", nil)

	// Modify config file
	time.Sleep(500 * time.Millisecond)
	updatedConfig := `
[orchestrator]
num_workers = 2
demucs_concurrent_limit = 2
log_level = "warn"
`
	if err := os.WriteFile(configPath, []byte(updatedConfig), 0644); err != nil {
		t.Fatalf("failed to write updated config: %v", err)
	}

	// Wait for FileWatcher ticker (2s) + debounce (300ms)
	deadline := time.Now().Add(5 * time.Second)
	var matched bool
	for time.Now().Before(deadline) {
		cfg := disp.GetConfig()
		if cfg.DemucsConcurrentLimit == 2 && cfg.LogLevel == dispatcher.LevelWarn {
			matched = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if !matched {
		t.Fatalf("FileWatcher did not reload config within deadline, current limit=%d", disp.GetConfig().DemucsConcurrentLimit)
	}
}
