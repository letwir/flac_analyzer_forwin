# FLAC analyzer metrics and Demucs recovery pre-deploy verification

- task_id: `2026-09-19-flac-metrics-demucs-predeploy`
- The live host was `letwir-main.tigris-tailor.ts.net`.
- Live evidence showed valid 16 GiB configured dedicated VRAM capacity, about 8.82 GiB used, about 7.18 GiB available, and GPU utilization reported as zero.
- A goroutine profile showed twelve workers waiting in `DemucsDaemonPool.Acquire` from hash checking and one worker waiting from Demucs separation.
- Hash checking now uses a standalone CPU worker and preserves the existing `flac_decode` hash algorithm and slice boundaries.
- GPU telemetry now distinguishes unknown/stale samples from valid zero values and exports explicit validity and sample-age metrics.
- Demucs pool metrics track registered clients, and the pool can recover capacity after daemon restart/recycle failure.
- The committed implementation is `0432ff2`; four formatting/build-fix files remain uncommitted pending VCS approval.
- `update.ps1 -CheckOnly` passed and built SHA-256 `FB58276BDFC0744E60C16A0D7966AB16EF9C29E4767A1FCBBC38897171A06B6D` without replacing installed binaries.
- `tests/test_worker_hash.py` passed 8 tests; `go vet ./...` passed; focused dispatcher tests passed.
- `go test -race` was unavailable because this Windows Go environment has CGO disabled.
- Deployment and runtime verification remain pending explicit RELEASE approval.
