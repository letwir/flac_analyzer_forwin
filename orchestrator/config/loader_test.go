package config

import (
	"testing"
)

func TestComputeDynamicWorkers(t *testing.T) {
	raw := &RawConfig{
		Orchestrator: OrchestratorConfig{
			MaxRamRatio:          0.625,
			CpuWorkerRatio:       0.80,
			EstimatedWorkerRamGB: 1.75,
		},
	}

	// 64GB RAM, 16 Cores
	workers, ratio := ComputeDynamicWorkers(raw, 64.0, 16)
	if ratio != 0.625 {
		t.Errorf("Expected ratio 0.625, got %v", ratio)
	}
	// 64 * 0.625 = 40GB -> 40 / 1.75 = 22.8 -> 22 workers
	// 16 * 0.80 = 12 workers
	// min(22, 12) = 12
	if workers != 12 {
		t.Errorf("Expected 12 workers based on CPU limit, got %d", workers)
	}
}

func TestResolvePythonEnv(t *testing.T) {
	raw := map[string]string{
		"OMP_NUM_THREADS": "0",
		"STATIC_VAR":      "custom_val",
	}

	resolved := ResolvePythonEnv(raw, 16, 4)
	if resolved["OMP_NUM_THREADS"] != "4" {
		t.Errorf("Expected OMP_NUM_THREADS = 4 (16/4), got %s", resolved["OMP_NUM_THREADS"])
	}
	if resolved["STATIC_VAR"] != "custom_val" {
		t.Errorf("Expected STATIC_VAR = custom_val, got %s", resolved["STATIC_VAR"])
	}
}
