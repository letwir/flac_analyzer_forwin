"""
analyzer/common_pitch_harmony.py
=================================
ボーカル・ベース・和声楽器向けの有声ゲート付き F0 輪郭・ビブラート・音域・和声統合の純粋射モジュールですわ！
Sep(IO_Effect, PureDomain): 帯域パラメータ (fmin, fmax) によるパラメータ化関手として
vocals (C2..C7) と bass (E1..E4) で同一の幾何・変調解析ロジックを再利用いたしますの。
"""

from __future__ import annotations

from typing import Any

import librosa
import numpy as np

from .common_signal import decay_slope_morphism, subband_energy_morphism
from .core import FIXED_SEQ_FRAMES, LIBROSA_LOCK, _resample_to_fixed_frames

_EPS = 1e-10


def _neutral_voiced_f0_metrics() -> dict[str, Any]:
    return {
        "voiced_ratio": 0.0,
        "f0_min_hz": 0.0,
        "f0_max_hz": 0.0,
        "f0_median_hz": 0.0,
        "pitch_range_semitones": 0.0,
        "melodic_interval_mean_semitones": 0.0,
        "pitch_quantization_score": 0.0,
        "vibrato_rate_hz": 0.0,
        "vibrato_extent_cents": 0.0,
        "f0_seq_32": [0.0] * FIXED_SEQ_FRAMES,
    }


def _estimate_vibrato_metrics(midi_voiced: np.ndarray, frame_rate_hz: float) -> tuple[float, float]:
    """有声 MIDI 音高列から 4-8Hz ビブラート周波数と片振幅(cent)を抽出しますわ！ Mor(R^T -> R x R)"""
    if midi_voiced.size < 16 or frame_rate_hz <= 16.0:
        return 0.0, 0.0
    # 緩やかな旋律移動による低域成分を抑えるため隣接差分の代わりに移動平均引き去りを行うわ
    win = max(5, int(round(frame_rate_hz * 0.35)))
    if win % 2 == 0:
        win += 1
    if midi_voiced.size <= win:
        detrended = midi_voiced - float(np.mean(midi_voiced))
    else:
        kernel = np.ones(win, dtype=np.float64) / float(win)
        smooth = np.convolve(midi_voiced, kernel, mode="same")
        detrended = midi_voiced - smooth

    spec = np.fft.rfft(detrended * np.hanning(detrended.size))
    freqs = np.fft.rfftfreq(detrended.size, d=1.0 / frame_rate_hz)
    band_mask = (freqs >= 4.0) & (freqs <= 8.0)
    if not np.any(band_mask):
        return 0.0, 0.0

    band_mag = np.abs(spec[band_mask])
    band_freqs = freqs[band_mask]
    peak_idx = int(np.argmax(band_mag))
    # Parseval 正規化により正弦波変調の片振幅 (semitones -> cents) を復元するわ
    win_gain = float(np.sum(np.hanning(detrended.size)) / 2.0)
    amp_semitones = float(band_mag[peak_idx] / max(win_gain, _EPS))
    extent_cents = float(amp_semitones * 100.0)
    if extent_cents < 5.0:
        return 0.0, 0.0
    return float(band_freqs[peak_idx]), extent_cents


