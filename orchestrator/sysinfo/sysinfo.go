package sysinfo

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"unsafe"
)

type MemoryInfo struct {
	MemoryLoad uint32 // Memory usage percentage (0-100)
	TotalPhys  uint64 // Total physical memory in bytes
	AvailPhys  uint64 // Available physical memory in bytes
}

type memoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

var (
	modkernel32              = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryStatusEx = modkernel32.NewProc("GlobalMemoryStatusEx")
	procGetDiskFreeSpaceExW  = modkernel32.NewProc("GetDiskFreeSpaceExW")
)

// DiskInfo contains disk space statistics for a directory/drive in bytes.
type DiskInfo struct {
	FreeBytesAvailable uint64 // Caller available free bytes
	TotalBytes         uint64 // Total disk bytes
	TotalFreeBytes     uint64 // Total free bytes on disk
}

// GetDiskFreeSpace returns disk space metrics for the given path using Win32 GetDiskFreeSpaceExW.
func GetDiskFreeSpace(dirPath string) (*DiskInfo, error) {
	if dirPath == "" {
		dirPath = "."
	}
	pDir, err := syscall.UTF16PtrFromString(dirPath)
	if err != nil {
		return nil, fmt.Errorf("UTF16PtrFromString failed: %w", err)
	}

	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	ret, _, callErr := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(pDir)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if ret == 0 {
		return nil, fmt.Errorf("GetDiskFreeSpaceExW failed for %s: %v", dirPath, callErr)
	}

	return &DiskInfo{
		FreeBytesAvailable: freeBytesAvailable,
		TotalBytes:         totalBytes,
		TotalFreeBytes:     totalFreeBytes,
	}, nil
}

// GetMemoryInfo calls Windows API GlobalMemoryStatusEx to retrieve current system memory metrics.
func GetMemoryInfo() (*MemoryInfo, error) {
	var msX memoryStatusEx
	msX.dwLength = uint32(unsafe.Sizeof(msX))

	ret, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&msX)))
	if ret == 0 {
		return nil, fmt.Errorf("GlobalMemoryStatusEx failed: %v", err)
	}

	return &MemoryInfo{
		MemoryLoad: msX.dwMemoryLoad,
		TotalPhys:  msX.ullTotalPhys,
		AvailPhys:  msX.ullAvailPhys,
	}, nil
}

type SystemSpecs struct {
	CPU      string
	RAMGB    float64
	GPU      string
	OS       string
	Pagefile string
}

// DetectHardwareSpecs queries Windows API and CIM instances to retrieve host machine hardware specifications.
func DetectHardwareSpecs() (*SystemSpecs, error) {
	mem, _ := GetMemoryInfo()
	ramGB := 0.0
	if mem != nil && mem.TotalPhys > 0 {
		ramGB = math.Round(float64(mem.TotalPhys) / (1024 * 1024 * 1024))
	}

	cmd := exec.Command("powershell", "-NoProfile", "-Command", `
		$cpu = (Get-CimInstance Win32_Processor | Select-Object -First 1).Name;
		$gpu = (Get-CimInstance Win32_VideoController | Select-Object -ExpandProperty Name) -join ', ';
		$os = (Get-CimInstance Win32_OperatingSystem).Caption;
		$pagefiles = Get-CimInstance Win32_PageFileUsage | ForEach-Object { "$($_.Name) ($([math]::Round($_.AllocatedBaseSize / 1024, 1)) GB)" };
		$pagefileStr = $pagefiles -join ', ';
		@{ cpu=$cpu; gpu=$gpu; os=$os; pagefile=$pagefileStr } | ConvertTo-Json
	`)
	out, err := cmd.Output()
	specs := &SystemSpecs{RAMGB: ramGB}
	if err == nil {
		var res struct {
			CPU      string `json:"cpu"`
			GPU      string `json:"gpu"`
			OS       string `json:"os"`
			Pagefile string `json:"pagefile"`
		}
		if json.Unmarshal(out, &res) == nil {
			specs.CPU = strings.TrimSpace(res.CPU)
			specs.GPU = strings.TrimSpace(res.GPU)
			specs.OS = strings.TrimSpace(res.OS)
			specs.Pagefile = strings.TrimSpace(res.Pagefile)
		}
	}
	return specs, nil
}

// UpdateHardwareSpecsFile updates the DEV_SPECS section of HARDWARE_SPECS.md dynamically on startup.
func UpdateHardwareSpecsFile(filePath string) error {
	specs, err := DetectHardwareSpecs()
	if err != nil {
		return err
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	devBlock := fmt.Sprintf(`<dev_specs id="DEV_SPECS">
## 開発マシンスペック (Development Host Machine Specifications) [Auto-Detected by Go Orchestrator]
- **CPU**: %s
- **RAM**: %.1f GB Physical DDR4
- **GPU**: %s
- **OS**: %s / PowerShell 7
- **Pagefile**: %s
</dev_specs>`, specs.CPU, specs.RAMGB, specs.GPU, specs.OS, specs.Pagefile)

	re := regexp.MustCompile(`(?s)<dev_specs id="DEV_SPECS">.*?</dev_specs>`)
	newContent := re.ReplaceAllString(string(content), devBlock)

	return os.WriteFile(filePath, []byte(newContent), 0644)
}
