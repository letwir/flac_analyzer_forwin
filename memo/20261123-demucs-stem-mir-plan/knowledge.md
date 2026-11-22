# Knowledge: 20261123-demucs-stem-mir-plan

- **source**: `F:\repo\flac_analyzer_forwin` (`models.py`, `demucs_daemon.py`, `analyzer/librosa_dsp.py`, `analyzer/essentia_dsp.py`, `analyzer/types_features.py`, `orchestrator/dispatcher/pipeline_demucs.go`, `orchestrator/dispatcher/analysis_decision.go`)
- **scope**: `project:flac_analyzer_forwin`
- **verified-date**: 2026-11-23
- **supersedes**: N/A

## Verified Facts & Architectural Invariants

1. **Pre-Downmix Stereo & Cross-Stem Hook (`models.py:366-383`, `demucs_daemon.py:142-215`)**:
   - `htdemucs_6s` outputs stereo `(2, N)` arrays for all 6 stems (`vocals`, `drums`, `bass`, `guitar`, `piano`, `other`), which are immediately averaged to mono via `np.mean(stem_y, axis=0)` before SHM/file serialization.
   - Computing stereo morphisms (`analyzer/common_stereo.py`: Panning ILD, Mid/Side Width $W$, L/R phase correlation $\rho_{LR}$, low-band phase coherence) and cross-stem wave morphisms (`analyzer/common_cross_stem.py`: Energy Share, Active Ratio, Reconstruction Residual SNR $\mathrm{mix}-\sum y_s$, Vocal-Band Masking, 32-frame Arrangement Arc, Kick-Bass Pocket & Sidechain Ducking) immediately prior to mono downmix requires **0 bytes of additional SHM allocation** and preserves Wavefront single-stem streaming.
2. **Existing Pipeline Gaps**:
   - `analyzer/essentia_dsp.py:21` checks `"vocal"` (singular) instead of `"vocals"`, causing `_calc_chord_sequence` on the `vocals` stem to return `["C"] * 32`.
   - `analyzer/librosa_dsp.py:294-303` hardcodes `rms_min=0.0`, `rms_median=0.0`, `centroid_min=0.0`, `centroid_median=0.0` despite `_calc_rms_stats` / `_calc_centroid_stats` computing them.
   - `analyzer/librosa_dsp.py:351-471` (`STEM_CONFIGS`) duplicates the exact same warmup and extractor across `mix` and all 6 stems; plugins `voice_cpp.py`, `audio_cutoff_lufs.py`, `psychoacoustics_din45692.py`, and `structure_ssm.py` are enabled in `analyzer.toml` and serialized in `types_features.py` but not wired into `librosa_extractor_v4` / `worker_cpu_daemon.py`.
3. **Category-Theoretic Common Pure Modules (`analyzer/common_*.py`)**:
   - Separating `common_signal.py`, `common_stereo.py`, `common_cross_stem.py`, `common_pitch_harmony.py`, and `common_dispatch.py` isolates pure mathematical morphisms $\mathrm{Mor}(\mathrm{Domain}\to\mathrm{Codomain})$ from SHM/IPC/ONNX effects.
