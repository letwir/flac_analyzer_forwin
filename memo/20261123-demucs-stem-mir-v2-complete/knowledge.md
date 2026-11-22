# Knowledge: 20261123-demucs-stem-mir-v2-complete

- **source**: `F:\repo\flac_analyzer_forwin` (`v2` branch)
- **scope**: `project:flac_analyzer_forwin`
- **verified-date**: 2026-11-23
- **supersedes**: `20261123-demucs-stem-mir-plan`

## Implemented Architecture & Verified Facts

1. **Five Pure Category-Theoretic Common Modules (`analyzer/common_*.py`)**:
   - `analyzer/common_signal.py`: `envelope_stats7_morphism`, `subband_energy_morphism`, `xcorr_lag_morphism`, `decay_slope_morphism`, `drum_band_onset_morphism`.
   - `analyzer/common_stereo.py`: `stereo_field_morphism` (`pan_mean`, `pan_std`, `stereo_width`, `lr_correlation`, `low_band_phase_coherence`, `pan_seq_32`, `width_seq_32`) and `mono_downmix_morphism`.
   - `analyzer/common_cross_stem.py`: `cross_stem_foundation_morphism` (energy share, active ratio, stem SNR, residual SNR, vocal-to-accompaniment dB, 300Hz–4kHz vocal band masking, 32-frame arrangement arc, rhythm pocket/sidechain ducking, harmonic bus, harmonic tension, multi-stem structure SSM) and `rhythm_pocket_and_ducking_morphism`.
   - `analyzer/common_pitch_harmony.py`: `voiced_f0_contour_morphism` (parameterized `(fmin, fmax)` + 4–8Hz vibrato rate/extent + 12-TET quantization), `vocal_character_morphism`, `bass_character_morphism`, `harmonic_instrument_character_morphism` (`guitar`/`piano`/`other`), `harmonic_bus_chroma_morphism`, and `cross_stem_harmonic_tension_morphism`.
   - `analyzer/common_dispatch.py`: `resolve_stem_warmup_pure`, `resolve_stem_plugins_pure`, `extract_stem_plugins_morphism`, `build_stem_extractor_morphism`.
2. **Zero-SHM-Bloat Stereo & Cross-Stem Hook**:
   - `models.py` (`HTDemucsSeparator.separate`) and `demucs_daemon.py` compute `stereo_field_morphism` and `cross_stem_foundation_morphism` immediately before `mono_downmix_morphism`, forwarding lightweight metadata via Go `StemInfo` (`orchestrator/dispatcher/daemon.go`) to `worker_cpu_daemon.py` and `RawFeatures.to_postgres_dict()` while keeping SHM 1D mono.
3. **Pruned Wasteful Stem Compute & Wired 4 Plugins**:
   - `drums` skips `chroma`, `chroma_cqt`, `tonnetz`, `key`, `chord_sequence` with schema-compatible neutral elements; `vocals`/`piano`/`guitar` skip `subbass_env_mean` in `analyzer/tensor_dsp.py`.
   - `voice_cpp`, `audio_cutoff_lufs`, `psychoacoustics_din45692`, and `structure_ssm` are wired via `common_dispatch.py`, plus `multistem_structure_ssm_morphism` and selective stem Essentia (`run_selective_stem_essentia_pure`).
4. **Live PostgreSQL Backup Verification (`zig/migrate_features.py`)**:
   - Trigger `trg_archive_library_flac` (`tgenabled='O'`) verified active on `raw.library_flac` archiving into `raw.library_flac_history`.
   - Single-row live migration (`--migrate-features --limit 1` on `id=98390`) incremented `raw.library_flac_history` from `91050` to `91051` and persisted `mix.scalars.stereo`, `voice_cpp`, and `audio_cutoff_lufs`.
