# Walkthrough: Demucs queue pipeline fix — 2026-09-23

## Changes

- Unified daemon creation under a shared `spawning` reservation and context-aware pool wait. A reserved spawn is counted against pool capacity, and completion/failure/close wakes waiters.
- Added `CountWaitingTasks`, ordered through the SQLite writer loop, covering pending, queued, and parked retryable rows.
- Added process-local intake ordinal, durable waiting count, consistent task labels, serialized Demucs start, first post-feature handoff, DB ingest queue handoff, and confirmed PostgreSQL success logs.
- Added deterministic Prewarm-versus-Acquire spawn race coverage and waiting-status count coverage.

## Verification

- `go.exe test ./state ./dispatcher` from `orchestrator`: passed.
- `go.exe test ./...` from `orchestrator`: passed for all module packages.
- `go.exe test ./dispatcher -run TestPoolPrewarmReservesSpawnAgainstConcurrentAcquire -count=20`: passed.
- `git diff --check`: passed.
- `go.exe test -race ...`: unavailable because cgo is disabled and gcc is not installed.

## Scope and residual risk

- No deployment, service restart, commit, or push was performed.
- The tests verify local pool and status-count behavior; no live GPU host run was performed.
- Race-detector validation remains unverified due the host toolchain configuration.
