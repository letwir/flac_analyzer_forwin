# Implementation Plan - Automatic CUE Parsing & Track-Level Dispatching

Implement automatic CUE/tag parsing in Go orchestrator and dispatch track-level tasks to prevent metadata loss (`album`, `title`, `artist`, `predictions`).

## Proposed Changes

### Go Orchestrator

#### [MODIFY] [orchestrator/state/db.go](file:///a:/Users/letwir/repo/flac_analyzer_forwin/orchestrator/state/db.go)
- Updated `task_state` schema to composite primary key `(file_path, track_number)`.
- Updated `CheckOrInsertWithForce` and `UpdateStatus` to support `trackNumber`.

#### [MODIFY] [orchestrator/dispatcher/dispatcher.go](file:///a:/Users/letwir/repo/flac_analyzer_forwin/orchestrator/dispatcher/dispatcher.go)
- Updated worker status updates to pass `task.TrackNumber`.
- Added `InspectCue` method to invoke `worker_cue.py`.

#### [MODIFY] [orchestrator/main.go](file:///a:/Users/letwir/repo/flac_analyzer_forwin/orchestrator/main.go)
- Updated `/task` endpoint to inspect CUE/FLAC tags via `worker_cue.py` and auto-expand into track-level tasks.

### Python Workers

#### [NEW] [worker_cue.py](file:///a:/Users/letwir/repo/flac_analyzer_forwin/worker_cue.py)
- Created CUE/FLAC tag inspector worker that outputs track slices JSON.

#### [MODIFY] [worker_essentia.py](file:///a:/Users/letwir/repo/flac_analyzer_forwin/worker_essentia.py)
- Updated `models_dir` to absolute path to prevent model load failures.

## Verification Plan

- Rebuilt `orchestrator.exe` with Go 1.x without compilation errors.
- Verified `worker_cue.py` execution against FLAC files.
