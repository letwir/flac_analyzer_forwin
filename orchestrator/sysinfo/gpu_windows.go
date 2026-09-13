package sysinfo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Mor: SystemState -> GpuMetrics
// Functor: f_gpu ∘ g_perfcounter
// Semantics: P1-I2 GPU sysinfo source contract. Dedicated and shared VRAM are tracked separately.
// Availability is strictly valid only when dedicated capacity and usage are verified, fresh,
// consistent, and unambiguous. Failures, staleness, or multi-adapter ambiguity produce unknown availability.

// VramProvenance tracks the authoritative origin of VRAM measurements.
type VramProvenance string

const (
	ProvenanceNone           VramProvenance = "none"
	ProvenanceUnknown        VramProvenance = "unknown"
	ProvenanceManualOverride VramProvenance = "manual_override"
	ProvenancePerfCounter    VramProvenance = "perf_counter"
	ProvenanceWmiVideoCtrl   VramProvenance = "wmi_video_controller"
	ProvenanceCalculated     VramProvenance = "calculated"
)

// GpuTotalSource represents the origin of total VRAM capacity for backward compatibility.
type GpuTotalSource string

const (
	GpuTotalSourceUnknown  GpuTotalSource = "unknown"
	GpuTotalSourceConfig   GpuTotalSource = "config"
	GpuTotalSourceHardware GpuTotalSource = "hardware"
)

const (
	// DefaultGpuStaleThreshold defines the duration after which cached metrics expire and become unknown.
	DefaultGpuStaleThreshold = 10 * time.Second

	// Wmi32BitOverflowSentinel marks the 4GB 32-bit uint32 ceiling reported by WMI Win32_VideoController.AdapterRAM.
	Wmi32BitOverflowSentinel      = 4294967295
	maxDedicatedVramOverrideBytes = 512 * 1024 * 1024 * 1024
)

// GpuMetrics represents the observable GPU state under the P1-I2 source contract.
type GpuMetrics struct {
	// Public compatibility fields
	UtilizationPercent  float64 // GPU engine utilization rate (0.0 - 100.0%)
	DedicatedUsedBytes  uint64  // Dedicated video memory (VRAM) used in bytes
	DedicatedTotalBytes uint64  // Dedicated video memory (VRAM) total capacity in bytes (0 if unknown)
	SharedUsedBytes     uint64  // Shared system memory used by GPU in bytes
	TotalCommittedBytes uint64  // Total committed video memory in bytes
	AvailableVramBytes  uint64  // Dedicated available VRAM in bytes (0 if unknown, never MaxUint64)

	// Explicit validity flags
	UtilizationValid        bool // true if UtilizationPercent is verified and valid
	UtilizationKnown        bool // Alias for UtilizationValid
	DedicatedCapacityValid  bool // true if DedicatedTotalBytes is verified and authoritative
	DedicatedTotalValid     bool // Alias for DedicatedCapacityValid
	DedicatedTotalKnown     bool // Alias for DedicatedCapacityValid
	DedicatedUsageValid     bool // true if DedicatedUsedBytes is verified and valid
	DedicatedUsedValid      bool // Alias for DedicatedUsageValid
	DedicatedUsedKnown      bool // Alias for DedicatedUsageValid
	DedicatedAvailableValid bool // true if AvailableVramBytes is verified dedicated available VRAM
	AvailableVramValid      bool // Convenience alias for DedicatedAvailableValid
	AvailableVramKnown      bool // Convenience alias for DedicatedAvailableValid
	SharedUsageValid        bool // true if SharedUsedBytes is verified and valid
	SharedUsedValid         bool // Convenience alias for SharedUsageValid
	SharedUsedKnown         bool // Convenience alias for SharedUsageValid
	TotalCommittedValid     bool // true if TotalCommittedBytes is verified and valid
	TotalCommittedKnown     bool // Convenience alias for TotalCommittedValid

	// Explicit provenance metadata
	DedicatedCapacityProvenance  VramProvenance // Origin of DedicatedTotalBytes
	DedicatedTotalSource         GpuTotalSource // Backward-compatibility alias for DedicatedCapacityProvenance
	DedicatedUsageProvenance     VramProvenance // Origin of DedicatedUsedBytes
	DedicatedAvailableProvenance VramProvenance // Origin of AvailableVramBytes
	SharedUsageProvenance        VramProvenance // Origin of SharedUsedBytes

	// Safety and diagnostic attributes
	CollectedAt           time.Time // Timestamp when the sample was captured
	AdapterCount          int       // Number of detected display adapters
	AdapterName           string    // Name(s) of detected display adapters
	MultiAdapterAmbiguity bool      // true if multiple adapters prevent unambiguous single-GPU attribution
	Inconsistent          bool      // true if counter metrics are mathematically contradictory
	StatusDetail          string    // Diagnostic detail explaining failure, ambiguity, or invalidity
	CollectionError       error     // Error associated with sample collection if failed
}

