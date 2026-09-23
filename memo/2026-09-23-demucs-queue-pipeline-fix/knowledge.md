# Knowledge: Demucs queue pipeline fix — 2026-09-23

- The Demucs daemon pool caps capacity at one. Prewarm, on-demand acquisition, dead-client restart, and recycling now share the `spawning` reservation while the factory runs outside the pool mutex.
- Durable waiting count is defined as SQLite rows in `PENDING`, `QUEUED`, or `FAILED_MAYBE_RETRY`; `RUNNING` and terminal rows are excluded. The count request is ordered through the state DB writer loop.
- Queue, Demucs, post-feature, and PostgreSQL success logs share a title-or-basename plus track label. Queue intake ordinals are process-local and newly persisted tasks alone are logged.

Source scope: `orchestrator/dispatcher` and `orchestrator/state` in `flac_analyzer_forwin`.
Verified date: 2026-09-23.
Supersedes: none.
