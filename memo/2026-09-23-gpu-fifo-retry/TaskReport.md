# Task report: GPU arbitration and durable FIFO queue — 2026-09-23

## Verified

- The previous capacity-one GPU channel did not guarantee FIFO handoff, allowing later Demucs acquisitions to repeatedly precede GPU feature waiters.
- GPU arbiter acquisition deadlines are now wrapped with `ErrRetryableTimeout` in Demucs and GPU feature paths, preserving the context deadline for `errors.Is` checks. Caller cancellation remains cancellation.
- The shared GPU arbiter now grants the oldest live waiter directly and removes canceled waiters.
- Durable pending tasks are claimed by SQLite row insertion order (FIFO), replacing estimated-duration and aging-based priority. The feeder selects FIFO through an explicit ordering-policy boundary.
- A later config-backed order can be connected at `ClaimPendingTasksInOrder`; `file_size` is intentionally unsupported until implemented.
- `go.exe test ./state ./dispatcher` passed; `go.exe test ./...` passed from `orchestrator`; `git diff --check` passed.

## Residual limits

- The 120-second GPU wait may still expire if a GPU owner remains stuck; it will now be retryable rather than terminal.
- Live GPU-host behavior and the user's workload acceptance remain unverified. No service was restarted or deployed.
- Race detector was not run in this check.

## Classification

- PromptDefect: not established.
- AgentDefect: initial generated tests used timing sleeps; replaced with waiter-state synchronization before verification.
