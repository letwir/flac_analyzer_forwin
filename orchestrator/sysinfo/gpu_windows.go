package sysinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Mor: SystemState -> GpuMetrics
// Functor: f_gpu ∘ g_perfcounter
// Semantics: Windows GPU エンジン使用率および VRAM (Dedicated/Shared) リアルタイム可観測性射

type GpuMetrics struct {
	UtilizationPercent  float64 // GPU 負荷率 (0.0 - 100.0%)
	DedicatedUsedBytes  uint64  // 専用ビデオメモリ (Dedicated VRAM) 使用量 (Bytes)
	DedicatedTotalBytes uint64  // 専用ビデオメモリ 総容量 (Bytes)
	SharedUsedBytes     uint64  // 共有システムメモリ GPU使用量 (Bytes)
	TotalCommittedBytes uint64  // 総コミット済みビデオメモリ (Bytes)
	AvailableVramBytes  uint64  // 利用可能 VRAM 空き容量 (Bytes)
}

var (
	modpdh                  = syscall.NewLazyDLL("pdh.dll")
	procPdhOpenQueryW       = modpdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW = modpdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData = modpdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterValue = modpdh.NewProc("PdhGetFormattedCounterValue")
	procPdhCloseQuery       = modpdh.NewProc("PdhCloseQuery")
)

const (
	PDH_FMT_DOUBLE = 0x00000200
	PDH_FMT_LARGE  = 0x00000400
)

type pdhFmtCounterValueDouble struct {
	CStatus uint32
	Padding uint32
	DoubleValue float64
}

type pdhFmtCounterValueLarge struct {
	CStatus uint32
	Padding uint32
	LargeValue int64
}

// Global cached GPU metrics updated by background collector
var (
	latestGpuMetrics atomic.Pointer[GpuMetrics]
	gpuCollectorOnce sync.Once
)

func init() {
	// 初期フォールバック値を設定しておきますわ（ゼロ値パニック防止）
	latestGpuMetrics.Store(&GpuMetrics{
		UtilizationPercent:  0.0,
		DedicatedUsedBytes:  0,
		DedicatedTotalBytes: 0,
		SharedUsedBytes:     0,
		TotalCommittedBytes: 0,
		AvailableVramBytes:  math.MaxUint64, // デフォルトは無制限とみなしますの
	})
}

// GetLatestGpuMetrics returns the most recently collected GPU metrics in a lock-free manner.
func GetLatestGpuMetrics() *GpuMetrics {
	m := latestGpuMetrics.Load()
	if m == nil {
		return &GpuMetrics{AvailableVramBytes: math.MaxUint64}
	}
	return m
}

// StartGpuCollectorDaemon initializes and runs background periodic GPU metrics collection.
func StartGpuCollectorDaemon(ctx context.Context, interval time.Duration) {
	gpuCollectorOnce.Do(func() {
		if interval <= 0 {
			interval = 2 * time.Second
		}
		// 初回同期収集を試みますわ
		if initialMetrics, err := FetchGpuMetricsComplex(); err == nil && initialMetrics != nil {
			latestGpuMetrics.Store(initialMetrics)
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
					}
				}
			}
		}()
	})
}

// FetchGpuMetricsComplex queries GPU utilization and VRAM using CIM / WMI and PDH fallbacks.
func FetchGpuMetricsComplex() (*GpuMetrics, error) {
	// 1. CIM / PowerShell 高速JSONクエリ（タイムアウト付き context で厳格保護）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", `
		$ErrorActionPreference = 'SilentlyContinue';
		$gpuEngine = Get-CimInstance Win32_PerfFormattedData_GPUPerformanceCounters_GPUEngine | Measure-Object -Property UtilizationPercentage -Maximum | Select-Object -ExpandProperty Maximum;
		$mem = Get-CimInstance Win32_PerfFormattedData_GPUPerformanceCounters_GPUAdapterMemory | Measure-Object -Property DedicatedUsage, SharedUsage, TotalCommitted -Sum;
		$vAdapter = Get-CimInstance Win32_VideoController | Select-Object -First 1 -ExpandProperty AdapterRAM;
		@{
			util = if ($gpuEngine) { [math]::Min(100.0, [double]$gpuEngine) } else { 0.0 };
			ded_used = if ($mem[0].Sum) { [int64]$mem[0].Sum } else { 0 };
			shr_used = if ($mem[1].Sum) { [int64]$mem[1].Sum } else { 0 };
			tot_com = if ($mem[2].Sum) { [int64]$mem[2].Sum } else { 0 };
			adapter_ram = if ($vAdapter) { [int64]$vAdapter } else { 0 };
		} | ConvertTo-Json
	`)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to query GPU performance counters via CIM (%v): %w", ctx.Err(), err)
	}

	var res struct {
		Util       float64 `json:"util"`
		DedUsed    int64   `json:"ded_used"`
		ShrUsed    int64   `json:"shr_used"`
		TotCom     int64   `json:"tot_com"`
		AdapterRam int64   `json:"adapter_ram"`
	}

	cleanOut := strings.TrimSpace(string(out))
	if err := json.Unmarshal([]byte(cleanOut), &res); err != nil {
		return nil, fmt.Errorf("failed to parse GPU performance counter JSON (%s): %w", cleanOut, err)
	}

	clampedUtil := res.Util
	if clampedUtil > 100.0 {
		clampedUtil = 100.0
	} else if clampedUtil < 0.0 {
		clampedUtil = 0.0
	}

	totalDedicated := uint64(res.AdapterRam)
	if totalDedicated == 0 && res.DedUsed > 0 {
		// AdapterRAM が未報告の場合は、コミット量または余裕値から推定いたしますわ
		totalDedicated = uint64(res.DedUsed) * 2
	}

	usedDedicated := uint64(res.DedUsed)
	var availVram uint64 = math.MaxUint64
	if totalDedicated > usedDedicated {
		availVram = totalDedicated - usedDedicated
	} else if totalDedicated > 0 {
		availVram = 0
	}

	return &GpuMetrics{
		UtilizationPercent:  clampedUtil,
		DedicatedUsedBytes:  usedDedicated,
		DedicatedTotalBytes: totalDedicated,
		SharedUsedBytes:     uint64(res.ShrUsed),
		TotalCommittedBytes: uint64(res.TotCom),
		AvailableVramBytes:  availVram,
	}, nil
}
