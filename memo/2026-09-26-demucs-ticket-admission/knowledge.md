# Demucs downstream RAM ticket — task report

- Task ID: 2026-09-26-demucs-ticket-admission
- Result: FAILED; no implementation was applied.
- Evidence: Demucs scheduler and daemon pool are each single-capacity, but the Demucs scheduler/execution admission are released before the stem wavefront finishes. Task admission remains until the pipeline returns. Durable feeder parks rejected admissions without occupying a worker.
- Design: A single lifetime ticket should be acquired during feeder admission, account for added parallel feature RAM conservatively, and be released only after dependent resources are cleaned up. A separate CLI single-file mode has no queue execution-lane tag and assumes exclusive process execution; automatic DB-lane migration was not attempted.
- Worker: One agy implementation attempt returned an invalid structured response. Post-launch retry or backend switch was not performed.
- Verification: git status --short, git diff --check, git diff --stat, and exact target diff inspection found no code changes. No Go tests were run because no implementation was applied.
- Unknown: Whether agy generated a valid patch in its isolated staging; no patch was applied to the workspace.
