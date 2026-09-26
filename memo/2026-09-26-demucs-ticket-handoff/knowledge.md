# Demucs ticket handoff prompt task

- Task ID: 2026-09-26-demucs-ticket-handoff
- Result: Prepared a handoff prompt only; no Go code edited or tested.
- Context: The preceding attempt to implement Demucs/downstream RAM admission ended after an agy structured-response parse failure. Exact target diff inspection found no Go changes. A fresh task may independently assess and implement, following its own worker-selection rules.
- Scope: The handoff specifies before-launch ticket reservation, conservative RAM classification, release by ownership, OS memory recheck at admission, durable queue parking, and tests. Automatic single-file CLI lane rerouting is left conditional on an independently safe DB/concurrency design.
- Unknown: Implementation and runtime OOM behavior remain unverified.
