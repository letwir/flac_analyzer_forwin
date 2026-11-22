"""
analyzer/common_stereo.py
=========================
Demucs 出力などのステレオ波形 (2, N) から空間定位・ステレオ幅・位相相関を抽出する純粋射モジュールですわ！
Sep(IO_Effect, PureDomain): モノラル化直前のオンメモリ (2, N) 配列に対して純粋変換を行い、
SHM 容量を増やさず空間情報を抽出いたしますの。
"""

from __future__ import annotations

from typing import Any

import numpy as np

from .core import FIXED_SEQ_FRAMES, _resample_to_fixed_frames

_EPS = 1e-10


def _normalize_stereo_array(y_stereo: np.ndarray) -> np.ndarray:
    """入力波形を (2, N) float32 へ安全に整形しますわ！ Mor(Array -> R^{2xN})"""
    arr = np.asarray(y_stereo, dtype=np.float32)
    if arr.size == 0:
        return np.empty((2, 0), dtype=np.float32)
    arr = np.nan_to_num(arr, nan=0.0, posinf=0.0, neginf=0.0)
    if arr.ndim == 1:
        return np.stack([arr, arr], axis=0)
    if arr.shape[0] == 2:
        return arr
    if arr.shape[1] == 2:
        return arr.T
    if arr.shape[0] == 1:
        return np.repeat(arr, 2, axis=0)
    return arr[:2, :]


def mono_downmix_morphism(y_stereo: np.ndarray) -> np.ndarray:
    """ステレオ波形を連続メモリ配置の 1D float32 モノラル波形へ射影しますわ！ Mor(R^{2xN} -> R^N)"""
    arr = np.asarray(y_stereo, dtype=np.float32)
    if arr.size == 0:
        return np.empty(0, dtype=np.float32)
    if arr.ndim == 1:
        return np.ascontiguousarray(arr, dtype=np.float32)
    if arr.shape[0] <= 2:
        return np.ascontiguousarray(np.mean(arr, axis=0), dtype=np.float32)
    return np.ascontiguousarray(np.mean(arr, axis=1), dtype=np.float32)


def _compute_lr_correlation(left: np.ndarray, right: np.ndarray) -> float:
    """L/R チャンネル間のピアソン相関係数 ρ_LR ∈ [-1, 1] を算出しますわ！ Mor(R^N x R^N -> R)"""
    if left.size == 0:
        return 1.0
    l_centered = left.astype(np.float64) - float(np.mean(left))
    r_centered = right.astype(np.float64) - float(np.mean(right))
    denom = float(np.linalg.norm(l_centered) * np.linalg.norm(r_centered))
    if denom <= _EPS:
        return 1.0
    corr = float(np.dot(l_centered, r_centered) / denom)
    return float(np.clip(corr, -1.0, 1.0))


