// Package benchmark defines offline, deterministic acceptance checks for
// representative pipeline benchmark results.
package benchmark

import (
	"fmt"
	"math"
	"time"
)

// WorkloadClass identifies a repeatable benchmark shape. Before and after
// windows must use the same class to be comparable.
type WorkloadClass string

const (
	WorkloadShortMix            WorkloadClass = "short_mix"
	WorkloadCueAlbum            WorkloadClass = "cue_album"
	WorkloadLongTrack           WorkloadClass = "long_track"
	WorkloadDemucsStemWavefront WorkloadClass = "demucs_stem_wavefront"
	WorkloadParallel4To8        WorkloadClass = "parallel_4_to_8"
)

var RequiredWorkloads = []WorkloadClass{
	WorkloadShortMix,
	WorkloadCueAlbum,
	WorkloadLongTrack,
	WorkloadDemucsStemWavefront,
	WorkloadParallel4To8,
}

// IsValid reports whether the workload is one of the representative classes.
func (w WorkloadClass) IsValid() bool {
	switch w {
	case WorkloadShortMix, WorkloadCueAlbum, WorkloadLongTrack, WorkloadDemucsStemWavefront, WorkloadParallel4To8:
		return true
	default:
		return false
	}
}

// EvaluateSuite requires evidence for every representative workload instead
// of allowing a favorable single case to stand in for Phase 3 acceptance.
func EvaluateSuite(before, after map[WorkloadClass]MeasurementWindow, limits AcceptanceLimits) ([]Evaluation, error) {
	evaluations := make([]Evaluation, 0, len(RequiredWorkloads))
	for _, workload := range RequiredWorkloads {
		beforeWindow, beforeOK := before[workload]
		afterWindow, afterOK := after[workload]
		if !beforeOK || !afterOK {
			return nil, fmt.Errorf("missing before/after evidence for required workload %q", workload)
		}
		evaluation, err := Evaluate(beforeWindow, afterWindow, limits)
		if err != nil {
			return nil, err
		}
		evaluations = append(evaluations, evaluation)
	}
	return evaluations, nil
}

// MeasurementWindow is an aggregate over one stable benchmark window. It is
// deliberately hardware-agnostic: a collector may produce it from any metric
// source, while acceptance itself remains offline and deterministic.
type MeasurementWindow struct {
	Workload                 WorkloadClass `json:"workload"`
	StartedAt                time.Time     `json:"startedAt"`
	EndedAt                  time.Time     `json:"endedAt"`
	CPUAvgUtilizationPercent float64       `json:"cpuAvgUtilizationPercent"`
	GPUAvgUtilizationPercent float64       `json:"gpuAvgUtilizationPercent"`
	ThroughputPerSecond      float64       `json:"throughputPerSecond"`
	P95WaitSeconds           float64       `json:"p95WaitSeconds"`
	OOMCount                 int           `json:"oomCount"`
	PeakRAMBytes             uint64        `json:"peakRamBytes"`
	PeakVRAMBytes            uint64        `json:"peakVramBytes"`
}

// Validate rejects incomplete or nonsensical input before it is compared.
func (m MeasurementWindow) Validate() error {
	if !m.Workload.IsValid() {
		return fmt.Errorf("unknown workload %q", m.Workload)
	}
	if m.StartedAt.IsZero() || m.EndedAt.IsZero() || !m.EndedAt.After(m.StartedAt) {
		return fmt.Errorf("measurement window must have an end after its start")
	}
	if err := validatePercent("CPU average utilization", m.CPUAvgUtilizationPercent); err != nil {
		return err
	}
	if err := validatePercent("GPU average utilization", m.GPUAvgUtilizationPercent); err != nil {
		return err
	}
	if !finiteNonNegative(m.ThroughputPerSecond) {
		return fmt.Errorf("throughput must be finite and non-negative")
	}
	if !finiteNonNegative(m.P95WaitSeconds) {
		return fmt.Errorf("p95 wait must be finite and non-negative")
	}
	if m.OOMCount < 0 {
		return fmt.Errorf("OOM count must be non-negative")
	}
	return nil
}

// AcceptanceLimits bound the after window. RAM and VRAM limits are required;
// a zero CPU, GPU, or p95 wait limit means that metric is reported but not a
// pass/fail ceiling for this benchmark.
type AcceptanceLimits struct {
	MaxRAMBytes                 uint64  `json:"maxRamBytes"`
	MaxVRAMBytes                uint64  `json:"maxVramBytes"`
	MaxCPUAvgUtilizationPercent float64 `json:"maxCpuAvgUtilizationPercent,omitempty"`
	MaxGPUAvgUtilizationPercent float64 `json:"maxGpuAvgUtilizationPercent,omitempty"`
	MaxP95WaitSeconds           float64 `json:"maxP95WaitSeconds,omitempty"`
}

