# Task Report: FLAC queue size ordering

## Result

Implemented size-ascending dispatch for all not-yet-started durable tasks. The agy worker initially timed out; at the user's explicit direction, implementation continued with GPT-6-Luna small.

## Requested behavior

Keep active analysis uninterrupted. Whenever a task is enqueued, select from all not-yet-started tasks by ascending actual file size, so a newly added smaller file can precede older larger files.

## Changes

- Replaced buffered prefetch with a worker-ready unbuffered handoff; unstarted tasks remain pending in SQLite and are reconsidered after intake.
- Selects positive actual file-size bytes ascending with stable rowid ties; unknown sizes sort last.
- Measures actual file size at HTTP and dispatcher durable intake, surfacing stat errors.
- Preserves admission parking and returns a claimed task to PENDING with reservation release if shutdown precedes handoff.
- Added DB ordering and dispatcher queue/intake regression tests.

## Evidence and checks

- `go test ./dispatcher ./state -timeout 2m`: PASS.
- `go test ./... -timeout 4m`: PASS.
- `go vet ./...`: PASS.
- `git diff --check`: PASS.
- Rule lint and preflight: PASS.
- Race-test attempt unavailable because `gcc` is absent and Go race testing requires CGO.

## Residual risk

Race-detector validation remains unverified in this environment. Existing tasks with missing or invalid size metadata sort after known-size tasks; running tasks are not interrupted.
