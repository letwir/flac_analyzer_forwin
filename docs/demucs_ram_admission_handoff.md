# Demucs RAM admission: remaining work and handoff

Verified against `906990c` on 2026-09-26. The one-track downstream ticket is already implemented. The feeder obtains it before worker handoff, parks a blocked task in SQLite, and rechecks OS memory and GPU telemetry before dispatch. Pipeline finalization releases it after wavefront, GPU, SHM, and cache cleanup. The focused admission tests and `go test -count=1 ./...` passed locally. This is code and local-test evidence, not a live OOM guarantee.

## Remaining acceptance: real Windows workload

Status: **not run in this handoff**. Use the intended RTX 5070 Ti host and a disposable test queue and database, with the operator choosing the maintenance window and rollback point. Record task IDs and timestamps alongside `AvailPhys`, memory load, dedicated and shared GPU memory, disk free space, queue status, and the admission/lease and cleanup logs. Do not infer ticket ownership solely from `analyzer_queue_length`: that gauge counts the in-memory queue, while blocked tasks can remain in SQLite as `FAILED_MAYBE_RETRY`.

1. Run two ordinary Demucs tracks. Keep the first track in its post-Demucs wavefront long enough to observe the second. Acceptance: the second track stays durably deferred without occupying a worker, and its Demucs stage begins only after the first track's cleanup and ticket release. Confirm that the first track still runs CPU and GPU stem branches as planned.
2. Exercise a short track, a long single track, a CUE batch, and `MixOnly`. Acceptance: the long track uses one CPU consumer, Disk fallback is chosen when its SHM estimate cannot fit, and each task reaches the expected terminal or retry state without duplicate claim.
3. Induce cancellation, timeout, feature failure, and cleanup failure in a controlled test setup. Acceptance: no ticket, SHM handle, GPU execution lease, or retryable SQLite row is stranded; a later task resumes after release. A cleanup error must remain visible in the terminal task reason.
4. Apply transient RAM pressure and then release it. Acceptance: `FAILED_MAYBE_RETRY` work resumes after its retry delay without a worker or Demucs/GPU slot held during the wait. Record whether the pressure hysteresis and disk path behave as expected.
5. Compare peak host RAM, shared GPU memory, dedicated VRAM, disk use, throughput, and failures against a known baseline. Report the observed workload and limits. A successful run reduces evidence of overlap-driven OOM; it cannot establish that all external pressure or driver behavior is safe.

The repository also has no confirmed race-detector result for this change. Run `go test -race` when a supported Windows CGO toolchain is available; keep ordinary Go test success separate from race and live-host evidence.

## Deferred design: DB-tagged Single lane

The existing `-single-file` flag is an exclusive CLI mode (`orchestrator/main.go` and `dispatcher/single_task.go`). It is not a lane in the durable feeder, so a queued task must never launch that CLI automatically. No DB-to-Single transfer was implemented for this task.

If automatic transfer is later required, first specify a durable lane tag and an atomic state transition that prevents the normal feeder and the Single lane from claiming the same `(filepath, track_number)`. Define the lease owner, retry/timeout/cancel transitions, restart recovery for a claimed task, and how PostgreSQL preflight and final ingest remain idempotent. Then add tests for concurrent claims, process interruption before and after lane transfer, replay, and CUE tracks. Only enable routing after those tests and a live restart trial pass. Until then, the supported alternative is to keep oversized work in the existing queue with long-track CPU parallelism of one and Disk fallback, or run the exclusive CLI manually as a separately chosen operation.

## Local verification already available

- `orchestrator/dispatcher/admission_test.go` covers ticket exhaustion and durable resume, predecessor wavefront blocking, cleanup error ordering, telemetry failure, permanent versus temporary shortage, and MixOnly/long-track estimates.
- `orchestrator/dispatcher/pipeline_demucs_wavefront_test.go` covers bounded stem handoff.
- On 2026-09-26: `go.exe test -count=1 ./...`, `go.exe vet ./...`, strict lint of `docs/demucs_ram_admission_requirements.lrf`, and `git.exe diff --check` passed.

Keep source changes and live-host actions as separate follow-up work. The admission implementation is already in `906990c`; the items above are validation and an optional future lane design.