// IsDedicatedAvailable returns true if dedicated VRAM availability is verified and non-stale.
func (m *GpuMetrics) IsDedicatedAvailable() bool {
	return m != nil && m.DedicatedAvailableValid
}

// IsStale checks whether the metric sample exceeds the given staleness threshold relative to now.
func (m *GpuMetrics) IsStale(now time.Time, threshold time.Duration) bool {
	if m == nil || m.CollectedAt.IsZero() {
		return true
	}
	if threshold <= 0 {
		threshold = DefaultGpuStaleThreshold
	}
	return now.Sub(m.CollectedAt) > threshold
}

// IsStaleNow checks whether the metric sample is stale relative to time.Now().
func (m *GpuMetrics) IsStaleNow(threshold time.Duration) bool {
	return m.IsStale(time.Now(), threshold)
}

var (
	modpdh                          = syscall.NewLazyDLL("pdh.dll")
	procPdhOpenQueryW               = modpdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW       = modpdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData         = modpdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterValue = modpdh.NewProc("PdhGetFormattedCounterValue")
	procPdhCloseQuery               = modpdh.NewProc("PdhCloseQuery")
)

const (
	PDH_FMT_DOUBLE = 0x00000200
	PDH_FMT_LARGE  = 0x00000400
)

type pdhFmtCounterValueDouble struct {
	CStatus     uint32
	Padding     uint32
	DoubleValue float64
}

type pdhFmtCounterValueLarge struct {
	CStatus    uint32
	Padding    uint32
	LargeValue int64
}

// Global state for lock-free cached GPU metrics and explicit capacity overrides
var (
	latestGpuMetrics      atomic.Pointer[GpuMetrics]
	gpuCollectorOnce      sync.Once
	dedicatedVramOverride atomic.Uint64
)

// SetDedicatedVramOverride configures an explicit total dedicated VRAM override in bytes.
// Setting 0 clears the override.
func SetDedicatedVramOverride(bytes uint64) {
	if bytes > maxDedicatedVramOverrideBytes {
		bytes = 0
	}
	dedicatedVramOverride.Store(bytes)
	latestGpuMetrics.Store(NewUnknownGpuMetrics("dedicated VRAM override changed", time.Now()))
}

// GetDedicatedVramOverride returns the currently configured dedicated VRAM override in bytes.
func GetDedicatedVramOverride() uint64 {
	return dedicatedVramOverride.Load()
}

// ClearDedicatedVramOverride resets any configured dedicated VRAM override.
func ClearDedicatedVramOverride() {
	SetDedicatedVramOverride(0)
}

// SetDedicatedVramTotalGB sets the dedicated VRAM total capacity override in gigabytes.
func SetDedicatedVramTotalGB(gb float64) {
	if !IsValidDedicatedVramOverride(gb) {
		ClearDedicatedVramOverride()
		return
	}
	SetDedicatedVramOverride(uint64(gb * 1024 * 1024 * 1024))
}

// GetDedicatedVramTotalGB gets the currently configured dedicated VRAM total capacity in gigabytes.
func GetDedicatedVramTotalGB() float64 {
	overrideBytes := GetDedicatedVramOverride()
	if overrideBytes == 0 {
		return 0
	}
	return float64(overrideBytes) / (1024 * 1024 * 1024)
}

// IsValidDedicatedVramOverride validates whether a dedicated VRAM total override in GB is finite, positive, and bounded.
func IsValidDedicatedVramOverride(gb float64) bool {
	return !math.IsNaN(gb) && !math.IsInf(gb, 0) && gb > 0 && gb <= 512.0
}

