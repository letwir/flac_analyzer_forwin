# Diary: Demucs queue pipeline fix — 2026-09-23

- Timestamp: 2026-09-23T13:42:44+09:00
- Task: Fix Demucs spawn reservation concurrency and make queue-to-ingest progress visible.
- Request evidence: The user asked to continue the Demucs-serial/post-Demucs-parallel queue fix, with dynamic `(ordinal / waiting count)` intake logs and named next-item / DB handoff phases.
- Action: Shared the daemon spawn reservation across Prewarm, Acquire, restart, and recycle; added durable waiting count through the DB writer queue; added task labels and phase logs; added deterministic pool race and count-status tests.
- Result: Full Go module test suite passed; deterministic spawn-race test passed 20 runs; `git diff --check` passed. Race detector could not run because `CGO_ENABLED=0` and gcc is unavailable.
- Friction: Initial focused test invocation used the repository root instead of the nested `orchestrator` module; first compile exposed an unused local, then passed after correction. Race detector was unavailable in this environment.
- Attribution: PromptDefect 0%: task intent and logging phases were clear, with an existing requirements LRF. AgentDefect 10%: the first compile exposed an unused local and the initial test command targeted the wrong module directory; both were corrected before completion.
- Impact: Prewarm and a concurrent Acquire cannot start two Demucs daemons for the single GPU slot; logs distinguish durable backlog, serialized Demucs start, post-feature start, queue handoff, and confirmed PostgreSQL success.
- Feedback: No prompt defect identified. Evidence: the task continuation plus the existing requirement LRF defined the behavior and acceptance checks.
- Rewritten request: N/A.
