"""
analyzer/librosa_spectral.py
============================
スペクトル周波数特性特徴量（Centroid, Rolloff, ZCR, Flatness, Bandwidth, Contrast）の抽出モジュールですわ！
"""

import logging
from typing import Any
import librosa
import numpy as np

from .core import AudioContext, FeatureExtractor, FIXED_SEQ_FRAMES, LIBROSA_LOCK, _resample_to_fixed_frames
from .registry_plugins import BasePlugin, register_plugin
from .types_features import SpectralCentroidFeatures, SpectralRolloffFeatures, ZcrFeatures

logger = logging.getLogger("analyzer.librosa_spectral")


def _calc_centroid_stats(centroid: np.ndarray) -> dict[str, float]:
    """Spectral Centroid時系列から7スカラー統計量を算出しますわ！"""
    mean_val = float(np.mean(centroid))
    std_val = float(np.std(centroid))
    peak_val = float(np.max(centroid)) if len(centroid) > 0 else 0.0
    max_val = peak_val
    min_val = float(np.min(centroid)) if len(centroid) > 0 else 0.0
    median_val = float(np.median(centroid)) if len(centroid) > 0 else 0.0
    abs_cent = np.abs(centroid)
    s = float(np.sum(abs_cent))
    if s < 1e-10:
        entropy_val = 0.0
    else:
        p = abs_cent / s
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


def extract_centroid_obj(ctx: AudioContext) -> SpectralCentroidFeatures:
    """Spectral Centroid時系列から SpectralCentroidFeatures を構築しますわ！"""
    safe_cent = ctx.centroid
    stats = _calc_centroid_stats(safe_cent)
    return SpectralCentroidFeatures(
        mean=stats["mean"],
        std=stats["std"],
        entropy=stats["entropy"],
        seq=_resample_to_fixed_frames(safe_cent, FIXED_SEQ_FRAMES),
        peak=stats["peak"],
    )


def extract_rolloff(ctx: AudioContext) -> SpectralRolloffFeatures:
    """Spectral Rolloff (85%) を算出しますわ！"""
    with LIBROSA_LOCK:
        raw_rolloff = librosa.feature.spectral_rolloff(S=ctx.spectro, sr=ctx.sr, roll_percent=0.85)[0]
    safe_rolloff = np.nan_to_num(raw_rolloff, nan=0.0, posinf=0.0, neginf=0.0)
    return SpectralRolloffFeatures(
        mean=float(np.mean(safe_rolloff)),
        std=float(np.std(safe_rolloff)),
        seq=_resample_to_fixed_frames(safe_rolloff, FIXED_SEQ_FRAMES),
    )


def extract_zcr(ctx: AudioContext) -> ZcrFeatures:
    """Zero Crossing Rate (ZCR) を算出しますわ！"""
    raw_zcr = librosa.feature.zero_crossing_rate(y=ctx.y)[0]
    safe_zcr = np.nan_to_num(raw_zcr, nan=0.0, posinf=0.0, neginf=0.0)
    return ZcrFeatures(
        mean=float(np.mean(safe_zcr)),
        std=float(np.std(safe_zcr)),
        seq=_resample_to_fixed_frames(safe_zcr, FIXED_SEQ_FRAMES),
    )


def extract_flatness(ctx: AudioContext) -> float:
    """Spectral Flatness を算出しますわ！"""
    raw_flatness = librosa.feature.spectral_flatness(S=ctx.spectro)[0]
    safe_flat = np.nan_to_num(raw_flatness, nan=0.0, posinf=0.0, neginf=0.0)
    return float(np.mean(safe_flat))


def extract_spectral_bandwidth(ctx: AudioContext) -> float:
    """Spectral Bandwidth を算出しますわ！"""
    raw_bw = librosa.feature.spectral_bandwidth(S=ctx.spectro, sr=ctx.sr)[0]
    safe_bw = np.nan_to_num(raw_bw, nan=0.0, posinf=0.0, neginf=0.0)
    return float(np.mean(safe_bw))


def extract_contrast(ctx: AudioContext) -> list[float]:
    """Spectral Contrast (7帯域平均) を算出しますわ！"""
    with LIBROSA_LOCK:
        raw_contrast = librosa.feature.spectral_contrast(S=ctx.spectro, sr=ctx.sr)
    safe_contrast = np.nan_to_num(raw_contrast, nan=0.0, posinf=0.0, neginf=0.0)
    band_means = np.mean(safe_contrast, axis=1)
    return [float(v) for v in band_means]


@register_plugin(
    name="librosa_spectral",
    description="Centroid, Rolloff, ZCR, Flatness, Bandwidth, Contrast",
    enabled_by_default=True,
    priority=20,
)
class LibrosaSpectralPlugin(BasePlugin):
    def extract(self, ctx: AudioContext) -> dict[str, Any]:
        cent_feat = extract_centroid_obj(ctx)
        roll_feat = extract_rolloff(ctx)
        zcr_feat = extract_zcr(ctx)
        flat_val = extract_flatness(ctx)
        bw_val = extract_spectral_bandwidth(ctx)
        contrast_vals = extract_contrast(ctx)

        # Centroid delta
        cent_seq = np.array(cent_feat.seq)
        delta_seq = np.diff(cent_seq, prepend=cent_seq[0] if len(cent_seq) > 0 else 0.0)

        return {
            "centroid_mean": cent_feat.mean,
            "centroid_std": cent_feat.std,
            "centroid_peak": cent_feat.peak,
            "centroid_entropy": cent_feat.entropy,
            "centroid_seq": cent_feat.seq,
            "centroid_delta_seq": delta_seq.tolist(),
            "rolloff_mean": roll_feat.mean,
            "rolloff_std": roll_feat.std,
            "rolloff_seq": roll_feat.seq,
            "zcr_mean": zcr_feat.mean,
            "zcr_std": zcr_feat.std,
            "zcr_seq": zcr_feat.seq,
            "flatness": flat_val,
            "spectral_bandwidth": bw_val,
            "contrast_bands": contrast_vals,
        }
