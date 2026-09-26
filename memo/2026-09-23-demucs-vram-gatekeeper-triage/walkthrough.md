# Walkthrough - Demucs Serialization Does Not Remove VRAM Admission Deferrals (2026-09-23)

## 概要

Reviewed the current checkout against the 2026-09-23 Gatekeeper VRAM warnings and the earlier live pprof/metrics snapshot. No software files were changed.

## 成果物一覧

- `orchestrator/dispatcher/pipeline_demucs.go`: after `SeparateWithEvents` returns, release the GPU arbiter, Demucs client, scheduler slot, and execution lease before waiting for the post-inference wavefront.
- `demucs_daemon.py` and `models.py`: prewarm a resident daemon and initialize a global Demucs model before waiting for work; reuse that model for separation requests.
- `orchestrator/dispatcher/admission.go` and `gatekeeper.go`: before task dispatch, reject when available VRAM is below estimated Demucs VRAM plus the configured minimum reserve. The supplied 0.48 GB versus 1.50 GB log is therefore still an admission block before Demucs starts.
- Earlier live metrics reported Demucs daemon pool size 2 while the current checkout constructs and clamps the pool to capacity 1. This suggests the live binary or its metrics may not match this checkout; exact cause is unknown.

## 検証結果

The earlier change fixes holding the Demucs execution slot through post-Demucs feature analysis. It does not release the preloaded model's VRAM or relax the pre-dispatch Gatekeeper threshold, so it does not by itself resolve repeated VRAM NOGO/park/retry behavior. Whether resident Demucs memory is the source of the observed 0.48 GB free VRAM is unverified; other GPU processes or telemetry issues remain possible. No tests were run because this was a read-only diagnosis.
