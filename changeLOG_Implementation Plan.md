# Implementation Plan: Physical RAM Lock & Graceful Fallback Optimization

- **Goal**: Transition from pagefile-backed shared memory to direct physical RAM locking via Win32 `VirtualLock` API and `SetProcessWorkingSetSizeEx`, while guaranteeing a 100% transparent fallback to standard shared memory management.

- Target: `orchestrator/dispatcher/shm_windows.go`, `main.go`, `config.toml.example`
- Feature: Win32 `VirtualLock` / `VirtualUnlock` physical RAM pinning, working set extension, and graceful fallback log reporting.
- Status: Completed
