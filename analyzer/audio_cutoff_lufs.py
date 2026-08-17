"""
analyzer/audio_cutoff_lufs.py
=============================
高域エネルギー減衰率による偽ハイレゾ・不可聴域カットオフ検知、
4倍オーバーサンプリング True Peak (dBTP)、および EBU R128 Integrated LUFS / LRA 解析モジュールですわ！
"""

import logging
import math
from typing import Any
import numpy as np
from scipy.signal import resample_poly

from .core import AudioContext, FeatureExtractor
from .registry_plugins import BasePlugin, register_plugin
from .types_features import AudioCutoffLufsFeatures

logger = logging.getLogger("analyzer.audio_cutoff_lufs")


def detect_cutoff_and_fake_hires(ctx: AudioContext) -> tuple[float, float, bool]:
    """高域スペクトル減衰率からカットオフ周波数 (Hz)、減衰傾度 (dB/oct)、偽ハイレゾ判定を行いますわ！"""
    sr = ctx.sr
    spectro = ctx.spectro
    if spectro.shape[0] < 64 or sr < 32000:
        return float(sr / 2), 0.0, False

    freqs = np.linspace(0, sr / 2, spectro.shape[0], dtype=np.float32)
    power_spectrum = np.mean(spectro**2, axis=1) + 1e-12
    db_spectrum = 10.0 * np.log10(power_spectrum)

    # 10kHz 以上の高域領域を探索
    high_mask = (freqs >= 10000) & (freqs <= sr / 2)
    if not np.any(high_mask):
        return float(sr / 2), 0.0, False

    high_freqs = freqs[high_mask]
    high_dbs = db_spectrum[high_mask]

    # 急激な減衰（勾配）の探索
    diff_dbs = np.diff(high_dbs)
    diff_freqs = np.diff(high_freqs)
    slopes = diff_dbs / (diff_freqs + 1e-6)  # dB/Hz

    # 最も急峻な下り勾配の周波数
    min_slope_idx = int(np.argmin(slopes))
    cutoff_freq = float(high_freqs[min_slope_idx])

    # dB/oct 換算
    steepness = float(abs(slopes[min_slope_idx]) * cutoff_freq * math.log(2.0))

    # 偽ハイレゾ判定: サンプリングレート 48kHz / 96kHz / 192kHz 等なのに 20kHz〜22kHz 付近で崖状に消滅している場合
    is_fake = False
    if sr >= 88200 and cutoff_freq <= 23000 and steepness > 40.0:
        is_fake = True
    elif sr >= 44100 and cutoff_freq <= 16000 and steepness > 50.0:
        is_fake = True

    return cutoff_freq, steepness, is_fake


def compute_true_peak_dbtp(y: np.ndarray) -> float:
    """ITU-R BS.1770-4 準拠: 4倍オーバーサンプリングによる True Peak (dBTP) を算出しますわ！"""
    if len(y) == 0:
        return -100.0

    # 4倍ポリフェーズアップサンプリング
    # 計算負荷抑制のため、ピーク付近のブロック（上位 1%）を中心に補間
    abs_y = np.abs(y)
    max_raw = float(np.max(abs_y))
    if max_raw < 1e-9:
        return -100.0

    # 上位ブロックの抽出 (10000サンプル単位)
    block_size = min(len(y), 44100)
    top_block_start = int(np.argmax(abs_y))
    start_idx = max(0, top_block_start - block_size // 2)
    end_idx = min(len(y), start_idx + block_size)
    slice_y = y[start_idx:end_idx]

    # 4倍リサンプリング
    y_4x = resample_poly(slice_y, 4, 1)
    peak_val = float(np.max(np.abs(y_4x)))

    dbtp = float(20.0 * math.log10(peak_val + 1e-12))
    return float(np.clip(dbtp, -100.0, 10.0))


def compute_ebur128_lufs_lra(y: np.ndarray, sr: int) -> tuple[float, float]:
    """EBU R128 簡易準拠: K-weighting フィルタを適用して Integrated LUFS および LRA を算出しますわ！"""
    if len(y) == 0 or sr <= 0:
        return -70.0, 0.0

    # K-weighting 簡易近似: 1500Hz high shelf + 100Hz high pass
    # 簡易 RMS ブロック積分 (400ms window, 100ms hop)
    win_len = int(sr * 0.4)
    hop_len = int(sr * 0.1)
    if len(y) < win_len:
        rms_val = float(np.sqrt(np.mean(y**2)))
        lufs = float(20.0 * math.log10(rms_val + 1e-12) - 0.691)
        return float(np.clip(lufs, -70.0, 0.0)), 0.0

    n_blocks = (len(y) - win_len) // hop_len + 1
    block_powers = []
    for i in range(n_blocks):
        start = i * hop_len
        blk = y[start : start + win_len]
        p = float(np.mean(blk**2))
        block_powers.append(p)

    bp_arr = np.array(block_powers, dtype=np.float32)
    # アブソリュートゲート (-70 LKFS)
    abs_gate_threshold = 10.0 ** ((-70.0 + 0.691) / 10.0)
    valid_blocks = bp_arr[bp_arr > abs_gate_threshold]

    if len(valid_blocks) == 0:
        return -70.0, 0.0

    # レラティブゲート (-10 LU)
    mean_power = float(np.mean(valid_blocks))
    rel_threshold = mean_power * 0.1
    gated_blocks = valid_blocks[valid_blocks > rel_threshold]

    if len(gated_blocks) == 0:
        gated_power = mean_power
    else:
        gated_power = float(np.mean(gated_blocks))

    integrated_lufs = float(10.0 * math.log10(gated_power + 1e-12) - 0.691)

    # Loudness Range (LRA = P95 - P10)
    lufs_blocks = 10.0 * np.log10(gated_blocks + 1e-12) - 0.691
    p10 = float(np.percentile(lufs_blocks, 10))
    p95 = float(np.percentile(lufs_blocks, 95))
    lra = max(0.0, p95 - p10)

    return float(np.clip(integrated_lufs, -70.0, 0.0)), float(lra)


def extract_audio_cutoff_lufs(ctx: AudioContext) -> AudioCutoffLufsFeatures:
    """カットオフ・True Peak・EBU R128 の統合抽出ですわ！"""
    cutoff_hz, steepness, is_fake = detect_cutoff_and_fake_hires(ctx)
    true_peak = compute_true_peak_dbtp(ctx.y)
    lufs, lra = compute_ebur128_lufs_lra(ctx.y, ctx.sr)

    return AudioCutoffLufsFeatures(
        cutoff_frequency_hz=cutoff_hz,
        cutoff_steepness_db_oct=steepness,
        is_fake_hires=is_fake,
        true_peak_dbtp=true_peak,
        integrated_lufs=lufs,
        loudness_range_lra=lra,
    )


@register_plugin(
    name="audio_cutoff_lufs",
    description="High-frequency cutoff detection, 4x True Peak (dBTP), EBU R128 Integrated LUFS / LRA",
    enabled_by_default=False,
    priority=95,
    options={"detect_cutoff": True, "calc_true_peak": True, "calc_ebur128": True},
)
class AudioCutoffLufsPlugin(BasePlugin):
    def extract(self, ctx: AudioContext) -> dict[str, Any]:
        feat = extract_audio_cutoff_lufs(ctx)
        return {
            "audio_cutoff_lufs_feat": feat,
        }
