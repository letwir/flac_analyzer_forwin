# Walkthrough - Automatic CUE Parsing & Track-Level Task Dispatching

Implemented automatic CUE/tag inspection in the Go orchestrator upon receiving `/task` requests, expanding FLAC files into track-level tasks with full metadata (`album`, `title`, `artist`, `track_number`, `predictions`).

## Changes Made

### Go Orchestrator

- **[orchestrator/state/db.go](file:///a:/Users/letwir/repo/flac_analyzer_forwin/orchestrator/state/db.go)**: Updated `task_state` schema to composite primary key `(file_path, track_number)`.
- **[orchestrator/dispatcher/dispatcher.go](file:///a:/Users/letwir/repo/flac_analyzer_forwin/orchestrator/dispatcher/dispatcher.go)**: Integrated `InspectCue` method and updated status tracking to include `TrackNumber`.
- **[orchestrator/main.go](file:///a:/Users/letwir/repo/flac_analyzer_forwin/orchestrator/main.go)**: Modified `/task` handler to inspect CUE/FLAC metadata automatically and enqueue track-level payloads.
- **[orchestrator/orchestrator.exe](file:///a:/Users/letwir/repo/flac_analyzer_forwin/orchestrator/orchestrator.exe)**: Rebuilt Go binary.

### Python Workers

- **[worker_cue.py](file:///a:/Users/letwir/repo/flac_analyzer_forwin/worker_cue.py)**: Added lightweight CUE/FLAC inspector worker.
- **[worker_essentia.py](file:///a:/Users/letwir/repo/flac_analyzer_forwin/worker_essentia.py)**: Absolute path resolution for `models_dir` to ensure Essentia model loading.

## Validation Results

- Successfully compiled `orchestrator.exe`.
- Tested `worker_cue.py` for CUE boundary extraction.
