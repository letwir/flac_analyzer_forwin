"""
analyzer/librosa_rhythm.py
==========================
リズム・テンポ・グルーヴ特徴量（BPM, Tempogram, Onset Strength, Groove, Beat Regularity/Stability）の抽出モジュールですわ！
"""

import logging
import math
from typing import Any
import librosa
import numpy as np

from .core import AudioContext, FeatureExtractor, FIXED_SEQ_FRAMES, LIBROSA_LOCK, _resample_to_fixed_frames
from .registry_plugins import BasePlugin, register_plugin
from .types_features import GrooveFeatures, OnsetFeatures, TempogramFeatures

logger = logging.getLogger("analyzer.librosa_rhythm")


def extract_bpm(ctx: AudioContext) -> float:
    """BPM (Tempo) を返却しますわ！"""
    bpm_val, _ = ctx.tempobeat
    return float(bpm_val)


def extract_tempogram(ctx: AudioContext) -> TempogramFeatures:
    """Tempogram の統計値および時系列ですわ！"""
    tempogram = ctx.tempogram
    mean_val = float(np.mean(tempogram))
    std_val = float(np.std(tempogram))
    peak_val = float(np.max(tempogram)) if tempogram.size > 0 else 0.0

    abs_tg = np.abs(tempogram)
    s = float(np.sum(abs_tg))
    if s < 1e-10:
        entropy_val = 0.0
    else:
        p = abs_tg / s
        entropy_val = float(-np.sum(p * np.log2(p + 1e-10)))

    # 平均テンポ時系列 (32-frames)
    tempo_curve = np.mean(tempogram, axis=0) if tempogram.ndim > 1 else tempogram
    tempo_seq = _resample_to_fixed_frames(tempo_curve, FIXED_SEQ_FRAMES)

    return TempogramFeatures(
        mean=mean_val,
        std=std_val,
        peak=peak_val,
        entropy=entropy_val,
        tempo_seq=tempo_seq,
    )


def extract_onset(ctx: AudioContext) -> OnsetFeatures:
    """Onset Strength 統計値および自己相関を算出しますわ！"""
    onset_env = ctx.onset_env
    if len(onset_env) == 0:
        return OnsetFeatures()

    mean_val = float(np.mean(onset_env))
    std_val = float(np.std(onset_env))
    max_val = float(np.max(onset_env))
    p25 = float(np.percentile(onset_env, 25))
    p50 = float(np.percentile(onset_env, 50))
    p75 = float(np.percentile(onset_env, 75))
    crest = float(max_val / (mean_val + 1e-9))

    # 自己相関 (Autocorrelation)
    with LIBROSA_LOCK:
        ac = librosa.autocorrelate(onset_env, max_size=FIXED_SEQ_FRAMES)
    ac_norm = ac / (np.max(ac) + 1e-9)
    autocorr = [float(v) for v in ac_norm[:FIXED_SEQ_FRAMES]]

    # 歪度・尖度
    from scipy.stats import kurtosis, skew
    skew_val = float(skew(onset_env)) if len(onset_env) > 2 else 0.0
    kurt_val = float(kurtosis(onset_env)) if len(onset_env) > 3 else 0.0

    return OnsetFeatures(
        mean=mean_val,
        std=std_val,
        max=max_val,
        p25=p25,
        p50=p50,
        p75=p75,
        crest=crest,
        autocorr=autocorr,
        skew=0.0 if (math.isnan(skew_val) or math.isinf(skew_val)) else skew_val,
        kurt=0.0 if (math.isnan(kurt_val) or math.isinf(kurt_val)) else kurt_val,
        onset_strength_seq=_resample_to_fixed_frames(onset_env, FIXED_SEQ_FRAMES),
    )


def extract_groove(ctx: AudioContext) -> GrooveFeatures:
    """Groove / Syncopation 特徴量を算出しますわ！"""
    _, beats = ctx.tempobeat
    if len(beats) < 4:
        return GrooveFeatures(swing_ratio=1.0, syncopation_index=0.0, groove_class="straight")

    beat_intervals = np.diff(beats)
    even_intervals = beat_intervals[0::2]
    odd_intervals = beat_intervals[1::2]

    if len(even_intervals) > 0 and len(odd_intervals) > 0:
        mean_even = float(np.mean(even_intervals))
        mean_odd = float(np.mean(odd_intervals))
        swing_ratio = float(mean_even / (mean_odd + 1e-9))
    else:
        swing_ratio = 1.0

    # シンコペーション指数
    onset = ctx.onset_env
    syncopation = float(np.std(beat_intervals) / (np.mean(beat_intervals) + 1e-9)) if len(beat_intervals) > 0 else 0.0

    groove_class = "straight"
    if swing_ratio > 1.4:
        groove_class = "shuffle"
    elif swing_ratio > 1.15:
        groove_class = "swing"

    return GrooveFeatures(
        swing_ratio=1.0 if (math.isnan(swing_ratio) or math.isinf(swing_ratio)) else swing_ratio,
        syncopation_index=0.0 if (math.isnan(syncopation) or math.isinf(syncopation)) else syncopation,
        groove_class=groove_class,
    )


def extract_beat_regularity(ctx: AudioContext) -> float | None:
    """Beat Regularity (ビート間隔の一貫性) を算出しますわ！"""
    _, beats = ctx.tempobeat
    if len(beats) < 4:
        return None
    intervals = np.diff(beats)
    mean_int = float(np.mean(intervals))
    if mean_int < 1e-9:
        return 0.0
    reg = float(1.0 - min(1.0, float(np.std(intervals)) / mean_int))
    return reg if not math.isnan(reg) else 0.0


def extract_beat_stability(ctx: AudioContext) -> float:
    """Beat Stability (テンポ安定度) を算出しますわ！"""
    _, beats = ctx.tempobeat
    if len(beats) < 4:
        return 0.0
    intervals = np.diff(beats)
    cv = float(np.std(intervals) / (np.mean(intervals) + 1e-9))
    stab = float(max(0.0, 1.0 - cv))
    return stab if not math.isnan(stab) else 0.0


@register_plugin(
    name="librosa_rhythm",
    description="BPM, Tempogram, Onset Strength, Groove, Beat Regularity/Stability",
    enabled_by_default=True,
    priority=40,
)
class LibrosaRhythmPlugin(BasePlugin):
    def extract(self, ctx: AudioContext) -> dict[str, Any]:
        bpm_val = extract_bpm(ctx)
        tg_feat = extract_tempogram(ctx)
        onset_feat = extract_onset(ctx)
        groove_feat = extract_groove(ctx)
        regularity = extract_beat_regularity(ctx)
        stability = extract_beat_stability(ctx)

        return {
            "bpm": bpm_val,
            "tempogram_feat": tg_feat,
            "tempogram_tempo": tg_feat.tempo_seq,
            "onset_feat": onset_feat,
            "groove": groove_feat,
            "beat_regularity": regularity,
            "beat_stability": stability,
        }
