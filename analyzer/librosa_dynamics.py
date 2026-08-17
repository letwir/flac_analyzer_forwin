"""
analyzer/librosa_dynamics.py
============================
音響ダイナミクス特徴量（RMS, Energy, Crest Factor, SNR, Dynamics Range）の抽出モジュールですわ！
"""

import logging
from typing import Any
import librosa
import numpy as np

from .core import AudioContext, FeatureExtractor, FIXED_SEQ_FRAMES, _resample_to_fixed_frames
from .registry_plugins import BasePlugin, register_plugin
from .types_features import RmsFeatures

logger = logging.getLogger("analyzer.librosa_dynamics")


def _calc_rms_stats(rms: np.ndarray) -> dict[str, float]:
    """RMS時系列から7スカラー統計量を算出しますわ！"""
    mean_val = float(np.mean(rms))
    std_val = float(np.std(rms))
    peak_val = float(np.max(rms)) if len(rms) > 0 else 0.0
    max_val = peak_val
    min_val = float(np.min(rms)) if len(rms) > 0 else 0.0
    median_val = float(np.median(rms)) if len(rms) > 0 else 0.0
    abs_rms = np.abs(rms)
    s = float(np.sum(abs_rms))
    if s < 1e-10:
        entropy_val = 0.0
    else:
        p = abs_rms / s
        entropy_val = float(-np.sum(p * np.log2(p + 1e-10)))
    return {
        "mean": mean_val,
        "std": std_val,
        "peak": peak_val,
        "max": max_val,
        "min": min_val,
        "median": median_val,
        "entropy": entropy_val,
    }


def extract_rms_obj(ctx: AudioContext) -> RmsFeatures:
    """RMS時系列から RmsFeatures オブジェクトを構築しますわ！"""
    raw_rms = librosa.feature.rms(S=ctx.spectro)[0]
    safe_rms = np.nan_to_num(raw_rms, nan=0.0, posinf=0.0, neginf=0.0)
    stats = _calc_rms_stats(safe_rms)
    return RmsFeatures(
        mean=stats["mean"],
        std=stats["std"],
        entropy=stats["entropy"],
        seq=_resample_to_fixed_frames(safe_rms, FIXED_SEQ_FRAMES),
        peak=stats["peak"],
    )


def extract_energy(ctx: AudioContext) -> float:
    """波形の短時間エネルギー平均 (Energy) を算出しますわ！"""
    if len(ctx.y) == 0:
        return 0.0
    return float(np.mean(ctx.y**2))


def extract_crest_factor(ctx: AudioContext) -> float:
    """信号の波高率 (Crest Factor = Peak / RMS) を算出しますわ！"""
    peak = float(np.max(np.abs(ctx.y))) if len(ctx.y) > 0 else 0.0
    rms_val = float(np.sqrt(np.mean(ctx.y**2))) if len(ctx.y) > 0 else 0.0
    if rms_val < 1e-9:
        return 0.0
    return float(peak / rms_val)


def extract_snr(ctx: AudioContext) -> float | None:
    """信号対雑音比 (SNR, dB) を返却しますわ（AudioContextに保持されている場合）。"""
    return ctx._snr_val


def extract_dynamics_range_seq(ctx: AudioContext) -> list[float]:
    """短時間フレームごとのダイナミックレンジ時系列 (32-frames) ですわ。"""
    raw_rms = librosa.feature.rms(S=ctx.spectro)[0]
    safe_rms = np.nan_to_num(raw_rms, nan=0.0, posinf=0.0, neginf=0.0)
    db_rms = librosa.amplitude_to_db(safe_rms, ref=np.max)
    return _resample_to_fixed_frames(db_rms, FIXED_SEQ_FRAMES)


@register_plugin(
    name="librosa_dynamics",
    description="RMS, Energy, Crest Factor, SNR, Dynamics Range",
    enabled_by_default=True,
    priority=10,
)
class LibrosaDynamicsPlugin(BasePlugin):
    def extract(self, ctx: AudioContext) -> dict[str, Any]:
        rms_feat = extract_rms_obj(ctx)
        energy_val = extract_energy(ctx)
        crest_val = extract_crest_factor(ctx)
        snr_val = extract_snr(ctx)
        dyn_range_seq = extract_dynamics_range_seq(ctx)
        return {
            "rms_mean": rms_feat.mean,
            "rms_std": rms_feat.std,
            "rms_peak": rms_feat.peak,
            "rms_entropy": rms_feat.entropy,
            "rms_seq": rms_feat.seq,
            "energy": energy_val,
            "crest_factor": crest_val,
            "snr": snr_val,
            "dynamics_range_seq": dyn_range_seq,
        }
