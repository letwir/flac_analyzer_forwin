# Diary: Workload-Aware Scheduler — 2026-09-24

### 2026-09-24

- **Hypothesis:** The durable queue should retain all incoming work, then schedule from SQLite using estimated cost and workload shape; worker daemons should grow only when demanded and shrink when idle.
- **Tried:** Implemented batch enqueue and size-aging claims, CUE/long/short track policy, demand-based daemon pooling, and GPU cleanup after feature branch join.
- **Rejected:** Treating the initial VRAM contention theory as proven; the supplied Task Manager images show snapshots but do not establish cause or three-hour track behavior.
- **Uncertainty:** No live-host acceptance run was performed. A file-size-only duration estimate may be inaccurate for highly compressed audio.
- **Attribution:** PromptDefect=0. AgentDefect=0 for the final implementation; a final explicit push was used to confirm the remote ref after an earlier network-limited read-only query.
- **Result:** Local build/checks passed and commit `6d764332271abeb540cd13b032f27b8e8c6f83df` was pushed. User continues debugging against the live host.

Tags: hypothesis, queue, gpu, runtime-debugging
