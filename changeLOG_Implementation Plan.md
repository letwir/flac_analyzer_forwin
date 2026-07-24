# Implementation Plan - Automatic SQLite Schema Migration for Track Number

Fix SQLite `no such column: track_number` error by introducing automatic table migration in Go orchestrator.

## Proposed Changes

### Go Orchestrator

#### [MODIFY] [orchestrator/state/db.go](file:///a:/Users/letwir/repo/flac_analyzer_forwin/orchestrator/state/db.go)
- Added `migrateTables()` to inspect `task_state` schema using `PRAGMA table_info`.
- Automatically migrates existing single-key `task_state` tables into composite primary key `(file_path, track_number)` tables (`task_state_new`) without losing data.

## Verification Plan

- Rebuilt `orchestrator.exe` with migration logic.
