# Implementation Plan - Non-Blocking Asynchronous Single Writer Channel for SQLite

Implement a Single Writer Actor pattern using Go channels (`opQueue chan dbWriteOp`) for `orchestrator.db` to eliminate write locks and zero out SQLite wait times.

## Proposed Changes

### Go Orchestrator

#### [MODIFY] [orchestrator/state/db.go](file:///a:/Users/letwir/repo/flac_analyzer_forwin/orchestrator/state/db.go)
- Created `opQueue chan dbWriteOp` with buffer size 10,000 for non-blocking queueing.
- Implemented `writerLoop()` running in a dedicated background goroutine.
- Converted `UpdateStatus` into a fire-and-forget non-blocking call (0ms wait time).
- Converted `CheckOrInsertWithForce` to use one-shot result channels for single-threaded serialization.

## Verification Plan

- Rebuilt `orchestrator.exe` with non-blocking async SQLite channel pattern.
