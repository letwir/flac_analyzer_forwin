# Knowledge: GPU arbitration and durable FIFO queue — 2026-09-23

- Demucs and Tensor feature work share a capacity-one GPU arbiter. A buffered channel serialized access but did not promise FIFO handoff. Direct grant to the oldest waiter prevents barging; canceled waiters must either be removed or pass a raced grant onward.
- GPU arbiter deadline failures need `ErrRetryableTimeout` wrapping so `pipeline_step` parks them as `FAILED_MAYBE_RETRY`; retain the underlying context error for `errors.Is`.
- Durable SQLite queue selection now uses insertion order and an explicit `PendingTaskOrder` API boundary. `ClaimPendingTasks` defaults to FIFO; the feeder passes FIFO explicitly. Future config-backed policies can select another order there. `file_size` is not implemented yet.
- Verified locally with `go.exe test ./...` from `orchestrator`; no live GPU acceptance was performed.

Source scope: `orchestrator/dispatcher` and `orchestrator/state` in `flac_analyzer_forwin`.
Verified date: 2026-09-23.
Supersedes: none.