def _compute_low_band_coherence(left: np.ndarray, right: np.ndarray, sr: int, cutoff_hz: float = 120.0) -> float:
    """Parseval の定理に基づき 120Hz 以下低域の L/R 位相整合度を rFFT で高速算出しますわ！ Mor(R^N x R^N -> R)"""
    if left.size < 64 or sr <= 0:
        return 1.0
    # 全長 FFT によるメモリ肥大を避けるため最大 16384 サンプル単位に制限するか先頭窓で計算するわ
    step = max(1, left.size // 131072)
    l_sub = left[::step].astype(np.float64)
    r_sub = right[::step].astype(np.float64)
    eff_sr = float(sr) / float(step)
    freqs = np.fft.rfftfreq(l_sub.size, d=1.0 / eff_sr)
    mask = (freqs > 0.0) & (freqs <= float(cutoff_hz))
    if not np.any(mask):
        return 1.0
    l_fft = np.fft.rfft(l_sub)[mask]
    r_fft = np.fft.rfft(r_sub)[mask]
    denom = float(np.linalg.norm(l_fft) * np.linalg.norm(r_fft))
    if denom <= _EPS:
        return 1.0
    coherence = float(np.real(np.vdot(l_fft, r_fft)) / denom)
    return float(np.clip(coherence, -1.0, 1.0))


def _neutral_stereo_metrics() -> dict[str, Any]:
    return {
        "pan_mean": 0.0,
        "pan_std": 0.0,
        "stereo_width": 0.0,
        "lr_correlation": 1.0,
        "low_band_phase_coherence": 1.0,
        "pan_seq_32": [0.0] * FIXED_SEQ_FRAMES,
        "width_seq_32": [0.0] * FIXED_SEQ_FRAMES,
    }


def stereo_field_morphism_Complex(y_stereo: np.ndarray, sr: int) -> dict[str, Any]:
    """ステレオ波形 (2, N) から Pan・Stereo Width・L/R 相関・低域位相整合・32フレーム推移を抽出する純粋射ですわ！

    Mor(R^{2xN} x SR -> StereoMetrics)
    """
    st = _normalize_stereo_array(y_stereo)
    if st.shape[1] == 0:
        return _neutral_stereo_metrics()

    left = st[0].astype(np.float64)
    right = st[1].astype(np.float64)
    mid = 0.5 * (left + right)
    side = 0.5 * (left - right)

    rms_m = float(np.sqrt(np.mean(mid * mid)))
    rms_s = float(np.sqrt(np.mean(side * side)))
    if (rms_m + rms_s) <= _EPS:
        return _neutral_stereo_metrics()

    stereo_width = float(np.clip(rms_s / (rms_m + rms_s + _EPS), 0.0, 1.0))
    lr_corr = _compute_lr_correlation(left, right)
    low_coh = _compute_low_band_coherence(left, right, sr)

    # フレーム単位の Pan (-1=Left, +1=Right) と Width (0=Mono, 1=FullSide) を算出するわ
    n_samples = left.size
    frame_len = max(256, n_samples // FIXED_SEQ_FRAMES)
    usable = (n_samples // frame_len) * frame_len
    if usable < frame_len:
        e_l = float(np.mean(left * left))
        e_r = float(np.mean(right * right))
        pan_val = float(np.clip((e_r - e_l) / (e_r + e_l + _EPS), -1.0, 1.0))
        return {
            "pan_mean": pan_val,
            "pan_std": 0.0,
            "stereo_width": stereo_width,
            "lr_correlation": lr_corr,
            "low_band_phase_coherence": low_coh,
            "pan_seq_32": [pan_val] * FIXED_SEQ_FRAMES,
            "width_seq_32": [stereo_width] * FIXED_SEQ_FRAMES,
        }

    l_frames = left[:usable].reshape(-1, frame_len)
    r_frames = right[:usable].reshape(-1, frame_len)
    e_l_f = np.mean(l_frames * l_frames, axis=1)
    e_r_f = np.mean(r_frames * r_frames, axis=1)
    active_f = (e_l_f + e_r_f) > _EPS
    pan_frames = np.where(active_f, (e_r_f - e_l_f) / (e_r_f + e_l_f + _EPS), 0.0)

    m_frames = 0.5 * (l_frames + r_frames)
    s_frames = 0.5 * (l_frames - r_frames)
    rms_m_f = np.sqrt(np.mean(m_frames * m_frames, axis=1))
    rms_s_f = np.sqrt(np.mean(s_frames * s_frames, axis=1))
    width_frames = np.where((rms_m_f + rms_s_f) > _EPS, rms_s_f / (rms_m_f + rms_s_f + _EPS), 0.0)

    active_pans = pan_frames[active_f] if np.any(active_f) else pan_frames
    return {
        "pan_mean": float(np.clip(np.mean(active_pans), -1.0, 1.0)),
        "pan_std": float(np.std(active_pans)),
        "stereo_width": stereo_width,
        "lr_correlation": lr_corr,
        "low_band_phase_coherence": low_coh,
        "pan_seq_32": _resample_to_fixed_frames(np.clip(pan_frames, -1.0, 1.0), FIXED_SEQ_FRAMES),
        "width_seq_32": _resample_to_fixed_frames(np.clip(width_frames, 0.0, 1.0), FIXED_SEQ_FRAMES),
    }


def stereo_field_morphism(y_stereo: np.ndarray, sr: int) -> dict[str, Any]:
    """公開シンボル名を保つ純粋委譲ラッパーですわ！ Mor(R^{2xN} x SR -> StereoMetrics)"""
    return stereo_field_morphism_Complex(y_stereo, sr)
