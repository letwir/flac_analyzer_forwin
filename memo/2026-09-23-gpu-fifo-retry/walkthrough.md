# Walkthrough: GPU arbitration and durable FIFO queue — 2026-09-23

## Changes

- Replaced the shared buffered channel with a cancellation-aware FIFO GPU arbiter.
- Demucs and GPU feature lanes release ownership through direct FIFO handoff. GPU arbiter deadlines now classify as retryable.
- Changed durable pending-task selection to FIFO and introduced `PendingTaskOrder` / `ClaimPendingTasksInOrder` as the future configuration boundary.
- Added tests for arbiter FIFO order, cancellation removal, retryable deadline classification, durable FIFO regardless of estimated duration/age, and unsupported future policies.

## Verification

- `go.exe test ./state ./dispatcher`: passed.
- `go.exe test ./...` from `orchestrator`: passed.
- `git diff --check`: passed.

## Scope and residual risk

- No service restart or deployment was performed.
- The real GPU host workload remains for user acceptance. Expired GPU acquisition waits will be retryable, but FIFO does not resolve a permanently stuck GPU owner.
- Race detector was not run.
