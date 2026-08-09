# Implementation Plan: Memory Protection & Dynamic Retry Throttling Architecture

- **Goal**: Resolve `ArrayMemoryError` and `CreateFileMappingW` (WinError 1455: `The paging file is too small for this operation to complete`) without changing tensor shapes or mathematical formulas, via dynamic queue throttling in Go Dispatcher and backoff retries in Python Workers.
- **Target**: `config.toml`, `config.toml.example`, `orchestrator/main.go`, `orchestrator/dispatcher/dispatcher.go`, `analyzer/core.py`, `worker_librosa.py`.
- **Feature**:
  - `config.toml`: Added `shm_retry_count`, `shm_retry_delay_sec`, `memory_retry_count`, `memory_retry_delay_sec` for configurable dynamic throttling (aligned with 20-second task cycle).
  - `orchestrator/dispatcher`: Implemented dynamic SHM allocation retry loop (`NewSharedMemory` attempts) with queue throttling and configurable backoff sleep.
  - `analyzer/core.py`: Set `self._stft = None` immediately after `spectro` property calculation to allow early GC of 211MB+ complex64 arrays.
  - `worker_librosa.py`: Added `try-except (MemoryError)` backoff retry loop with `gc.collect()` and configurable sleep interval before retrying feature extraction.
- **Status**: Completed
