# Diary: GPU arbitration and durable FIFO queue — 2026-09-23

- Timestamp: 2026-09-23T16:00:00+09:00
- Task: Fix GPU arbiter deadline failures and switch task claiming to FIFO while leaving a config-backed order extension point.
- Request evidence: User supplied GPU arbiter `context deadline exceeded` failures, then requested FIFO first and future config.toml file-size ordering extensibility.
- Action: Replaced channel arbitration with a cancellable FIFO arbiter, classified arbiter deadlines as retryable, changed SQLite pending-task selection to FIFO, and exposed a policy parameter at the claim boundary.
- Result: Focused state/dispatcher tests and the full Go module suite passed; diff check passed.
- Friction: AGY took several minutes but completed successfully. Review found timing-based test setup and a grant/cancellation race; both were addressed before final verification.
- Attribution: PromptDefect 0%: acceptance direction was clear. AgentDefect 0% after correction: generated test synchronization was improved during review; no known defect remains in the verified local changes.
- Impact: GPU wait timeouts enter the existing retry path and durable tasks are claimed FIFO. File-size sorting and its config key remain future work.
- Feedback: No prompt defect identified.
- Rewritten request: N/A.
