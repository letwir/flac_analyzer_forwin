# Walkthrough: 20261123-demucs-stem-mir-plan

## Summary
- Formulated a 3-Phase architecture and implementation plan for Demucs 6-stem (`vocals`, `drums`, `bass`, `guitar`, `piano`, `other`) + `mix` analysis in `F:\repo\flac_analyzer_forwin`.
- Decoupled pure mathematical DSP/MIR morphisms into 5 reusable common Python modules (`analyzer/common_signal.py`, `analyzer/common_stereo.py`, `analyzer/common_cross_stem.py`, `analyzer/common_pitch_harmony.py`, `analyzer/common_dispatch.py`).
- Appended Section 14 to `issues.md` with Major (Phase 1–3), Medium (`P*-M*`), and Small (`P*-M*-S*`) tickets plus a `POLYGLYPH×SIGMA/1` loop-coding contract block (`#SIGMA LRF/1`, convergence propositions `P1..P6`, and dispatch seed template).

## Verification
- `git diff --check -- F:\repo\flac_analyzer_forwin\issues.md` -> exit code 0 (`1 file changed, 112 insertions(+)`).
