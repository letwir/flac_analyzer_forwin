# Knowledge Base: Workload-Aware Scheduler — 2026-09-24

## Context Findings

- The durable SQLite task table already held complete task payloads, but the feeder claimed pending work FIFO and CUE ingestion enqueued each track separately.
- The change keeps SQLite as the durable shelf, atomically inserts CUE task batches, and adds deterministic size-based ordering with aging and FIFO tie breaking.
- Workload policy separates multi-track CUE, long single tracks, and short single tracks. Demucs remains serialized; short singles can fan out post-Demucs CPU stem consumers.
- CPU worker daemons now prewarm minimally, expand on acquisition, and trim idle workers when the queue and active task count are empty.
- PyTorch cache cleanup is requested after feature branches join and while holding the shared GPU arbiter.

## Verification and Limits

- `update.ps1 -CheckOnly` passed after the final code change: Go tests/build and the script's Python unittest and CLI checks succeeded.
- Focused Python cleanup test and strict LRF requirement lint passed; staged diff whitespace check passed.
- Commit `6d764332271abeb540cd13b032f27b8e8c6f83df` is on `origin/master`; a repeated porcelain push reported the branch up to date.
- Live host throughput, CUE batch behavior, VRAM recovery, and three-hour track behavior remain unverified; the user will continue debugging after this push.

## Morphism

The scheduler moves selection from an in-memory FIFO-only perspective to a durable SQLite-backed workload policy while retaining existing resource admission and GPU serialization boundaries.

Tags: flac-analyzer, sqlite, scheduler, demucs, gpu, queue
