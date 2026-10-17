# Diary: 20261017-single-task-gpu-collector

- timestamp: 2026-10-17 Asia/Tokyo
- task: Repair SingleTask + Unreg stall after preflight.
- request-evidence: User supplied command/output and explicitly requested a fix after confirming no further progress.
- action: Inspected entrypoint, GPU collector startup and resource admission; bounded worker implementation and independent review.
- result: Source cause identified; final validation is recorded in walkthrough.md.
- friction: Sandbox DNS prevented first guideline CLI fetch; escalation succeeded. Ephemeral sandbox temp paths differed across calls, so rule receipt was regenerated in the workspace and verified. No cause attributed to the user.
- attribution: PromptDefect 0%, AgentDefect 100% of observed process friction (ToolFailure); diagnostic confidence high for the source defect, production acceptance unverified.
- impact: Single-file execution can collect the GPU observation required to enter analysis; subsequent resource waits become visible.
- feedback: Request supplied enough evidence and repair authority; no clarification required.
- rewritten-request: Repair the single-file unregistered batch stall after preflight, preserve unrelated changes, and verify with isolated tests and a rebuilt executable.
