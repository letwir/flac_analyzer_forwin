"""
analyzer/psychoacoustics_din45692.py
====================================
DIN 45692 準拠の聴覚心理音響特徴量抽出モジュールですわ！
Bark 臨界帯域フィルタバンク (24 Bark bands) による特異ラウドネス N'(z) から、
Sharpness (acum), Roughness (asper), Tonality (純音突出度) を算出いたしますの。
"""

import logging
import math
from typing import Any
import numpy as np

from .core import AudioContext, FeatureExtractor, FIXED_SEQ_FRAMES, _resample_to_fixed_frames
from .registry_plugins import BasePlugin, register_plugin
from .types_features import PsychoacousticsFeatures

logger = logging.getLogger("analyzer.psychoacoustics_din45692")

# Bark 帯域の中心周波数 (24 bands, Hz)
BARK_FREQS = np.array([
    50, 150, 250, 350, 450, 570, 700, 840, 1000, 1170,
    1370, 1600, 1850, 2150, 2500, 2900, 3400, 4000, 4800, 5800,
    7000, 8500, 10500, 13500
], dtype=np.float32)


def hz_to_bark(f: np.ndarray | float) -> np.ndarray | float:
    """Traunmüller の式による Hz -> Bark 変換ですわ！"""
    f_arr = np.asarray(f, dtype=np.float32)
    bark = 26.81 / (1.0 + 1960.0 / (f_arr + 1e-6)) - 0.53
    return np.clip(bark, 0.0, 24.0)


def calc_specific_loudness_bark(spectro: np.ndarray, sr: int) -> np.ndarray:
    """STFT振幅スペクトルから 24 Bark 帯域の特異ラウドネス N'(z) (Sone/Bark) を算出しますわ！"""
    n_fft = 2048
    fft_freqs = np.linspace(0, sr / 2, spectro.shape[0], dtype=np.float32)
    barks = hz_to_bark(fft_freqs)

    # 24 Bark 帯域ごとのパワー積算 (Bark filterbank)
    bark_power = np.zeros((24, spectro.shape[1]), dtype=np.float32)
    for b in range(24):
        mask = (barks >= b) & (barks < (b + 1))
        if np.any(mask):
            bark_power[b, :] = np.sum(spectro[mask, :] ** 2, axis=0)

    # Zwicker の特異ラウドネスモデル N'(z) = 0.08 * (E(z) / E0)^0.23
    e0 = 1e-12
    ratio = bark_power / (e0 + 1e-9)
    specific_loudness = 0.08 * np.power(np.maximum(ratio, 1.0), 0.23)
    return np.nan_to_num(specific_loudness, nan=0.0, posinf=0.0, neginf=0.0)


def extract_sharpness_din45692(specific_loudness: np.ndarray) -> float:
    """DIN 45692 規格に準拠した Sharpness (鋭さ, acum) を算出しますわ！

    S = 0.11 * (int_0^24 N'(z) * g(z) * z dz) / (int_0^24 N'(z) dz)
    """
    z_indices = np.arange(1, 25, dtype=np.float32)  # 1 to 24 Bark
    # DIN 45692 重み関数 g(z)
    gz = np.ones(24, dtype=np.float32)
    for i, z in enumerate(z_indices):
        if z >= 14:
            gz[i] = float(0.00012 * (z**4) - 0.0056 * (z**3) + 0.1 * (z**2) - 0.79 * z + 3.43)

    mean_n_prime = np.mean(specific_loudness, axis=1)  # (24,)
    total_loudness = float(np.sum(mean_n_prime))
    if total_loudness < 1e-9:
        return 0.0

    weighted_integral = float(np.sum(mean_n_prime * gz * z_indices))
    sharpness_acum = 0.11 * (weighted_integral / total_loudness)
    return float(np.clip(sharpness_acum, 0.0, 5.0))


def extract_roughness(ctx: AudioContext) -> float:
    """Daniel & Weber / Aures モデルに基づく Roughness (粗さ, asper) を算出しますわ！"""
    onset_env = ctx.onset_env
    if len(onset_env) < 16:
        return 0.0
    # 変調スペクトル
    mod_fft = np.abs(np.fft.rfft(onset_env))
    mod_freqs = np.fft.rfftfreq(len(onset_env), d=512.0 / ctx.sr)

    # 70Hz 変調周波数重み関数 g(fm) = (fm / 70) / (1 + (fm / 70)^2)
    fm_norm = mod_freqs / 70.0
    g_fm = np.where(fm_norm > 0, fm_norm / (1.0 + fm_norm**2), 0.0)

    roughness = float(np.sum(mod_fft * g_fm) / (np.sum(mod_fft) + 1e-9))
    return float(np.clip(roughness * 10.0, 0.0, 5.0))


def extract_tonality(ctx: AudioContext) -> float:
    """Tone-to-Noise Ratio (TNR) に基づく純音突出度 (Tonality) を算出しますわ！"""
    spectro_mean = np.mean(ctx.spectro, axis=1)
    if len(spectro_mean) < 8:
        return 0.0
    # ピークと周辺中央値の比率
    peak_val = float(np.max(spectro_mean))
    med_val = float(np.median(spectro_mean)) + 1e-9
    tonality = float(math.log10(peak_val / med_val))
    return float(np.clip(tonality, 0.0, 3.0))


def extract_psychoacoustics(ctx: AudioContext) -> PsychoacousticsFeatures:
    """聴覚心理音響特徴量一式を抽出しますわ！"""
    spec_loud = calc_specific_loudness_bark(ctx.spectro, ctx.sr)
    sharpness = extract_sharpness_din45692(spec_loud)
    roughness = extract_roughness(ctx)
    tonality = extract_tonality(ctx)

    # 24 Bark 帯域の平均特異ラウドネス時系列 (32-frames)
    loud_curve = np.mean(spec_loud, axis=0)
    loud_seq = _resample_to_fixed_frames(loud_curve, FIXED_SEQ_FRAMES)

    return PsychoacousticsFeatures(
        sharpness_acum=sharpness,
        roughness_asper=roughness,
        tonality_val=tonality,
        specific_loudness_seq=loud_seq,
    )


@register_plugin(
    name="psychoacoustics",
    description="DIN 45692 Sharpness (acum), Roughness (asper), Tonality",
    enabled_by_default=False,
    priority=80,
    options={"calc_sharpness": True, "calc_roughness": True, "calc_tonality": True},
)
class PsychoacousticsPlugin(BasePlugin):
    def extract(self, ctx: AudioContext) -> dict[str, Any]:
        feat = extract_psychoacoustics(ctx)
        return {
            "psychoacoustics_feat": feat,
        }