// Validate rejects a policy which would leave memory unbounded or use invalid
// optional ceilings.
func (l AcceptanceLimits) Validate() error {
	if l.MaxRAMBytes == 0 {
		return fmt.Errorf("max RAM bytes must be greater than zero")
	}
	if l.MaxVRAMBytes == 0 {
		return fmt.Errorf("max VRAM bytes must be greater than zero")
	}
	if err := validateOptionalPercent("max CPU average utilization", l.MaxCPUAvgUtilizationPercent); err != nil {
		return err
	}
	if err := validateOptionalPercent("max GPU average utilization", l.MaxGPUAvgUtilizationPercent); err != nil {
		return err
	}
	if l.MaxP95WaitSeconds != 0 && !finiteNonNegative(l.MaxP95WaitSeconds) {
		return fmt.Errorf("max p95 wait must be finite and non-negative")
	}
	return nil
}

// Evaluation is a serializable, evidence-preserving comparison result.
type Evaluation struct {
	Passed          bool          `json:"passed"`
	Workload        WorkloadClass `json:"workload"`
	ThroughputRatio *float64      `json:"throughputRatio"`
	FailureReasons  []string      `json:"failureReasons"`
}

// Evaluate compares an after window to a before window without reading clocks,
// hardware, files, or global state. A pass requires zero OOMs, no throughput
// regression, and compliance with the declared after-window resource limits.
func Evaluate(before, after MeasurementWindow, limits AcceptanceLimits) (Evaluation, error) {
	if err := before.Validate(); err != nil {
		return Evaluation{}, fmt.Errorf("validate before window: %w", err)
	}
	if err := after.Validate(); err != nil {
		return Evaluation{}, fmt.Errorf("validate after window: %w", err)
	}
	if err := limits.Validate(); err != nil {
		return Evaluation{}, fmt.Errorf("validate acceptance limits: %w", err)
	}
	if before.Workload != after.Workload {
		return Evaluation{}, fmt.Errorf("workload mismatch: before=%q after=%q", before.Workload, after.Workload)
	}

	evaluation := Evaluation{
		Workload:        after.Workload,
		ThroughputRatio: throughputRatio(before.ThroughputPerSecond, after.ThroughputPerSecond),
		FailureReasons:  make([]string, 0),
	}
	if before.OOMCount != 0 {
		evaluation.FailureReasons = append(evaluation.FailureReasons, "before OOM count must be zero")
	}
	if after.OOMCount != 0 {
		evaluation.FailureReasons = append(evaluation.FailureReasons, "after OOM count must be zero")
	}
	if after.ThroughputPerSecond < before.ThroughputPerSecond {
		evaluation.FailureReasons = append(evaluation.FailureReasons, "throughput regressed")
	}
	if after.PeakRAMBytes > limits.MaxRAMBytes {
		evaluation.FailureReasons = append(evaluation.FailureReasons, "peak RAM exceeds limit")
	}
	if after.PeakVRAMBytes > limits.MaxVRAMBytes {
		evaluation.FailureReasons = append(evaluation.FailureReasons, "peak VRAM exceeds limit")
	}
	if limits.MaxCPUAvgUtilizationPercent > 0 && after.CPUAvgUtilizationPercent > limits.MaxCPUAvgUtilizationPercent {
		evaluation.FailureReasons = append(evaluation.FailureReasons, "CPU average utilization exceeds limit")
	}
	if limits.MaxGPUAvgUtilizationPercent > 0 && after.GPUAvgUtilizationPercent > limits.MaxGPUAvgUtilizationPercent {
		evaluation.FailureReasons = append(evaluation.FailureReasons, "GPU average utilization exceeds limit")
	}
	if limits.MaxP95WaitSeconds > 0 && after.P95WaitSeconds > limits.MaxP95WaitSeconds {
		evaluation.FailureReasons = append(evaluation.FailureReasons, "p95 wait exceeds limit")
	}
	evaluation.Passed = len(evaluation.FailureReasons) == 0
	return evaluation, nil
}

func throughputRatio(before, after float64) *float64 {
	if before == 0 {
		return nil
	}
	ratio := after / before
	return &ratio
}

func validatePercent(name string, value float64) error {
	if !finiteNonNegative(value) || value > 100 {
		return fmt.Errorf("%s must be finite and between 0 and 100", name)
	}
	return nil
}

func validateOptionalPercent(name string, value float64) error {
	if value == 0 {
		return nil
	}
	return validatePercent(name, value)
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}
