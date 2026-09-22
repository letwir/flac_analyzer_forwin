# Knowledge Base

## Context

- task_id: `2026-09-19-sigma1-postgres-ingested-check`
- Scope: enforce Σ1 for normalized `(filepath, track_number)` task identity.
- PostgreSQL `raw.library_flac` is the authoritative reference; SQLite `task_state` is an ingested/check cache.

## Findings

- Existing analysis preflight already classifies missing, incomplete, and complete PostgreSQL snapshots through `DecideAnalysis`.
- SQLite `COMPLETED` could previously suppress a missing or incomplete PostgreSQL reference.
- PostgreSQL success is now the completion boundary. SQLite DLQ persistence is retryable (`FAILED_MAYBE_RETRY`).
- Active SQLite states remain duplicate-suppressed; stale or missing SQLite state can reconcile against a complete PostgreSQL snapshot.

## Morphism

`(filepath, track_number) -> PostgreSQL snapshot -> {missing, incomplete, complete} -> SQLite queue/reconcile decision`.

