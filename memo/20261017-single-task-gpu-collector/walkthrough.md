# TaskReport: 20261017-single-task-gpu-collector

## Outcome

Root cause: single-file mode never started GPU collection, while admission required a fresh observation. The single-file entrypoint now starts the existing cancellable collector after preflight for a nonempty task list. Track starts and resource wait reasons are logged. Single-task admission uses the same existing reservation seam as the durable feeder; normal production rechecks remain enabled.

## Scope

Preserve unrelated init.bat and requirements-blackwell.txt changes. No stage, commit, push, production DB changes or live batch run. Root single-orchestrator.exe will be rebuilt after checks; the old executable is backed up as single-orchestrator.before.exe in this task directory (matching SHA256 verified).

## Validation

- Focused single-task tests: exit 0.
- `go test ./... -count=1 -timeout 120s` from orchestrator: all packages passed, exit 0.
- `go vet ./...`: exit 0.
- `git diff --check` on all three changed source files: exit 0.
- `go build -o ..\single-orchestrator.exe .`: exit 0.
- Rebuilt `single-orchestrator.exe -h`: exit 0, expected single-file options present.
- New regression tests cover cancellation while waiting (retryable SQLite state) and wrapped permanent budget rejection (failed state, exactly one reservation attempt), using isolated temporary databases.
- Rebuilt executable SHA256: AF80E1B658D7C2B9C6B62B366CDC232D022791540B9CAF635C87FEAC4ECBC2DE.
- Independent review and read-only hardware smoke results are recorded separately when returned.
- Not run: production audio analysis, live DB writes, end-to-end batch acceptance, race detector.

## Residual artifacts

Go created single-orchestrator.exe~ while replacing the old binary. Deletion returned Access denied both inside and outside sandbox; this file was preserved rather than changing permissions. The separately verified task-local backup remains available. No unrelated source changes were made.
