# Diary

### 2026-09-26 - Demucs RAM admission implementation - 2026-09-26-demucs-ram-admission-implementation

- **Hypothesis:** A dedicated track ticket acquired before dispatch and released only after cleanup prevents Demucs from overlapping the prior track's downstream wavefront.
- **Tried:** The agy code attempt timed out. Its partial diff was audited and not treated as success. The user authorized same-scope continuation with the current main agent. The implementation was completed, with extra audit fixes for MixOnly GPU cleanup and SHM unfreeze errors.
- **Rejected:** Automatic movement from the exclusive `-single-file` CLI into a DB lane; it would require safe duplicate execution, state transitions, and restart recovery that are not present.
- **Uncertainty:** No live Windows shared-GPU/SHM stress run was performed; capacity estimates and external process pressure can differ from observed runtime behavior.
- **Attribution:** Initial worker failure was an agent/tool execution failure. The later implementation is verified only by repository tests and static checks.
- **Search:** Reviewed feeder, durable queue, admission, Demucs, feature pipeline, scheduler, gatekeeper, single-task mode, resource planner, and DB state code.
- **Correction:** The generated worker TaskReport had claimed completion inaccurately; it now records the timeout and points to this verified completion record.
- **Emotion:** N/A.
- **Thoughts:** Keep one serialized downstream owner while retaining stem-level parallelism. Treat measured OS RAM as the current boundary, and do not add an estimate for already observed shared-GPU allocations a second time.

PromptDefect: none identified. AgentDefect: initial worker timed out and generated an inaccurate completion report; corrected and continued under explicit user authorization.