// NewUnknownGpuMetrics constructs a fail-safe GpuMetrics instance with all validities false
// and AvailableVramBytes set to 0 (replacing permissive MaxUint64).
func NewUnknownGpuMetrics(reason string, t time.Time) *GpuMetrics {
	var collErr error
	if reason != "" {
		collErr = errors.New(reason)
	}
	return &GpuMetrics{
		UtilizationPercent:           0.0,
		DedicatedUsedBytes:           0,
		DedicatedTotalBytes:          0,
		SharedUsedBytes:              0,
		TotalCommittedBytes:          0,
		AvailableVramBytes:           0,
		UtilizationValid:             false,
		UtilizationKnown:             false,
		DedicatedCapacityValid:       false,
		DedicatedTotalValid:          false,
		DedicatedTotalKnown:          false,
		DedicatedUsageValid:          false,
		DedicatedUsedValid:           false,
		DedicatedUsedKnown:           false,
		DedicatedAvailableValid:      false,
		AvailableVramValid:           false,
		AvailableVramKnown:           false,
		SharedUsageValid:             false,
		SharedUsedValid:              false,
		SharedUsedKnown:              false,
		TotalCommittedValid:          false,
		TotalCommittedKnown:          false,
		DedicatedCapacityProvenance:  ProvenanceNone,
		DedicatedTotalSource:         GpuTotalSourceUnknown,
		DedicatedUsageProvenance:     ProvenanceNone,
		DedicatedAvailableProvenance: ProvenanceNone,
		SharedUsageProvenance:        ProvenanceNone,
		CollectedAt:                  t,
		StatusDetail:                 reason,
		CollectionError:              collErr,
	}
}

func init() {
	envVal := os.Getenv("FLAC_ANALYZER_DEDICATED_VRAM_BYTES")
	if envVal == "" {
		envVal = os.Getenv("FLAC_DEDICATED_VRAM_BYTES")
	}
	if envVal != "" {
		if parsed, err := strconv.ParseUint(strings.TrimSpace(envVal), 10, 64); err == nil && parsed > 0 && parsed <= maxDedicatedVramOverrideBytes {
			dedicatedVramOverride.Store(parsed)
		}
	} else {
		envValGB := os.Getenv("FLAC_ANALYZER_DEDICATED_VRAM_GB")
		if envValGB == "" {
			envValGB = os.Getenv("FLAC_DEDICATED_VRAM_GB")
		}
		if envValGB != "" {
			if parsedGB, err := strconv.ParseFloat(strings.TrimSpace(envValGB), 64); err == nil && IsValidDedicatedVramOverride(parsedGB) {
				dedicatedVramOverride.Store(uint64(parsedGB * 1024 * 1024 * 1024))
			}
		}
	}
	latestGpuMetrics.Store(NewUnknownGpuMetrics("initial uncollected state", time.Time{}))
}

// GetLatestGpuMetrics returns the most recently collected GPU metrics in a lock-free manner.
// If metrics are uninitialized, absent, or stale relative to DefaultGpuStaleThreshold,
// dedicated availability is returned as unknown.
func GetLatestGpuMetrics() *GpuMetrics {
	m := latestGpuMetrics.Load()
	if m == nil {
		return NewUnknownGpuMetrics("metrics uninitialized", time.Time{})
	}
	if m.CollectedAt.IsZero() {
		return m
	}
	if m.IsStale(time.Now(), DefaultGpuStaleThreshold) {
		staleCopy := *m
		staleCopy.UtilizationValid = false
		staleCopy.UtilizationKnown = false
		staleCopy.DedicatedAvailableValid = false
		staleCopy.AvailableVramValid = false
		staleCopy.AvailableVramKnown = false
		staleCopy.AvailableVramBytes = 0
		staleCopy.DedicatedAvailableProvenance = ProvenanceUnknown
		staleCopy.StatusDetail = fmt.Sprintf("stale metrics: collected at %v exceeds %v threshold", m.CollectedAt, DefaultGpuStaleThreshold)
		staleCopy.CollectionError = fmt.Errorf("gpu metrics stale: last collected %v ago", time.Since(m.CollectedAt))
		return &staleCopy
	}
	return m
}

// RawGpuData holds raw performance counter and WMI observations before pure contract evaluation.
type RawGpuData struct {
	UtilizationPercent float64
	DedicatedUsage     int64
	SharedUsage        int64
	TotalCommitted     int64
	AdapterRAM         int64
	AdapterCount       int
	AdapterName        string
	CollectedAt        time.Time
	QueryErr           error
}

