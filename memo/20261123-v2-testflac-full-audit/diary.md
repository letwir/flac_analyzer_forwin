# Diary: `testFLAC/*` Full 185-Track Run & PostgreSQL v1-vs-v2 Audit (`2026-11-23`)

- Executed all 11 files (`185` slices, ~796.4 min audio) through `orchestrator.exe -single-file ... -force` into live PostgreSQL `raw.library_flac` with `trg_archive_library_flac` preserving v1 backups in `raw.library_flac_history`.
- Delegated full JSONB recursive v1-vs-v2 regression audit to subagent `b3195d17`.
- Subagent discovered all v2 features (`stereo`, `cross_stem`, `multistem_structure`, stem profiles, `453/453` Essentia keys) were 100% healthy, and isolated 1 genuine unjustified regression: `extract_section()` in `analyzer/librosa_dsp.py` had been stubbed out in `9af97d28`.
- Restored `extract_section_Large` from git history (`9af97d28^`), verified 35/35 Python tests & Go tests, re-ran 3 E2E files + updated remaining 175 DB tracks, and confirmed via subagent re-audit: **0 Musically Unjustified Regressions**.
