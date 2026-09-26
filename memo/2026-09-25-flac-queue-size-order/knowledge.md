# Knowledge: FLAC queue size ordering

## Behavior implemented
- Workers announce readiness before receiving a task; the unbuffered handoff keeps unstarted work PENDING in SQLite.
- The feeder claims one task per ready worker, sorting positive `fileSize` bytes ascending with rowid tie-breaking. Unknown, invalid, or nonpositive sizes sort last.
- `/task`, `EnqueueDurable`, and `EnqueueDurableBatch` derive file size with `os.Stat` before durable insertion.
- Admission deferrals remain retryable. If shutdown occurs before handoff, admission reservations are released and task status returns to PENDING.

## Verification
- `go test ./... -timeout 4m`, `go vet ./...`, `git diff --check`, and rule preflight passed.
- Race tests could not run: CGO requires `gcc`, unavailable in this environment.

## Limits
- Running tasks are not preempted. Legacy payloads with no usable size remain after known-size tasks.