// EvaluateGpuMetricsPure evaluates raw GPU observations against the P1-I2 source contract.
// Pure, deterministic function with zero I/O or ambient side-effects.
// Mor: (RawGpuData, OverrideBytes, Now, StaleThreshold) -> GpuMetrics
func EvaluateGpuMetricsPure(raw RawGpuData, override uint64, now time.Time, staleThreshold time.Duration) *GpuMetrics {
	m := &GpuMetrics{
		CollectedAt:                  raw.CollectedAt,
		AdapterCount:                 raw.AdapterCount,
		AdapterName:                  raw.AdapterName,
		DedicatedCapacityProvenance:  ProvenanceUnknown,
		DedicatedTotalSource:         GpuTotalSourceUnknown,
		DedicatedUsageProvenance:     ProvenanceUnknown,
		DedicatedAvailableProvenance: ProvenanceUnknown,
		SharedUsageProvenance:        ProvenanceUnknown,
	}

	// 1. Failure check: Query failure forces unknown dedicated availability
	if raw.QueryErr != nil {
		m.StatusDetail = fmt.Sprintf("query failure: %v", raw.QueryErr)
		m.CollectionError = raw.QueryErr
		return m
	}

	// 2. Utilization clamping (0.0 - 100.0%)
	util := raw.UtilizationPercent
	if util < 0.0 {
		util = 0.0
	} else if util > 100.0 {
		util = 100.0
	}
	m.UtilizationPercent = util
	m.UtilizationValid = true
	m.UtilizationKnown = true

	// 3. Shared usage and total committed (Strict separation: shared never substitutes dedicated)
	if raw.SharedUsage >= 0 {
		m.SharedUsedBytes = uint64(raw.SharedUsage)
		m.SharedUsageValid = true
		m.SharedUsedValid = true
		m.SharedUsedKnown = true
		m.SharedUsageProvenance = ProvenancePerfCounter
	} else {
		m.Inconsistent = true
	}

	if raw.TotalCommitted >= 0 {
		m.TotalCommittedBytes = uint64(raw.TotalCommitted)
		m.TotalCommittedValid = true
		m.TotalCommittedKnown = true
	} else {
		m.Inconsistent = true
	}

	// 4. Dedicated usage validity
	if raw.DedicatedUsage >= 0 {
		m.DedicatedUsedBytes = uint64(raw.DedicatedUsage)
		m.DedicatedUsageValid = true
		m.DedicatedUsedValid = true
		m.DedicatedUsedKnown = true
		m.DedicatedUsageProvenance = ProvenancePerfCounter
	} else {
		m.DedicatedUsageValid = false
		m.DedicatedUsedValid = false
		m.DedicatedUsedKnown = false
		m.Inconsistent = true
		m.StatusDetail = "negative dedicated usage reported"
	}

	// 5. Dedicated capacity determination & provenance
	// Replace any AdapterRAM / used*2 capacity inference:
	// Explicit override is the only capacity source currently accepted by this
	// collector. Win32_VideoController.AdapterRAM is a 32-bit field and cannot
	// represent modern dedicated VRAM reliably, so it is diagnostic-only.
	// No used*2 capacity inference is ever performed.
	if override > 0 {
		m.DedicatedTotalBytes = override
		m.DedicatedCapacityValid = true
		m.DedicatedTotalValid = true
		m.DedicatedTotalKnown = true
		m.DedicatedCapacityProvenance = ProvenanceManualOverride
		m.DedicatedTotalSource = GpuTotalSourceConfig
	} else {
		m.DedicatedTotalBytes = 0
		m.DedicatedCapacityValid = false
		m.DedicatedTotalValid = false
		m.DedicatedTotalKnown = false
		m.DedicatedCapacityProvenance = ProvenanceUnknown
		m.DedicatedTotalSource = GpuTotalSourceUnknown
		if raw.AdapterRAM >= Wmi32BitOverflowSentinel {
			m.StatusDetail = "wmi AdapterRAM 32-bit overflow; dedicated capacity requires explicit override or direct DXGI source"
		} else {
			m.StatusDetail = "dedicated capacity unavailable: requires explicit override or direct DXGI source"
		}
	}

	// 6. Inconsistency check:
	// Dedicated usage must not exceed total capacity when capacity is valid.
	// Total committed video memory must not be less than dedicated usage alone.
	if m.DedicatedCapacityValid && m.DedicatedUsageValid && m.DedicatedUsedBytes > m.DedicatedTotalBytes {
		m.Inconsistent = true
		if m.StatusDetail == "" {
			m.StatusDetail = fmt.Sprintf("inconsistent metrics: dedicated used (%d) > total capacity (%d)",
				m.DedicatedUsedBytes, m.DedicatedTotalBytes)
		}
	}
	if m.TotalCommittedValid && m.DedicatedUsageValid && m.TotalCommittedBytes < m.DedicatedUsedBytes {
		m.Inconsistent = true
		if m.StatusDetail == "" {
			m.StatusDetail = fmt.Sprintf("inconsistent metrics: total committed (%d) < dedicated used (%d)",
				m.TotalCommittedBytes, m.DedicatedUsedBytes)
		}
	}

	// 7. Multi-adapter ambiguity & adapter presence check:
	// If multiple adapters are detected, counter sums are ambiguous and must produce unknown dedicated availability.
	// If 0 adapters are detected, no GPU exists, producing unknown availability.
	if raw.AdapterCount > 1 {
		m.MultiAdapterAmbiguity = true
		if m.StatusDetail == "" {
			m.StatusDetail = fmt.Sprintf("multi-adapter ambiguity: detected %d adapters (%s)",
				raw.AdapterCount, raw.AdapterName)
		}
	} else if raw.AdapterCount <= 0 {
		if m.StatusDetail == "" {
			m.StatusDetail = "no display adapter detected"
		}
	}

	// 8. Staleness check:
	// A stale value must produce unknown dedicated availability.
	isStale := false
	if staleThreshold > 0 && !raw.CollectedAt.IsZero() {
		if now.Sub(raw.CollectedAt) > staleThreshold {
			isStale = true
			if m.StatusDetail == "" {
				m.StatusDetail = fmt.Sprintf("stale metrics: sample age %v exceeds threshold %v",
					now.Sub(raw.CollectedAt), staleThreshold)
			}
		}
	} else if raw.CollectedAt.IsZero() {
		isStale = true
	}

	// 9. Dedicated availability computation:
	// "Dedicated and shared must remain separate; a failure, stale value, or multi-adapter ambiguity must produce unknown dedicated availability."
	if !m.DedicatedCapacityValid || !m.DedicatedUsageValid || m.Inconsistent || m.MultiAdapterAmbiguity || raw.AdapterCount != 1 || isStale {
		m.DedicatedAvailableValid = false
		m.AvailableVramValid = false
		m.AvailableVramKnown = false
		m.AvailableVramBytes = 0
		m.DedicatedAvailableProvenance = ProvenanceUnknown
		return m
	}

	// Valid calculation: DedicatedTotalBytes >= DedicatedUsedBytes
	// Notice: DedicatedTotalBytes == DedicatedUsedBytes yields AvailableVramBytes == 0 with DedicatedAvailableValid == true (Valid zero!)
	m.AvailableVramBytes = m.DedicatedTotalBytes - m.DedicatedUsedBytes
	m.DedicatedAvailableValid = true
	m.AvailableVramValid = true
	m.AvailableVramKnown = true
	m.DedicatedAvailableProvenance = ProvenanceCalculated

	return m
}

