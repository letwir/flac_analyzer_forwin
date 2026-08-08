# Walkthrough: Physical RAM Lock & Graceful Fallback Implementation

- **Summary**: Implemented Win32 `VirtualLock` and `VirtualUnlock` inside Go orchestrator shared memory management (`shm_windows.go`). Added working set expansion (`SetProcessWorkingSetSizeEx`) and ensured a graceful fallback to standard shared memory when memory locking quotas are exceeded.

- Modified Files:
  - `orchestrator/dispatcher/shm_windows.go`: Added `procVirtualLock`, `procVirtualUnlock`, `procSetProcessWorkingSetSizeEx` bindings.
  - `orchestrator/dispatcher/shm_windows_test.go`: Added `isLocked` status validation.
  - `orchestrator/main.go`: Enabled working set expansion upon startup.
  - `config.toml.example`: Added `enable_virtual_lock` setting documentation.
