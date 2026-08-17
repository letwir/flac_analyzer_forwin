"""
analyzer/scipy_stats.py
=======================
Scipy ベースの高次統計特徴量（Skewness/Kurtosis, Hilbert Envelope/InstFreq, Spectral/Temporal Peaks）モジュールですわ！
"""

import logging
from typing import Any
import numpy as np

from .core import AudioContext, FeatureExtractor
from .registry_plugins import BasePlugin, register_plugin
from .stats import (
    _calc_hilbert_features,
    _calc_peak_features,
    _calc_scipy_stats_features,
)
from .types_features import HilbertFeatures, PeakFeatures, ScipyStatsFeatures

logger = logging.getLogger("analyzer.scipy_stats")


def extract_scipy_stats(ctx: AudioContext) -> ScipyStatsFeatures:
    """スペクトルの歪度・尖度統計特徴量を算出しますわ！"""
    return _calc_scipy_stats_features(ctx)


def extract_hilbert(ctx: AudioContext) -> HilbertFeatures:
    """Hilbert変換による瞬時振幅包絡および瞬時周波数特徴量を算出しますわ！"""
    return _calc_hilbert_features(ctx)


def extract_peak(ctx: AudioContext) -> PeakFeatures:
    """時間領域および周波数領域のピーク統計特徴量を算出しますわ！"""
    return _calc_peak_features(ctx)


@register_plugin(
    name="scipy_stats",
    description="Scipy Skewness/Kurtosis, Hilbert Envelope/InstFreq, Spectral/Temporal Peaks",
    enabled_by_default=True,
    priority=70,
)
class ScipyStatsPlugin(BasePlugin):
    def extract(self, ctx: AudioContext) -> dict[str, Any]:
        scipy_stats_feat = extract_scipy_stats(ctx)
        hilbert_feat = extract_hilbert(ctx)
        peak_feat = extract_peak(ctx)

        return {
            "scipy_stats_feat": scipy_stats_feat,
            "hilbert_feat": hilbert_feat,
            "peak_feat": peak_feat,
        }