// RawGpuReport represents deserialized CIM/WMI report for backward compatibility.
type RawGpuReport struct {
	AdapterCount int     `json:"adapter_count"`
	Util         float64 `json:"util"`
	DedUsed      int64   `json:"ded_used"`
	ShrUsed      int64   `json:"shr_used"`
	TotCom       int64   `json:"tot_com"`
	AdapterRAM   int64   `json:"adapter_ram"`
	AdapterName  string  `json:"adapter_name"`
}

// ComputeGpuMetricsPure provides a backward-compatible pure function converting RawGpuReport to GpuMetrics.
func ComputeGpuMetricsPure(raw RawGpuReport, overrideGB float64, collectedAt time.Time) *GpuMetrics {
	overrideBytes := uint64(0)
	if IsValidDedicatedVramOverride(overrideGB) {
		overrideBytes = uint64(overrideGB * 1024 * 1024 * 1024)
	}
	rawData := RawGpuData{
		UtilizationPercent: raw.Util,
		DedicatedUsage:     raw.DedUsed,
		SharedUsage:        raw.ShrUsed,
		TotalCommitted:     raw.TotCom,
		AdapterRAM:         raw.AdapterRAM,
		AdapterCount:       raw.AdapterCount,
		AdapterName:        raw.AdapterName,
		CollectedAt:        collectedAt,
	}
	return EvaluateGpuMetricsPure(rawData, overrideBytes, collectedAt, DefaultGpuStaleThreshold)
}

