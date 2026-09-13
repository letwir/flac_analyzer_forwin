package config

import (
	"math"
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

func TestNormalizeConfig_Timeouts(t *testing.T) {
	// Case 1: Default fallback when zero or missing
	rawDefault := &RawConfig{}
	cfgDefault := NormalizeConfig(rawDefault, 32.0, 8, "", nil)
	if cfgDefault.FeatureExtractTimeoutSec != 300 {
		t.Errorf("Expected FeatureExtractTimeoutSec = 300, got %d", cfgDefault.FeatureExtractTimeoutSec)
	}
	if cfgDefault.DemucsTimeoutSec != 300 {
		t.Errorf("Expected DemucsTimeoutSec = 300, got %d", cfgDefault.DemucsTimeoutSec)
	}
	if cfgDefault.AdaptiveTimeoutRatio != 1.5 {
		t.Errorf("Expected AdaptiveTimeoutRatio = 1.5, got %v", cfgDefault.AdaptiveTimeoutRatio)
	}
	if cfgDefault.MaxAdaptiveTimeoutSec != 7200 {
		t.Errorf("Expected MaxAdaptiveTimeoutSec = 7200, got %d", cfgDefault.MaxAdaptiveTimeoutSec)
	}

	// Case 2: Custom values parsed correctly
	rawCustom := &RawConfig{
		Orchestrator: OrchestratorConfig{
			FeatureExtractTimeoutSec: 600,
			DemucsTimeoutSec:         450,
			AdaptiveTimeoutRatio:     2.0,
			MaxAdaptiveTimeoutSec:    10800,
		},
	}
	cfgCustom := NormalizeConfig(rawCustom, 32.0, 8, "", nil)
	if cfgCustom.FeatureExtractTimeoutSec != 600 {
		t.Errorf("Expected FeatureExtractTimeoutSec = 600, got %d", cfgCustom.FeatureExtractTimeoutSec)
	}
	if cfgCustom.DemucsTimeoutSec != 450 {
		t.Errorf("Expected DemucsTimeoutSec = 450, got %d", cfgCustom.DemucsTimeoutSec)
	}
	if cfgCustom.AdaptiveTimeoutRatio != 2.0 {
		t.Errorf("Expected AdaptiveTimeoutRatio = 2.0, got %v", cfgCustom.AdaptiveTimeoutRatio)
	}
	if cfgCustom.MaxAdaptiveTimeoutSec != 10800 {
		t.Errorf("Expected MaxAdaptiveTimeoutSec = 10800, got %d", cfgCustom.MaxAdaptiveTimeoutSec)
	}
}

func TestNormalizeConfig_DedicatedVramTotalGB(t *testing.T) {
	cases := []struct {
		name     string
		input    float64
		expected float64
	}{
		{"valid_16gb", 16.0, 16.0},
		{"valid_lower_bound", 0.5, 0.5},
		{"valid_upper_bound", 512.0, 512.0},
		{"zero_default", 0.0, 0.0},
		{"negative", -4.0, 0.0},
		{"nan", math.NaN(), 0.0},
		{"pos_inf", math.Inf(1), 0.0},
		{"neg_inf", math.Inf(-1), 0.0},
		{"exceeds_bound", 512.1, 0.0},
		{"huge_exceeds_bound", 1024.0, 0.0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := &RawConfig{
				Orchestrator: OrchestratorConfig{
					DedicatedVramTotalGB: tc.input,
				},
			}
			cfg := NormalizeConfig(raw, 32.0, 8, "", nil)
			if cfg.DedicatedVramTotalGB != tc.expected {
				t.Errorf("For input %v, expected %v, got %v", tc.input, tc.expected, cfg.DedicatedVramTotalGB)
			}
		})
	}
}

func TestNormalizeConfigPinsDemucsToOneDedicatedSlot(t *testing.T) {
	raw := &RawConfig{Orchestrator: OrchestratorConfig{DemucsConcurrentLimit: 4, DemucsDaemonCapacity: 4}}
	cfg := NormalizeConfig(raw, 32.0, 8, "", nil)
	if cfg.DemucsConcurrentLimit != 1 || cfg.DemucsDaemonCapacity != 1 {
		t.Fatalf("Demucs config = concurrent:%d daemon:%d, want 1:1", cfg.DemucsConcurrentLimit, cfg.DemucsDaemonCapacity)
	}
}
