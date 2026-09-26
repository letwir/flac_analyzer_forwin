# Walkthrough: FLAC queue size ordering

## Implemented
- `orchestrator/dispatcher/dispatcher.go` and `durable_queue.go`: worker-ready unbuffered handoff; feeder selects one size-sorted pending row per ready worker.
- `orchestrator/state/db.go`: file-size ascending SQL order, stable rowid ties, missing/invalid/nonpositive last; retains the old order identifier as an accepted alias value.
- `orchestrator/http_server.go` and dispatcher intake methods: use actual on-disk size rather than caller metadata.
- `orchestrator/dispatcher/task_queue_test.go`, `admission_test.go`, and `orchestrator/state/db_test.go`: actual size, late smaller arrival, ties, unknown sizes, and worker-ready dispatch regressions.

## Verification
- `go test ./dispatcher ./state -timeout 2m`: passed.
- `go test ./... -timeout 4m`: passed for all packages.
- `go vet ./...`: passed.
- `git diff --check`: passed.
- LRF lint for BOOTSTRAP and LOAD, rule preflight: passed.
- `go test -race ./dispatcher ./state`: unavailable; CGO enabled attempt reported `gcc` missing.

## Residual risk
- Race-detector validation remains unavailable. Tasks without valid positive size metadata sort last; running tasks are not interrupted.
