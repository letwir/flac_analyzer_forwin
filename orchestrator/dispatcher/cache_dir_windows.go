//go:build windows

package dispatcher

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// processStillActive is the GetExitCodeProcess sentinel (STILL_ACTIVE) for a running process.
const processStillActive uint32 = 259

// isReparsePoint reports whether path is a junction/symlink/mount point, using the
// file attributes so that Go's version-dependent Lstat mode bits are not relied upon.
func isReparsePoint(path string) (bool, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, fmt.Errorf("encode path %q: %w", path, err)
	}
	attrs, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return false, fmt.Errorf("get attributes of %q: %w", path, err)
	}
	return attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

func openQueryProcess(pid int) (windows.Handle, error) {
	return windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
}

// processStartUnixNano returns the creation time of process pid as Unix nanoseconds.
func processStartUnixNano(pid int) (int64, error) {
	handle, err := openQueryProcess(pid)
	if err != nil {
		return 0, fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(handle)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return 0, fmt.Errorf("get process times for %d: %w", pid, err)
	}
	return creation.Nanoseconds(), nil
}

// processMatchesOwner is deliberately conservative: it answers false (dead) only when the
// process provably no longer exists, has exited, or has a different start time (PID reuse).
// Any inability to inspect it (e.g. access denied) is treated as alive so that a live
// owner's directory is never purged.
func processMatchesOwner(owner cacheOwner) bool {
	handle, err := openQueryProcess(owner.PID)
	if err != nil {
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(handle)
	// A terminated process whose object is still held open can keep reporting exit code 259
	// (STILL_ACTIVE) and a matching creation time. A zero-timeout wait on the process handle
	// distinguishes "signaled/terminated" from "running" reliably.
	if event, waitErr := windows.WaitForSingleObject(handle, 0); waitErr == nil && event == windows.WAIT_OBJECT_0 {
		return false
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return true
	}
	if exitCode != processStillActive {
		return false
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return true
	}
	if owner.StartTimeUnixNano != 0 && creation.Nanoseconds() != owner.StartTimeUnixNano {
		return false
	}
	return true
}
