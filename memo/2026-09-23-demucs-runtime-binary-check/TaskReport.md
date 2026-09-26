# Task Report: Runtime Binary Verification and Demucs Prewarm Race (2026-09-23)

## Result

SSH read-only inspection on port 90 found the running process PID 4752 at `C:\Users\letwir\repo\flac_analyzer_forwin\orchestrator.exe`. Remote and local Git HEAD both equal `28af7b6b6028c38d62890ca9e08db73a543e960e`. The runtime image and `orchestrator\orchestrator.exe` have identical SHA-256 `5BEC827EA9748F2BE5E39BEB629B540460B030A5A2BBEE5E553124D59DFDB9E0`; embedded `vcs.revision` also matches HEAD. Build metadata says `vcs.modified=true`; the remote checkout currently has only `HARDWARE_SPECS.md` and `config.toml.bak` modified, so clean-tree provenance is not proven.

## Finding

Two Demucs daemon process trees were present, both parented by the live orchestrator. Current source permits a startup race: `Prewarm` drops the pool mutex while starting a process without marking `spawning`, while concurrent `Acquire` may also spawn under the capacity check; both paths can append a client. Each daemon initializes a global GPU model. This can explain pool size 2 despite configured capacity 1 and may contribute to low free VRAM; process-specific GPU memory was not measured.

## Scope

Read-only SSH, Git metadata, file hashes, and process paths only. No code, service, configuration, or process state was changed; no tests were run.
