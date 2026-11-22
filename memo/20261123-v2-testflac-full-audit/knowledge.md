# Knowledge: `testFLAC/*` Full 185-Track v2 Audit & `extract_section` Restoration

- **source**: `F:\repo\flac_analyzer_forwin` (`origin/v2`)
- **scope**: `project:flac_analyzer_forwin`
- **verified-date**: 2026-11-23

## Resolved Issues During Full `testFLAC/*` Campaign (11 Files, 185 Slices)

1. **Restored `extract_section(ctx)` Regressed Stub (`analyzer/librosa_dsp.py`)**:
   - Subagent PostgreSQL audit (`b3195d17`) compared all 185 v2 rows (`174` paired against `raw.library_flac_history` v1) and detected `features.mix.scalars.section.{section_count, section_length_std, drop_position}` had been zeroed across `142/174` paired tracks because `extract_section()` was stubbed during commit `9af97d28`.
   - Restored `extract_section_Large(ctx)` (`9af97d28^` onset-RMS boundary logic), added unit test `test_extract_section_restores_mix_section_metrics_and_skips_stems`, re-verified 3 files E2E via `orchestrator.exe`, backfilled remaining 175 tracks, and re-audited: `v1_nz -> v2_0 = 0`, `exact_match = 142/174`, `v1_0 -> v2_nz = 32`.
2. **Force-Reanalysis Conflict & Backup Trigger Preservation (`orchestrator/dispatcher/ingest_pgx.go`)**:
   - Added `ON CONFLICT (audio_hash) DO UPDATE` in `UpsertTrackDirectly` when `task.AnalysisRowID == 0 && task.Force`, preserving production `filepath` (`M:\Music\...`, `N:\Music\...`) while firing `trg_archive_library_flac` into `raw.library_flac_history`.
3. **Long-Track Chorus Span Rounding (`analyzer/structure_ssm.py`)**:
   - Changed `int(15.0 / time_per_frame)` to `max(1, int(round(15.0 / time_per_frame)))`, achieving `185/185` tracks with `chorus_end_sec > chorus_start_sec`.