// StartGpuCollectorDaemon initializes and runs background periodic GPU metrics collection.
func StartGpuCollectorDaemon(ctx context.Context, interval time.Duration) {
	gpuCollectorOnce.Do(func() {
		if interval <= 0 {
			interval = 2 * time.Second
		}
		// Synchronous first collection attempt
		if initialMetrics, err := FetchGpuMetricsComplex(); err == nil && initialMetrics != nil {
			latestGpuMetrics.Store(initialMetrics)
		} else if err != nil {
			latestGpuMetrics.Store(NewUnknownGpuMetrics(fmt.Sprintf("initial collection failed: %v", err), time.Now()))
		}

		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					metrics, err := FetchGpuMetricsComplex()
					if err == nil && metrics != nil {
						latestGpuMetrics.Store(metrics)
					} else if err != nil {
						latestGpuMetrics.Store(NewUnknownGpuMetrics(fmt.Sprintf("periodic collection failed: %v", err), time.Now()))
					}
				}
			}
		}()
	})
}

// FetchGpuMetricsComplex queries GPU utilization and VRAM using CIM / WMI and evaluates them against the source contract.
func FetchGpuMetricsComplex() (*GpuMetrics, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", `
		$ErrorActionPreference = 'Stop';
		$gpuEngine = Get-CimInstance Win32_PerfFormattedData_GPUPerformanceCounters_GPUEngine | Measure-Object -Property UtilizationPercentage -Maximum | Select-Object -ExpandProperty Maximum;
		$mem = Get-CimInstance Win32_PerfFormattedData_GPUPerformanceCounters_GPUAdapterMemory | Measure-Object -Property DedicatedUsage, SharedUsage, TotalCommitted -Sum;
		if ($null -eq $gpuEngine -or $null -eq $mem -or $mem.Count -ne 3) { throw 'incomplete GPU performance-counter sample' }
		$adapters = @(Get-CimInstance Win32_VideoController | Where-Object { $_.PNPDeviceID -like 'PCI*' });
		if ($adapters.Count -eq 0) { $adapters = @(Get-CimInstance Win32_VideoController) };
		$adapterCount = $adapters.Count;
		$adapterNames = ($adapters | ForEach-Object { $_.Name }) -join '; ';
		$vAdapter = if ($adapterCount -eq 1) { $adapters[0].AdapterRAM } else { 0 };
		@{
			util = if ($gpuEngine) { [math]::Min(100.0, [double]$gpuEngine) } else { 0.0 };
			ded_used = if ($mem[0].Sum) { [int64]$mem[0].Sum } else { 0 };
			shr_used = if ($mem[1].Sum) { [int64]$mem[1].Sum } else { 0 };
			tot_com = if ($mem[2].Sum) { [int64]$mem[2].Sum } else { 0 };
			adapter_ram = if ($vAdapter) { [int64]$vAdapter } else { 0 };
			adapter_count = [int]$adapterCount;
			adapter_name = [string]$adapterNames;
		} | ConvertTo-Json
	`)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to query GPU performance counters via CIM (%v): %w", ctx.Err(), err)
	}

	var res struct {
		Util         float64 `json:"util"`
		DedUsed      int64   `json:"ded_used"`
		ShrUsed      int64   `json:"shr_used"`
		TotCom       int64   `json:"tot_com"`
		AdapterRam   int64   `json:"adapter_ram"`
		AdapterCount int     `json:"adapter_count"`
		AdapterName  string  `json:"adapter_name"`
	}

	cleanOut := strings.TrimSpace(string(out))
	if err := json.Unmarshal([]byte(cleanOut), &res); err != nil {
		return nil, fmt.Errorf("failed to parse GPU performance counter JSON (%s): %w", cleanOut, err)
	}

	now := time.Now()
	raw := RawGpuData{
		UtilizationPercent: res.Util,
		DedicatedUsage:     res.DedUsed,
		SharedUsage:        res.ShrUsed,
		TotalCommitted:     res.TotCom,
		AdapterRAM:         res.AdapterRam,
		AdapterCount:       res.AdapterCount,
		AdapterName:        res.AdapterName,
		CollectedAt:        now,
	}

	return EvaluateGpuMetricsPure(raw, GetDedicatedVramOverride(), now, DefaultGpuStaleThreshold), nil
}
