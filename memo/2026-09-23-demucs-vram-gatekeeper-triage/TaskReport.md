# Task Report: Demucs VRAM Gatekeeper Triage (2026-09-23)

## Result

The supplied host log shows repeated TaskFeeder admission deferrals because available VRAM stayed around 0.45-0.51 GB while the configured admission required 1.50 GB (1.00 GB estimated task VRAM plus 0.50 GB reserve). The blocked point is the Gatekeeper before dispatch, so the excerpt does not show Demucs concurrency as the immediate cause.

Repository inspection confirms the Demucs daemon pool, adaptive scheduler, GPU arbiter, and admission GPU/Demucs lane budgets are each bounded to one. This makes overlapping Demucs inference an unlikely explanation for the excerpt. External GPU use or stale/unreliable GPU telemetry remain possible; a telemetry collection timeout is also logged. PostgreSQL UPSERT timeouts caused five payloads to fall back to the local DLQ; all five later retried successfully, which is a separate transient symptom.

## Evidence and limits

- Attachment: `2026-09-23 07:45` through `09:28` logs, including repeated VRAM NOGO and two GPU CIM collection timeouts.
- Source: `orchestrator/dispatcher/dispatcher.go`, `orchestrator/dispatcher/admission.go`, `orchestrator/config/loader.go`, `orchestrator/dispatcher/gatekeeper.go`.
- Read-only SSH snapshot to `letwir-main.tigris-tailor.ts.net` did not complete; live process and GPU state are unverified.
- No application files or live services were changed.

## Follow-up

If live inspection becomes available, compare GPU process/VRAM ownership, the latest orchestrator logs, and GPU metric freshness. Avoid increasing Demucs concurrency as a response to a pre-dispatch VRAM admission block.

## Live snapshot follow-up (2026-09-23)

- Read-only `:2112/debug/pprof/goroutine?debug=2` showed the worker goroutines blocked receiving from the task queue; `taskFeeder` was in its normal select loop. No active pipeline stack appeared in the captured portion.
- The live `/metrics` snapshot showed `analyzer_queue_length=0`, `analyzer_active_workers=0`, `analyzer_demucs_slots_in_use=0`, `analyzer_demucs_dynamic_limit=1`, and `analyzer_gpu_sample_age_seconds=3.40`.
- `analyzer_last_demucs_wait_seconds=244.00` records a prior completed Demucs slot wait of about four minutes. This is historical, not a current wait. The current snapshot is idle, so it cannot establish what held the slot during that wait.
- No current queue or active work was observed. Pprof does not identify an external process holding VRAM; the host GPU process list was not collected in this follow-up.
