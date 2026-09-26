# Walkthrough: Demucs RAM admission implementation attempt

- Task ID: 2026-09-26-demucs-ram-admission-worker-failure
- Date: 2026-09-26
- Outcome: No Go implementation was applied. The single routed agy worker call returned `FAILED: agy structured response is not valid JSON`; this is not treated as successful work and no alternate backend was used.
- Initial state: `git status` showed only the ten pre-existing untracked memo directories; scoped Go target diff was empty.
- Inspection facts: feeder reserves task admission immediately before handing a task to the task queue and parks rejected tasks durably. The task lease is deferred until pipeline return, but it currently does not enforce one downstream track. Demucs execution scheduler/pool/GPU execution lease are released after separation, before wavefront completion and GPU cleanup. `-single-file` uses a separate exclusive path and no DB lane transfer was attempted.
- Post-attempt verification: scoped status remained unchanged, scoped `git diff` was empty, and `git diff --check` passed. No Go tests were run because no Go source changed.
- Prompt/agent attribution: the requested behavior and boundaries were detailed; the worker response-format failure is agent/tool-side. No implementation correctness claim is made.
- Remaining work: re-dispatch only after the orchestration/worker response gate is repaired or a new permitted route is explicitly selected; then implement ticket accounting and lifecycle tests, and validate DB/stem parallelism.
- Residual risk: RAM admission and lease-lifetime defects remain unresolved. OOM prevention cannot be claimed; OS telemetry, shared-GPU-memory effects, and working-set variance remain hazards.
- Scope preserved: no Go files, DB schema, CLI behavior, external systems, VCS state, or pre-existing memo artifacts were changed.
