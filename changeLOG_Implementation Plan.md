# Implementation Plan: Win32 Job Object Process Grouping & Auto-Kill

- **Goal**: Group Python worker processes under `orchestrator.exe` in Windows Task Manager (Chrome-style tree view) and enable automatic process cleanup on parent exit without applying any resource throttling flags.
- **Target**: `orchestrator/dispatcher/job_windows.go`, `orchestrator/main.go`, `orchestrator/dispatcher/dispatcher.go`.
- **Feature**:
  - `job_windows.go`: Added `InitGlobalJob()` and `AssignProcessToJob()` using Win32 `CreateJobObjectW` and `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`.
  - `main.go`: Called `dispatcher.InitGlobalJob()` at startup.
  - `dispatcher.go`: Called `AssignProcessToJob` immediately after `cmd.Start()` in `runPythonScript`.
- **Status**: Completed
