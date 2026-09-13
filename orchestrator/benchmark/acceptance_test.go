package benchmark

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEvaluateAcceptsNonRegressingBoundedWindow(t *testing.T) {
	before := validWindow()
	after := before
	after.EndedAt = after.EndedAt.Add(time.Minute)
	after.ThroughputPerSecond = 3.5
	after.PeakRAMBytes = 700
	after.PeakVRAMBytes = 600

	evaluation, err := Evaluate(before, after, validLimits())
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !evaluation.Passed {
		t.Fatalf("Evaluate() passed = false, reasons = %v", evaluation.FailureReasons)
	}
	if evaluation.ThroughputRatio == nil || *evaluation.ThroughputRatio != 1.4 {
		t.Fatalf("ThroughputRatio = %v, want 1.4", evaluation.ThroughputRatio)
	}
}

func TestEvaluateAcceptsNewThroughputFromZeroBaseline(t *testing.T) {
	before := validWindow()
	before.ThroughputPerSecond = 0
	after := before
	after.EndedAt = after.EndedAt.Add(time.Minute)
	after.ThroughputPerSecond = 1

	evaluation, err := Evaluate(before, after, validLimits())
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !evaluation.Passed {
		t.Fatalf("Evaluate() passed = false, reasons = %v", evaluation.FailureReasons)
	}
	if evaluation.ThroughputRatio != nil {
		t.Fatalf("ThroughputRatio = %v, want nil for zero baseline", *evaluation.ThroughputRatio)
	}
	if _, err := json.Marshal(evaluation); err != nil {
		t.Fatalf("Marshal(Evaluation) error = %v", err)
	}
}

func TestEvaluateRejectsEachAcceptanceFailure(t *testing.T) {
	before := validWindow()
	before.OOMCount = 1
	after := validWindow()
	after.EndedAt = after.EndedAt.Add(time.Minute)
	after.OOMCount = 1
	after.ThroughputPerSecond = 2.4
	after.PeakRAMBytes = 1001
	after.PeakVRAMBytes = 1001
	after.CPUAvgUtilizationPercent = 81
	after.GPUAvgUtilizationPercent = 71
	after.P95WaitSeconds = 3
	limits := validLimits()
	limits.MaxCPUAvgUtilizationPercent = 80
	limits.MaxGPUAvgUtilizationPercent = 70
	limits.MaxP95WaitSeconds = 2

	evaluation, err := Evaluate(before, after, limits)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if evaluation.Passed {
		t.Fatal("Evaluate() passed = true, want false")
	}
	for _, want := range []string{
		"before OOM count must be zero",
		"after OOM count must be zero",
		"throughput regressed",
		"peak RAM exceeds limit",
		"peak VRAM exceeds limit",
		"CPU average utilization exceeds limit",
		"GPU average utilization exceeds limit",
		"p95 wait exceeds limit",
	} {
		if !contains(evaluation.FailureReasons, want) {
			t.Errorf("FailureReasons = %v, missing %q", evaluation.FailureReasons, want)
		}
	}
}

func TestEvaluateRejectsIncomparableOrInvalidInput(t *testing.T) {
	before := validWindow()
	after := validWindow()
	after.Workload = WorkloadLongTrack
	if _, err := Evaluate(before, after, validLimits()); err == nil || !strings.Contains(err.Error(), "workload mismatch") {
		t.Fatalf("Evaluate() error = %v, want workload mismatch", err)
	}

	before = validWindow()
	before.CPUAvgUtilizationPercent = 101
	if _, err := Evaluate(before, validWindow(), validLimits()); err == nil || !strings.Contains(err.Error(), "CPU average utilization") {
		t.Fatalf("Evaluate() error = %v, want CPU validation error", err)
	}

	limits := validLimits()
	limits.MaxVRAMBytes = 0
	if _, err := Evaluate(validWindow(), validWindow(), limits); err == nil || !strings.Contains(err.Error(), "max VRAM") {
		t.Fatalf("Evaluate() error = %v, want VRAM limit validation error", err)
	}
}

func TestEvaluateSuiteRequiresEveryRepresentativeWorkload(t *testing.T) {
	before := make(map[WorkloadClass]MeasurementWindow)
	after := make(map[WorkloadClass]MeasurementWindow)
	for _, workload := range RequiredWorkloads {
		window := validWindow()
		window.Workload = workload
		before[workload] = window
		after[workload] = window
	}
	evaluations, err := EvaluateSuite(before, after, validLimits())
	if err != nil || len(evaluations) != len(RequiredWorkloads) {
		t.Fatalf("suite evaluations=%d err=%v", len(evaluations), err)
	}
	delete(after, WorkloadParallel4To8)
	if _, err := EvaluateSuite(before, after, validLimits()); err == nil {
		t.Fatal("suite accepted missing 4-8 parallel evidence")
	}
}

func validWindow() MeasurementWindow {
	start := time.Date(2026, time.September, 13, 0, 0, 0, 0, time.UTC)
	return MeasurementWindow{
		Workload:                 WorkloadDemucsStemWavefront,
		StartedAt:                start,
		EndedAt:                  start.Add(time.Minute),
		CPUAvgUtilizationPercent: 75,
		GPUAvgUtilizationPercent: 65,
		ThroughputPerSecond:      2.5,
		P95WaitSeconds:           1.5,
		PeakRAMBytes:             800,
		PeakVRAMBytes:            700,
	}
}

func validLimits() AcceptanceLimits {
	return AcceptanceLimits{MaxRAMBytes: 1000, MaxVRAMBytes: 1000}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
