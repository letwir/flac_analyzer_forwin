# Σ1 PostgreSQL authoritative ingested-check

- task_id: `2026-09-19-sigma1-postgres-ingested-check`
- Result: implemented in the working tree; not committed or deployed.
- Acceptance: missing/incomplete PostgreSQL references can requeue even when SQLite says `COMPLETED`; complete references reconcile missing/stale SQLite state; active states suppress duplicates; DLQ remains retryable.
- Checks: focused dispatcher tests PASS; state tests PASS; full `go test ./...` PASS; `git diff --check` PASS.
- Residual risk: live PostgreSQL, production queue contention, and external deployment were not exercised.

