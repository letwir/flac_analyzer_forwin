# Knowledge Base: Demucs RAM admission implementation - 2026-09-26-demucs-ram-admission-implementation

## Context

The dispatcher had a serialized Demucs execution slot, but its execution lease ended before the stem wavefront and follow-on processing completed. That allowed a later track's Demucs stage to overlap the preceding track's RAM-heavy feature work.

## Findings

- Added a distinct one-track downstream ticket held from feeder admission through task finalization. Ticket exhaustion is parked in the durable retry queue before worker handoff.
- Feeder admission estimates SHM/disk, analysis stem count, CPU consumers (including the existing long-track single consumer), working GPU demand, and a shared-GPU host-RAM reserve. It re-observes OS RAM and required GPU telemetry immediately before handoff. Current OS available RAM already reflects live shared-GPU use and is not reduced by it a second time.
- Estimate arithmetic fails closed on overflow. Requests above the configured total-RAM budget become terminal failures; transient observed pressure remains retryable. Disk fallback is selected before that terminal classification.
- The task ticket stays held through wavefront join, GPU cleanup, SHM close, and cache cleanup, including MixOnly and failure/cancel paths. Release is idempotent.
- `-single-file` remains an exclusive CLI path; no DB-lane transfer, schema change, or new replay behavior was introduced.

## Morphism

Admission now spans the complete RAM-owning track lifecycle instead of only the Demucs inference interval. This prevents the dispatcher from starting another track's Demucs while the prior track still owns its downstream memory lease.

## Verification and Limits

- `go test ./...` passed in `orchestrator/` after the final edits.
- `git diff --check` and strict requirement LRF lint passed.
- No live GPU/SHM workload was run. This reduces overlap-driven OOM risk but cannot guarantee OOM prevention against external processes, inaccurate sizing, or OS/GPU-driver behavior.
