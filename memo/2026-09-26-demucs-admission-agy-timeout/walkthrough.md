# Walkthrough: Demucs admission agy timeout

- Task ID: 2026-09-26-demucs-admission-agy-timeout
- Outcome: Worker timeout; no source code changed.
- Initial state: target tracked/index Go diffs were empty; prior untracked memos were preserved.
- Worker: one bounded agy invocation failed with `agy process timeout`. No alternate backend was used.
- Verification: after timeout, scoped worktree and index diffs remained empty. No Go tests were run.
- Remaining: implement the feeder-side admission ticket, retain it through wavefront/GPU/SHM cleanup, and add requested regression/failure tests.
- Risk: resource admission still cannot claim OOM prevention; OS memory/GPU telemetry and model working-set variance remain residual hazards.
