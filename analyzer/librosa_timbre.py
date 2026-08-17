"""
analyzer/librosa_timbre.py
==========================
音色特徴量（MFCC 13次元統計・時系列）の抽出モジュールですわ！
"""

import logging
from typing import Any
import librosa
import numpy as np

from .core import AudioContext, FeatureExtractor, FIXED_SEQ_FRAMES, LIBROSA_LOCK, _resample_to_fixed_frames
from .registry_plugins import BasePlugin, register_plugin
from .types_features import MfccFeatures

logger = logging.getLogger("analyzer.librosa_timbre")


def extract_mfcc_obj(ctx: AudioContext, n_mfcc: int = 13) -> MfccFeatures:
    """MFCC (13次元) の統計値および 32-frames 時系列を算出しますわ！"""
    with LIBROSA_LOCK:
        raw_mfcc = librosa.feature.mfcc(S=librosa.power_to_db(ctx.mel), n_mfcc=n_mfcc)
    safe_mfcc = np.nan_to_num(raw_mfcc, nan=0.0, posinf=0.0, neginf=0.0)

    means = [float(v) for v in np.mean(safe_mfcc, axis=1)]
    stds = [float(v) for v in np.std(safe_mfcc, axis=1)]

    entropies: list[float] = []
    for row in safe_mfcc:
        abs_row = np.abs(row)
        s = float(np.sum(abs_row))
        if s < 1e-10:
            entropies.append(0.0)
        else:
            p = abs_row / s
            entropies.append(float(-np.sum(p * np.log2(p + 1e-10))))

    seqs = [_resample_to_fixed_frames(row, FIXED_SEQ_FRAMES) for row in safe_mfcc]

    return MfccFeatures(
        mean=means,
        std=stds,
        entropy=entropies,
        seq=seqs,
    )


def extract_mfccs(ctx: AudioContext, n_mfcc: int = 13) -> list[float]:
    """後方互換用: 各次数の平均値を返却しますわ！"""
    feat = extract_mfcc_obj(ctx, n_mfcc=n_mfcc)
    return feat.mean


@register_plugin(
    name="librosa_timbre",
    description="MFCC 13-dimensional statistics and fixed-length sequences",
    enabled_by_default=True,
    priority=50,
)
class LibrosaTimbrePlugin(BasePlugin):
    def extract(self, ctx: AudioContext) -> dict[str, Any]:
        mfcc_feat = extract_mfcc_obj(ctx, n_mfcc=13)
        return {
            "mfccs": mfcc_feat.mean,
            "mfcc": mfcc_feat.seq,
        }
