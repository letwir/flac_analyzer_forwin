# Knowledge: Demucs wavefront cancellation contract

- Verified 2026-09-23 in `flac_analyzer_forwin`: `stemWavefront.consume` stores lane-context-wrapped cancellation errors, so tests must use `errors.Is` for sentinel identity.
- Scope: `orchestrator/dispatcher/pipeline_demucs.go` and `pipeline_demucs_wavefront_test.go`.
- This supersedes the stale direct equality assertion in the bounded handoff regression test.
