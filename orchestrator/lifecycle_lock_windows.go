package main

import (
	"fmt"
	"runtime"

	"golang.org/x/sys/windows"
)

const orchestratorMutexName = `Local\FlacAnalyzerOrchestratorLifetime`

func acquireLifecycleLock() (func() error, error) {
	runtime.LockOSThread()
	name, err := windows.UTF16PtrFromString(orchestratorMutexName)
	if err != nil {
		runtime.UnlockOSThread()
		return nil, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("create lifecycle mutex: %w", err)
	}
	wait, err := windows.WaitForSingleObject(handle, 0)
	if err != nil || (wait != windows.WAIT_OBJECT_0 && wait != windows.WAIT_ABANDONED) {
		_ = windows.CloseHandle(handle)
		runtime.UnlockOSThread()
		if err != nil {
			return nil, fmt.Errorf("wait lifecycle mutex: %w", err)
		}
		return nil, fmt.Errorf("another orchestrator instance is active")
	}
	return func() error {
		releaseErr := windows.ReleaseMutex(handle)
		closeErr := windows.CloseHandle(handle)
		runtime.UnlockOSThread()
		if releaseErr != nil {
			return fmt.Errorf("release lifecycle mutex: %w", releaseErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close lifecycle mutex: %w", closeErr)
		}
		return nil
	}, nil
}
