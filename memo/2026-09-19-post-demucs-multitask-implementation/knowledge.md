# Knowledge Base: post-Demucs multitask implementation

## Context

The approved SIGMA/1 contract is `docs/post_demucs_multitask_requirements.lrf`. Demucs inference remains serialized, while post-Demucs work is allowed to overlap the next track's Demucs inference under bounded resource controls.

## Findings

- `executeDemucsStage` previously held the Demucs scheduler and execution lease through the post-inference wavefront Join.
- The implementation now releases Demucs scheduler, Demucs daemon, shared GPU arbiter, and inference execution lease after `SeparateWithEvents` returns and before wavefront Join.
- CPU and GPU stem handoff queues are bounded by the requested stem count.
- GPU Tensor work uses the shared one-token arbiter, preventing unsafe overlap with Demucs inference.
- Demucs slot and execution-admission wait exhaustion is represented by `ErrRetryableTimeout` and persisted as `FAILED_MAYBE_RETRY`.

## Morphism

`DemucsExclusive(task) -> FrozenStemHandoff(task) -> ReleaseInferenceResources(task) -> BoundedPostAnalysis(task) -> Join(task)`

## Verification

`go test ./...`, `go vet ./dispatcher`, SIGMA/1 harness-lint Level 2 strict, and `git diff --check` passed. Race testing remains unavailable because the environment has no gcc compiler.

## Uncertainty

Live GPU throughput, real cross-track overlap, and production deployment behavior were not exercised in this workspace.
