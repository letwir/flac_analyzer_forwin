# Walkthrough: Full `testFLAC/*` Pipeline Run & Subagent v1-vs-v2 PostgreSQL Audit

- **Corpus**: All 11 files in `testFLAC/*` (`185` slices, `796.4` total audio minutes, up to `176.4kHz/24-bit` and `75.1m`/`48.6m` single tracks).
- **Execution**:
  - Batch 1 (`01_01_シンカンセンスゴイカタイアイス.flac`, `03 Nichiei.flac`) and Batch 2 (all remaining 9 files / 183 slices via `orchestrator.exe -single-file ... -force`) exited `0` cleanly with zero OOM or VRAM leaks (`1558–1588 MiB` idle post-run VRAM).
- **Subagent Audit (`b3195d17-3e8e-48c8-9474-964239d5172c`)**:
  - Initial audit caught `features.mix.scalars.section` zero regression (`142/174` paired tracks).
  - After restoring `extract_section` (`c92ddf3`), E2E re-running 3 files (10 tracks), and updating the remaining 175 tracks (`missing=0`), subagent re-audit confirmed:
    - `185/185` rows complete, `0` NaN/Inf, `0` sequence length decreases, `453/453` Essentia prediction keys across `185/185` rows.
    - `185/185` non-zero `section_count` & `drop_position` (`v1_nz -> v2_0 = 0`, `exact_match = 142/174`), `184/185` non-zero `section_length_std` (`1` single-section SFX track = `0.0`).
    - `185/185` populated v2 modules (`stereo`, `cross_stem`, `multistem_structure`, stem profiles) and `185/185` `chorus_end_sec > chorus_start_sec`.
    - **0 Musically Unjustified Regressions remaining**.
