# Walkthrough - Thread-Safe SQLite Operations & Busy Timeout

Resolved `SQLITE_BUSY (database is locked)` errors when multiple track tasks are enqueued simultaneously from CUE files.

## Changes Made

### Go Orchestrator

- **[orchestrator/state/db.go](file:///a:/Users/letwir/repo/flac_analyzer_forwin/orchestrator/state/db.go)**:
  - Added DSN parameters for `busy_timeout` (10 seconds), `journal_mode=WAL`, and `synchronous=NORMAL`.
  - Wrapped write transactions (`CheckOrInsertWithForce`, `UpdateStatus`, `ResetStaleTasks`) with `sync.Mutex` locks.
- **[orchestrator/orchestrator.exe](file:///a:/Users/letwir/repo/flac_analyzer_forwin/orchestrator/orchestrator.exe)**: Rebuilt Go binary.

## Validation Results

- Successfully recompiled `orchestrator.exe`.
