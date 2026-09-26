# Walkthrough: Demucs downstream ticket attempt

- Task ID: 2026-09-26-demucs-ticket-admission
- Intended behavior: Before launching Demucs, reserve a conservative per-track downstream RAM ticket with a one-track default, and park tasks that cannot receive it. Preserve cancellation and release ownership.
- Observed outcome: An agy worker attempt failed with `agy structured response is not valid JSON`; no target source file changed. No Go tests were run.
- Single-file caveat: `RunSingleTask` is reached from an exclusive CLI mode; queue-to-CLI rerouting would require a new durable lane and concurrency contract. The long-track workload classifier already limits CPU consumers to one and disk fallback exists.
- Checks: `git status --short`, `git diff --check`, `git diff --stat`, exact target `git diff` inspected; only pre-existing untracked memo folders remained.
- Residual risk: Existing Demucs/feature overlap and RAM pressure remain unchanged. No runtime OOM safety improvement is claimed.
