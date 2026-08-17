"""
analyzer/librosa_vocalpitch.py
==============================
ボーカルピッチ・旋律特徴量（Vocal F0, YIN ピッチ時系列, Dominant Pitch）の抽出モジュールですわ！
"""

import logging
from typing import Any
import librosa
import numpy as np

from constants import NOTES
from .core import AudioContext, FeatureExtractor, FIXED_SEQ_FRAMES, LIBROSA_LOCK, _resample_to_fixed_frames
from .essentia_dsp import _calc_vocal_f0_seq
from .registry_plugins import BasePlugin, register_plugin

logger = logging.getLogger("analyzer.librosa_vocalpitch")


def extract_dominant_pitch(ctx: AudioContext) -> str:
    """最も支配的な音名 (Note name) を推定しますわ！"""
    chroma_mean = np.mean(ctx.chroma, axis=1)
    max_idx = int(np.argmax(chroma_mean))
    if 0 <= max_idx < len(NOTES):
        return NOTES[max_idx]
    return "Unknown"


def extract_vocal_f0(ctx: AudioContext) -> list[float] | None:
    """YIN / Essentia による Vocal F0 ピッチ時系列 (32-frames) ですわ。"""
    return _calc_vocal_f0_seq(ctx)


@register_plugin(
    name="librosa_vocalpitch",
    description="Dominant Pitch and Vocal F0 sequence",
    enabled_by_default=True,
    priority=60,
)
class LibrosaVocalPitchPlugin(BasePlugin):
    def extract(self, ctx: AudioContext) -> dict[str, Any]:
        dominant_note = extract_dominant_pitch(ctx)
        f0_seq = extract_vocal_f0(ctx)
        return {
            "dominant_pitch": dominant_note,
            "vocal_f0_seq": f0_seq,
        }
