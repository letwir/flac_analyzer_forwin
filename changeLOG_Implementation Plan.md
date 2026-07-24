# Implementation Plan - Thread-Safe SQLite Operations & Busy Timeout

Resolve `SQLITE_BUSY (database is locked)` errors during high-concurrency CUE track task registration.

## Proposed Changes

### Go Orchestrator

#### [MODIFY] [orchestrator/state/db.go](file:///a:/Users/letwir/repo/flac_analyzer_forwin/orchestrator/state/db.go)
- Configured SQLite DSN with `_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)`.
- Added `sync.Mutex` (`mu`) to `DB` struct to serialize write operations (`CheckOrInsertWithForce`, `UpdateStatus`, `ResetStaleTasks`).

## Verification Plan

- Rebuilt `orchestrator.exe` with thread-safe SQLite operations.
