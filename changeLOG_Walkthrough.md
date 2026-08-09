# Walkthrough: Memory Protection & Dynamic Retry Throttling Implementation

## Summary of Changes

1. **`config.toml` & `config.toml.example`**:
   - Added `shm_retry_count = 5` and `shm_retry_delay_sec = 8` in `[orchestrator]`.
   - Added `memory_retry_count = "3"` and `memory_retry_delay_sec = "6"` in `[python_env]`.

2. **`orchestrator/main.go` & `orchestrator/dispatcher/dispatcher.go`**:
   - Configured Go Dispatcher to parse `ShmRetryCount` and `ShmRetryDelaySec`.
   - Replaced single-pass `NewSharedMemory` allocation with a retry loop that throttles task queues and sleeps for `ShmRetryDelaySec` seconds (default 8s) when Windows Commit Limit is hit (`CreateFileMappingW` failed).

3. **`analyzer/core.py`**:
   - Added `self._stft = None` immediately after `spectro` calculation in `AudioContext.spectro` property to release 211MB+ complex64 arrays for garbage collection.

4. **`worker_librosa.py`**:
   - Loaded `memory_retry_count` and `memory_retry_delay_sec` from `config.toml`.
   - Wrapped stem feature extraction in a backoff retry loop that catches `MemoryError` / `ArrayMemoryError`, triggers `gc.collect()`, sleeps for the configured delay, and retries extraction without modifying tensor shapes.

## Verification Results

- **Go Orchestrator Build**: Successfully compiled `orchestrator.exe`.
- **Python Integration Tests**: Verified `test_integration.py` compatibility.
