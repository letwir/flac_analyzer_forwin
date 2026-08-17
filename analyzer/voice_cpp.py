"""
analyzer/voice_cpp.py
=====================
ケプストラム第1ピーク突出度 (Cepstral Peak Prominence: CPP / CPPS)
およびボーカルの芯・気息性 (Breathiness / Dysphonia) 解析モジュールですわ！
"""

import logging
import math
from typing import Any
import numpy as np

from .core import AudioContext, FeatureExtractor, FIXED_SEQ_FRAMES, _resample_to_fixed_frames
from .registry_plugins import BasePlugin, register_plugin
from .types_features import VoiceCppFeatures

logger = logging.getLogger("analyzer.voice_cpp")


def calc_frame_cpp(frame: np.ndarray, sr: int) -> float:
    """単一フレーム波形に対する Cepstral Peak Prominence (CPP, dB) を算出しますわ！"""
    if len(frame) < 256:
        return 0.0

    # 1. 窓関数
    windowed = frame * np.hanning(len(frame))

    # 2. リニアパワースペクトル
    fft_spec = np.abs(np.fft.rfft(windowed, n=2048))
    log_spec = np.log(fft_spec + 1e-10)

    # 3. リアルケプストラム
    cepstrum = np.real(np.fft.rfft(log_spec))
    quefrency = np.arange(len(cepstrum), dtype=np.float32)

    # ピッチ周期探索範囲 (60Hz 〜 500Hz 相当の quefrency)
    min_quef = int(sr / 500.0)
    max_quef = int(sr / 60.0)

    if max_quef >= len(cepstrum):
        max_quef = len(cepstrum) - 1
    if min_quef >= max_quef:
        return 0.0

    search_region = cepstrum[min_quef:max_quef]
    if len(search_region) == 0:
        return 0.0

    # ケプストラム第1ピーク位置
    peak_idx = min_quef + int(np.argmax(search_region))
    peak_val = float(cepstrum[peak_idx])

    # ケプストラム全体の線形回帰直線 (バックグラウンドフロア)
    x = np.arange(min_quef, max_quef, dtype=np.float32)
    y = cepstrum[min_quef:max_quef]
    if len(x) > 1:
        slope, intercept = np.polyfit(x, y, 1)
        expected_floor = float(slope * peak_idx + intercept)
    else:
        expected_floor = float(np.mean(y))

    # CPP: 回帰直線からのピーク突出度 (dB)
    cpp = max(0.0, peak_val - expected_floor)
    return float(cpp)


def extract_voice_cpp(ctx: AudioContext) -> VoiceCppFeatures:
    """ボーカルステムまたは音源全体の CPP (Cepstral Peak Prominence) を算出しますわ！"""
    frame_length = 2048
    hop_length = 512

    y = ctx.y
    sr = ctx.sr

    if len(y) < frame_length:
        return VoiceCppFeatures()

    n_frames = (len(y) - frame_length) // hop_length + 1
    cpp_values = []

    # 計算負荷低減のため最大 300 フレーム均等サンプリング
    step = max(1, n_frames // 300)
    for i in range(0, n_frames, step):
        start = i * hop_length
        end = start + frame_length
        frame = y[start:end]
        cpp_val = calc_frame_cpp(frame, sr)
        cpp_values.append(cpp_val)

    cpp_arr = np.array(cpp_values, dtype=np.float32)
    mean_cpp = float(np.mean(cpp_arr)) if len(cpp_arr) > 0 else 0.0
    std_cpp = float(np.std(cpp_arr)) if len(cpp_arr) > 0 else 0.0

    # 気息性スコア (Breathiness: CPP が低いほど気息的・ハスキー, 0.0〜1.0)
    # 一般に CPP > 10dB はクリーン・芯のある声、CPP < 4dB はハスキー・気息的
    breathiness = float(1.0 / (1.0 + math.exp(mean_cpp - 6.0)))

    cpp_seq = _resample_to_fixed_frames(cpp_arr, FIXED_SEQ_FRAMES)

    return VoiceCppFeatures(
        cpp_mean=mean_cpp,
        cpp_std=std_cpp,
        cpp_seq=cpp_seq,
        breathiness_score=breathiness,
    )


@register_plugin(
    name="voice_cpp",
    description="Cepstral Peak Prominence (CPP / CPPS) and Vocal Breathiness Score",
    enabled_by_default=False,
    priority=90,
)
class VoiceCppPlugin(BasePlugin):
    def extract(self, ctx: AudioContext) -> dict[str, Any]:
        feat = extract_voice_cpp(ctx)
        return {
            "voice_cpp_feat": feat,
        }
