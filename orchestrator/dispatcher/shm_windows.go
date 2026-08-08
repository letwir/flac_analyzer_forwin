package dispatcher

import (
	"fmt"
	"log"
	"reflect"
	"syscall"
	"unsafe"
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procCreateFileMappingW       = kernel32.NewProc("CreateFileMappingW")
	procMapViewOfFile            = kernel32.NewProc("MapViewOfFile")
	procUnmapViewOfFile          = kernel32.NewProc("UnmapViewOfFile")
	procVirtualProtect           = kernel32.NewProc("VirtualProtect")
	procGlobalMemoryStatusEx     = kernel32.NewProc("GlobalMemoryStatusEx")
	procVirtualLock              = kernel32.NewProc("VirtualLock")
	procVirtualUnlock            = kernel32.NewProc("VirtualUnlock")
	procSetProcessWorkingSetSizeEx = kernel32.NewProc("SetProcessWorkingSetSizeEx")
	procGetCurrentProcess        = kernel32.NewProc("GetCurrentProcess")
)

const (
	PAGE_READWRITE = 0x04
	PAGE_READONLY  = 0x02
	FILE_MAP_WRITE = 0x0002
	FILE_MAP_READ  = 0x0004
)

type MemoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func GetAvailableMemory() (uint64, error) {
	var memStatus MemoryStatusEx
	memStatus.Length = uint32(unsafe.Sizeof(memStatus))
	ret, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&memStatus)))
	if ret == 0 {
		return 0, err
	}
	return memStatus.AvailPhys, nil
}

func EnableProcessWorkingSetLock(minMB, maxMB int) error {
	hProc, _, _ := procGetCurrentProcess.Call()
	if hProc == 0 {
		return fmt.Errorf("GetCurrentProcess failed")
	}
	minSize := uintptr(minMB * 1024 * 1024)
	maxSize := uintptr(maxMB * 1024 * 1024)
	// QUOTA_LIMITS_HARDWS_MIN_ENABLE (1) | QUOTA_LIMITS_HARDWS_MAX_DISABLE (8)
	flags := uintptr(0x00000001 | 0x00000008)
	ret, _, errStr := procSetProcessWorkingSetSizeEx.Call(hProc, minSize, maxSize, flags)
	if ret == 0 {
		return fmt.Errorf("SetProcessWorkingSetSizeEx failed: %v", errStr)
	}
	return nil
}

type SharedMemory struct {
	Name     string
	Size     uint32
	handle   syscall.Handle
	addr     uintptr
	data     []byte
	isLocked bool
}

func NewSharedMemory(name string, size uint32) (*SharedMemory, error) {
	name16, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}

	handle, _, errStr := procCreateFileMappingW.Call(
		uintptr(syscall.InvalidHandle),
		0,
		PAGE_READWRITE,
		0,
		uintptr(size),
		uintptr(unsafe.Pointer(name16)),
	)
	if handle == 0 {
		return nil, fmt.Errorf("CreateFileMappingW failed: %v", errStr)
	}

	addr, _, errStr := procMapViewOfFile.Call(
		handle,
		FILE_MAP_WRITE|FILE_MAP_READ,
		0,
		0,
		uintptr(size),
	)
	if addr == 0 {
		syscall.CloseHandle(syscall.Handle(handle))
		return nil, fmt.Errorf("MapViewOfFile failed: %v", errStr)
	}

	var data []byte
	header := (*reflect.SliceHeader)(unsafe.Pointer(&data))
	header.Data = addr
	header.Len = int(size)
	header.Cap = int(size)

	// 物理RAMへの固着 (VirtualLock) を試行しますわ！
	retLock, _, errLock := procVirtualLock.Call(addr, uintptr(size))
	locked := false
	if retLock != 0 {
		locked = true
	} else {
		// 失敗時は警告ログを出力し、通常のページファイルバッキング共有メモリへフォールバックしますの
		log.Printf("[WARN] VirtualLock failed for SHM %s (size %d): %v. Fallback to standard shared memory.", name, size, errLock)
	}

	return &SharedMemory{
		Name:     name,
		Size:     size,
		handle:   syscall.Handle(handle),
		addr:     addr,
		data:     data,
		isLocked: locked,
	}, nil
}

func (shm *SharedMemory) Write(data []byte) error {
	if len(data) > int(shm.Size) {
		return fmt.Errorf("data size %d exceeds shared memory size %d", len(data), shm.Size)
	}
	copy(shm.data, data)
	return nil
}

func (shm *SharedMemory) Freeze() error {
	var oldProtect uint32
	ret, _, errStr := procVirtualProtect.Call(
		shm.addr,
		uintptr(shm.Size),
		PAGE_READONLY,
		uintptr(unsafe.Pointer(&oldProtect)),
	)
	if ret == 0 {
		return fmt.Errorf("VirtualProtect failed: %v", errStr)
	}
	return nil
}

func (shm *SharedMemory) Close() error {
	var lastErr error
	if shm.addr != 0 {
		if shm.isLocked {
			procVirtualUnlock.Call(shm.addr, uintptr(shm.Size))
			shm.isLocked = false
		}
		ret, _, errStr := procUnmapViewOfFile.Call(shm.addr)
		if ret == 0 {
			lastErr = fmt.Errorf("UnmapViewOfFile failed: %v", errStr)
		}
		shm.addr = 0
		shm.data = nil
	}
	if shm.handle != 0 {
		err := syscall.CloseHandle(shm.handle)
		if err != nil {
			lastErr = fmt.Errorf("CloseHandle failed: %v", err)
		}
		shm.handle = 0
	}
	return lastErr
}

