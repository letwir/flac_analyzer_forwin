# Task Report: Demucs RAM admission implementation - 2026-09-26-demucs-ram-admission-implementation

## Result

Implemented a one-track downstream RAM ticket and feeder admission path. Admission occurs before worker handoff; ticket exhaustion is parked durably, and the ticket remains owned through wavefront completion, GPU cleanup, SHM close, and cache cleanup.

## Changes

- Added a `DownstreamTracks` admission dimension with default capacity one; retained the separate Demucs execution lease and stem-level feature parallelism.
- Added overflow-checked resource estimates for track capacity, analysis stems, CPU consumers, disk fallback, and GPU working reserve. Current OS `AvailPhys` is used at the final handoff check; live shared-GPU memory is not deducted twice.
- Classified per-task total-RAM budget excess as terminal and transient live pressure as durable retry. Unknown required telemetry fails closed.
- Extended cleanup/error propagation for SHM, feature GPU cleanup, and cache resources. Kept `-single-file` exclusive with no DB-lane transfer or DB schema change.
- Corrected the agy worker-generated report that prematurely claimed completion; preserved its history as an incomplete attempt.

## Checks

- `go test ./...` from `orchestrator/`: passed after implementation and planner adjustments.
- `git diff --check`: passed.
- `harness-lint.exe -path docs/demucs_ram_admission_requirements.lrf -level 2 -json -strict`: passed.
- No commit, push, external write, or live DB/service operation.

## Remaining Risk

No live Windows GPU/SHM stress test was run. Admission lowers overlap risk but does not guarantee OOM prevention under external memory pressure, sizing error, or OS/GPU-driver behavior.
