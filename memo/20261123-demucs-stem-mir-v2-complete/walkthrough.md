# Walkthrough: 20261123-demucs-stem-mir-v2-complete

## Completed Milestones on `v2`
- **Step 0**: Pushed pending changes to `origin/master` (`a0ee486`) and created/pushed `origin/v2`.
- **Phase 1 (`P1-M1`..`P1-M3`)**:
  - Added `analyzer/common_signal.py`, `analyzer/common_stereo.py`, `analyzer/common_cross_stem.py`, `analyzer/common_dispatch.py`.
  - Fixed `"vocal"` -> `"vocals"` chord bug and populated `rms_min/median`, `centroid_min/median`.
  - Wired pre-downmix `(2, N)` stereo & cross-stem extraction in `models.py` & `demucs_daemon.py` forwarded via `orchestrator/dispatcher/daemon.go` (`StemInfo`) to `worker_cpu_daemon.py` with 0 bytes of SHM expansion.
  - Wired `voice_cpp`, `audio_cutoff_lufs`, `psychoacoustics_din45692`, and `structure_ssm` into production extraction.
- **Phase 2 (`P2-M1`..`P2-M3`)**:
  - Pruned `drums` CQT/chroma/tonal compute (with neutral schema elements) and non-bass `subbass_env_mean`.
  - Added `analyzer/common_pitch_harmony.py` (`voiced_f0_contour_morphism`, `vocal_character_morphism`, `bass_character_morphism`, `harmonic_instrument_character_morphism`), `drum_band_onset_morphism`, and Kick-Bass `rhythm_pocket_and_ducking_morphism` integration.
- **Phase 3 (`P3-M1`..`P3-M2`)**:
  - Added `harmonic_bus_chroma_morphism`, `cross_stem_harmonic_tension_morphism`, `multistem_structure_ssm_morphism`, and `run_selective_stem_essentia_pure` (`[plugins.stem_essentia]`).
  - Updated `zig/migrate_features.py` with stereo + CUE-slice support and verified live backup archival (`trg_archive_library_flac` archived `id=98390` into `raw.library_flac_history`, `91050 -> 91051`).

## Remaining Repo-Wide Items Outside §14 Scope
- Hardware-dependent实機 checks in `issues.md` lines 112–119 and 234 (live RTX 5070 Ti batch/RAM admission soak & live CUE/single workload observation) require running the full Go orchestrator batch on the target audio library.
