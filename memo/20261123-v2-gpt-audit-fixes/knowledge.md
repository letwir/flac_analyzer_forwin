# Knowledge: 20261123-v2-gpt-audit-fixes

- **source**: `F:\repo\flac_analyzer_forwin` (`v2` branch)
- **scope**: `project:flac_analyzer_forwin`
- **verified-date**: 2026-11-23
- **supersedes**: N/A

## Resolved Algorithmic, Memory, and Spec Defects

1. **Bounded-Lag Cross-Correlation (`analyzer/common_signal.py`)**:
   - Replaced `np.correlate(a, b, mode="full")` ($O(N^2)$) in `xcorr_lag_morphism_Complex` with bounded inner products over `[-max_lag_frames, +max_lag_frames]` ($O(K \cdot N)$), verified on $N = 165,000$ frames (55 min at 20ms hop).
2. **Long-Track Memory & Wavefront Safety (`common_signal.py`, `common_cross_stem.py`, `common_pitch_harmony.py`, `common_stereo.py`)**:
   - Replaced full-track FFTs and `float64` full-track array allocations with native-SR bounded Welch windows (`<= 128 x 4096`), Parseval chunked 20ms frame band-power integration (`4096` frames/chunk), `1,048,576`-sample chunked energy dot products, and bounded native windows (`<= 262,144` samples) for pre-downmix CQT/YIN.
3. **Anti-Aliased Low-Band Stereo Coherence (`analyzer/common_stereo.py`)**:
   - Eliminated `[::step]` decimation in `_compute_low_band_coherence`; replaced with up to 64 contiguous `4096`-sample Hann windows at native `sr`, verified against 4410Hz out-of-phase alias traps on 30s 44.1kHz signals.
4. **18D Multi-Stem + Harmonic Chroma SSM (`analyzer/structure_ssm.py`)**:
   - Updated `multistem_structure_ssm_morphism_Large` to stack 6-stem RMS curves (`6x32`) with 12D pure harmonic chroma (`12x32`), returning `state_dim=18` and `chroma_included=True`.
5. **Eliminated Double Plugin Execution (`analyzer/common_dispatch.py`)**:
   - `build_stem_extractor_morphism` skips re-running `extract_stem_plugins_morphism` when `toml_cfg is None` and `base_extractor` already populated plugin outputs on `RawFeatures`.
