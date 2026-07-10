package dispatcher

import (
	"fmt"
	"reflect"
	"syscall"
	"unsafe"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procCreateFileMappingW = kernel32.NewProc("CreateFileMappingW")
	procMapViewOfFile  = kernel32.NewProc("MapViewOfFile")
	procUnmapViewOfFile = kernel32.NewProc("UnmapViewOfFile")
	procVirtualProtect = kernel32.NewProc("VirtualProtect")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
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

type SharedMemory struct {
	Name    string
	Size    uint32
	handle  syscall.Handle
	addr    uintptr
	data    []byte
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

	return &SharedMemory{
		Name:   name,
		Size:   size,
		handle: syscall.Handle(handle),
		addr:   addr,
		data:   data,
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
