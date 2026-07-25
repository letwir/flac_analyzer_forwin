package sysinfo

import (
	"fmt"
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
)

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