def voiced_f0_contour_morphism_Large(
    y: np.ndarray,
    sr: int,
    fmin_hz: float,
    fmax_hz: float,
    frame_rms: np.ndarray | None = None,
) -> dict[str, Any]:
    """有声ゲート付き YIN ピッチ追跡により音域・跳躍度・12平均律安定度・ビブラートを抽出する純粋射ですわ！

    Mor(R^N x SR x (fmin, fmax) -> VoicedF0Metrics)
    """
    clean = np.nan_to_num(np.asarray(y, dtype=np.float32).ravel(), nan=0.0, posinf=0.0, neginf=0.0)
    frame_length = 2048
    hop_length = 512
    if clean.size < frame_length or sr <= 0 or fmin_hz <= 0.0 or fmax_hz <= fmin_hz:
        return _neutral_voiced_f0_metrics()

    with LIBROSA_LOCK:
        f0 = librosa.yin(
            clean,
            fmin=float(fmin_hz),
            fmax=float(fmax_hz),
            sr=sr,
            frame_length=frame_length,
            hop_length=hop_length,
        )
        rms = (
            np.asarray(frame_rms, dtype=np.float32).ravel()
            if frame_rms is not None
            else librosa.feature.rms(y=clean, frame_length=frame_length, hop_length=hop_length)[0]
        )

    n = min(f0.size, rms.size)
    if n == 0:
        return _neutral_voiced_f0_metrics()

    f0_n = f0[:n].astype(np.float64)
    rms_n = rms[:n].astype(np.float64)
    rms_peak = float(np.max(rms_n))
    if rms_peak <= _EPS:
        return _neutral_voiced_f0_metrics()

    # 無音区間および YIN 上下限張り付き (非周期ノイズ) を有声マスクで除外するわ
    voiced_mask = (rms_n >= rms_peak * 0.05) & (f0_n >= fmin_hz * 0.98) & (f0_n <= fmax_hz * 0.98)
    voiced_ratio = float(np.mean(voiced_mask))
    voiced_f0 = f0_n[voiced_mask]
    if voiced_f0.size == 0:
        return _neutral_voiced_f0_metrics()

    f0_min = float(np.percentile(voiced_f0, 5))
    f0_max = float(np.percentile(voiced_f0, 95))
    f0_med = float(np.median(voiced_f0))
    range_st = float(12.0 * np.log2(max(f0_max, _EPS) / max(f0_min, _EPS))) if f0_min > _EPS else 0.0

    midi_voiced = 69.0 + 12.0 * np.log2(np.maximum(voiced_f0, _EPS) / 440.0)
    interval_mean = float(np.mean(np.abs(np.diff(midi_voiced)))) if midi_voiced.size > 1 else 0.0
    quant_dev = float(np.mean(np.abs(midi_voiced - np.round(midi_voiced))))
    quant_score = float(np.clip(1.0 - 2.0 * quant_dev, 0.0, 1.0))

    vib_rate, vib_cents = _estimate_vibrato_metrics(midi_voiced, float(sr) / float(hop_length))
    gated_seq = np.where(voiced_mask, f0_n, 0.0)

    return {
        "voiced_ratio": voiced_ratio,
        "f0_min_hz": f0_min,
        "f0_max_hz": f0_max,
        "f0_median_hz": f0_med,
        "pitch_range_semitones": range_st,
        "melodic_interval_mean_semitones": interval_mean,
        "pitch_quantization_score": quant_score,
        "vibrato_rate_hz": vib_rate,
        "vibrato_extent_cents": vib_cents,
        "f0_seq_32": _resample_to_fixed_frames(gated_seq, FIXED_SEQ_FRAMES),
    }


def voiced_f0_contour_morphism(
    y: np.ndarray,
    sr: int,
    fmin_hz: float,
    fmax_hz: float,
    frame_rms: np.ndarray | None = None,
) -> dict[str, Any]:
    """公開シンボル名を保つ純粋委譲ラッパーですわ！ Mor(R^N x SR x (fmin, fmax) -> VoicedF0Metrics)"""
    return voiced_f0_contour_morphism_Large(y, sr, fmin_hz, fmax_hz, frame_rms=frame_rms)


def vocal_character_morphism(y: np.ndarray, sr: int) -> dict[str, Any]:
    """vocals ステム特化射：有声 F0 輪郭 (C2..C7)・シンガーズフォルマント・歯擦音比・フレーズ減衰を合成しますわ！

    Mor(R^N x SR -> VocalProfile)
    """
    clean = np.nan_to_num(np.asarray(y, dtype=np.float32).ravel(), nan=0.0, posinf=0.0, neginf=0.0)
    if clean.size < 2048 or sr <= 0:
        return {
            **_neutral_voiced_f0_metrics(),
            "singers_formant_ratio": 0.0,
            "sibilance_ratio": 0.0,
            "phrase_decay_db_per_sec": 0.0,
            "vocal_activity_ratio": 0.0,
        }

    with LIBROSA_LOCK:
        frame_rms = librosa.feature.rms(y=clean, frame_length=2048, hop_length=512)[0]
    f0_info = voiced_f0_contour_morphism(clean, sr, fmin_hz=65.41, fmax_hz=2093.0, frame_rms=frame_rms)
    bands = subband_energy_morphism(
        clean,
        sr,
        {"singers_formant": (2500.0, 3500.0), "sibilance": (5000.0, 10000.0)},
    )
    decay = decay_slope_morphism(frame_rms, sr=sr, hop_length=512)
    return {
        **f0_info,
        "singers_formant_ratio": float(bands["singers_formant_ratio"]),
        "sibilance_ratio": float(bands["sibilance_ratio"]),
        "phrase_decay_db_per_sec": float(decay["decay_db_per_sec"]),
        "vocal_activity_ratio": float(decay["sustain_ratio"]),
    }
