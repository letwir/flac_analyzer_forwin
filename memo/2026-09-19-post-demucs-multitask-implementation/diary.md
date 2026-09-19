### 2026-09-19 — post-Demucs multitask implementation

- Hypothesis: Releasing Demucs-only resources immediately after frozen stem handoff allows another track's Demucs inference to overlap with the current track's CPU post-analysis without unsafe GPU overlap.
- Tried: Expressed requirements as SIGMA/1, linted them, implemented bounded handoff and shared GPU arbitration through the required agy CODE route, then inspected the exact diff.
- Rejected: Increasing Demucs concurrency above one was rejected because ONNX_LOCK, VRAM, and GPU saturation remain safety constraints.
- Uncertainty: Real production overlap and throughput are not verified; race tests are blocked by missing gcc.
- Attribution: The initial implementation exposed a scheduler-release failure path and an insufficient queue test; these were detected in main-agent post-verification and corrected. PromptDefect=0%, AgentDefect=15% for the initial implementation oversight, corrected before completion.
- Search: Repository source, current issue queue, SIGMA/1 requirements, harness-lint, Go tests, and go vet.
- Correction: Added idempotent scheduler/arbiter cleanup and retryable status handling for Demucs admission timeout.
- Emotion: Cautiously positive; the design boundary is clearer, but live performance remains unproven.
- Thoughts: The next useful boundary test is a real two-track fixture showing Track B Demucs starts while Track A CPU post-analysis is active.
