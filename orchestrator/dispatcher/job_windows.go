package dispatcher

import (
	"fmt"
	"log"
	"sync"
	"syscall"
	"unsafe"
)

var (
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject   = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procOpenProcess              = kernel32.NewProc("OpenProcess")
)

const (
	JobObjectExtendedLimitInformation = 9
	JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x2000
	PROCESS_SET_QUOTA                   = 0x0100
	PROCESS_TERMINATE                   = 0x0001
)

type JOBOBJECT_BASIC_LIMIT_INFORMATION struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type IO_COUNTERS struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type JOBOBJECT_EXTENDED_LIMIT_INFORMATION struct {
	BasicLimitInformation JOBOBJECT_BASIC_LIMIT_INFORMATION
	IoInfo                IO_COUNTERS
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

var (
	globalJobHandle syscall.Handle
	jobOnce         sync.Once
)

// InitGlobalJob initializes a Win32 Job Object for process grouping and automatic cleanup.
func InitGlobalJob() error {
	var initErr error
	jobOnce.Do(func() {
		hJob, _, errStr := procCreateJobObjectW.Call(0, 0)
		if hJob == 0 {
			initErr = fmt.Errorf("CreateJobObjectW failed: %v", errStr)
			return
		}

		var info JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE

		ret, _, errStr := procSetInformationJobObject.Call(
			hJob,
			uintptr(JobObjectExtendedLimitInformation),
			uintptr(unsafe.Pointer(&info)),
			uintptr(unsafe.Sizeof(info)),
		)
		if ret == 0 {
			syscall.CloseHandle(syscall.Handle(hJob))
			initErr = fmt.Errorf("SetInformationJobObject failed: %v", errStr)
			return
		}

		globalJobHandle = syscall.Handle(hJob)
		log.Println("[INFO] Successfully initialized Win32 Job Object (Chrome-style process grouping & auto-kill enabled)")
	})
	return initErr
}

// AssignPidToJob binds a child process (by PID) to the global Win32 Job Object.
func AssignPidToJob(pid int) error {
	if globalJobHandle == 0 || pid <= 0 {
		return nil // Job Object not initialized or invalid PID
	}
	hProc, _, errStr := procOpenProcess.Call(
		uintptr(PROCESS_SET_QUOTA|PROCESS_TERMINATE),
		0,
		uintptr(pid),
	)
	if hProc == 0 {
		return fmt.Errorf("OpenProcess failed for PID %d: %v", pid, errStr)
	}
	defer syscall.CloseHandle(syscall.Handle(hProc))

	ret, _, errStr := procAssignProcessToJobObject.Call(
		uintptr(globalJobHandle),
		hProc,
	)
	if ret == 0 {
		return fmt.Errorf("AssignProcessToJobObject failed for PID %d: %v", pid, errStr)
	}
	return nil
}
