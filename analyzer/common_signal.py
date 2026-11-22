"""
analyzer/common_signal.py
=========================
純粋音響信号・統計・帯域分割の共通射モジュールですわ！
Sep(IO_Effect, PureDomain): ファイルシステム・SHM・DB・グローバル状態に一切依存せず、
数値配列から特徴量辞書への純粋射 Mor(Domain -> Codomain) のみを提供しますの。
"""

from __future__ import annotations

import math
from typing import Mapping

import numpy as np

_EPS = 1e-10


def _sanitize_1d_array(arr: np.ndarray | list[float]) -> np.ndarray:
    """任意の実数配列を有限値のみの 1D float32 配列へ正規化しますわ！ Mor(Array -> R^N)"""
    raw = np.asarray(arr, dtype=np.float32).ravel()
    if raw.size == 0:
        return np.empty(0, dtype=np.float32)
    return np.nan_to_num(raw, nan=0.0, posinf=0.0, neginf=0.0)


def _compute_shannon_entropy(values: np.ndarray) -> float:
    """正規化非負ベクトルから Shannon エントロピーを算出しますわ！ Mor(R^N -> R)"""
    if values.size == 0:
        return 0.0
    abs_vals = np.abs(values).astype(np.float64)
    total = float(np.sum(abs_vals))
    if total <= _EPS:
        return 0.0
    probs = abs_vals / total
    return float(-np.sum(probs * np.log2(probs + _EPS)))


def envelope_stats7_morphism(arr: np.ndarray | list[float]) -> dict[str, float]:
    """時系列配列から 7 統計量 (mean, std, peak, max, min, median, entropy) を抽出する純粋射ですわ！

    Mor(R^N -> Stats7): 空配列や全ゼロ時でも破綻せず型安全な単位元を返却しますの。
    """
    clean = _sanitize_1d_array(arr)
    if clean.size == 0:
        return {
            "mean": 0.0,
            "std": 0.0,
            "peak": 0.0,
            "max": 0.0,
            "min": 0.0,
            "median": 0.0,
            "entropy": 0.0,
        }
    peak_val = float(np.max(clean))
    return {
        "mean": float(np.mean(clean)),
        "std": float(np.std(clean)),
        "peak": peak_val,
        "max": peak_val,
        "min": float(np.min(clean)),
        "median": float(np.median(clean)),
        "entropy": _compute_shannon_entropy(clean),
    }


def subband_energy_morphism(
    y: np.ndarray,
    sr: int,
    bands: Mapping[str, tuple[float, float]],
) -> dict[str, float]:
    """rFFT パワースペクトルを用いて指定周波数帯域ごとのエネルギーおよび占有比を算出する純粋射ですわ！

    Mor(R^N x SR x Bands -> SubbandEnergies): 複数の楽器ステム解析で共通利用されますの。
    """
    clean = _sanitize_1d_array(y)
    if clean.size == 0 or sr <= 0 or not bands:
        return {f"{name}_ratio": 0.0 for name in bands} | {f"{name}_energy": 0.0 for name in bands}

    # 長尺信号でも過剰な FFT メモリ確保を避けるため float32 rFFT を用いるわ
    spectrum = np.fft.rfft(clean)
    power = np.abs(spectrum) ** 2
    freqs = np.fft.rfftfreq(clean.size, d=1.0 / float(sr))
    total_energy = float(np.sum(power))
    out: dict[str, float] = {}
    for name, (f_lo, f_hi) in bands.items():
        mask = (freqs >= float(f_lo)) & (freqs < float(f_hi))
        band_e = float(np.sum(power[mask])) if np.any(mask) else 0.0
        out[f"{name}_energy"] = band_e
        out[f"{name}_ratio"] = float(band_e / (total_energy + _EPS)) if total_energy > _EPS else 0.0
    return out


def xcorr_lag_morphism_Complex(
    env_a: np.ndarray,
    env_b: np.ndarray,
    hop_sec: float,
    max_lag_sec: float = 0.25,
) -> dict[str, float]:
    """2つの包絡時系列の正規化相互相関から最適ラグ(ms)・ピーク相関・ゼロラグ相関を算出する純粋射ですわ！

    Mor(R^T x R^T -> XCorrMetrics): ビブラート周期や Kick-Bass ポケット同期・サイドチェイン解析の基盤ですの。
    """
    a = _sanitize_1d_array(env_a).astype(np.float64)
    b = _sanitize_1d_array(env_b).astype(np.float64)
    n = min(a.size, b.size)
    if n < 4 or hop_sec <= 0.0 or max_lag_sec <= 0.0:
        return {"best_lag_ms": 0.0, "peak_corr": 0.0, "zero_lag_corr": 0.0}

    a = a[:n] - float(np.mean(a[:n]))
    b = b[:n] - float(np.mean(b[:n]))
    denom = float(np.linalg.norm(a) * np.linalg.norm(b))
    if denom <= _EPS:
        return {"best_lag_ms": 0.0, "peak_corr": 0.0, "zero_lag_corr": 0.0}

    max_lag_frames = max(1, min(n - 1, int(round(max_lag_sec / hop_sec))))
    full_corr = np.correlate(a, b, mode="full") / denom
    mid = n - 1
    window = full_corr[mid - max_lag_frames : mid + max_lag_frames + 1]
    zero_corr = float(np.clip(full_corr[mid], -1.0, 1.0))

    # 絶対相関が最大のラグを選択し、負相関(サイドチェイン等)も情報として保持いたしますわ
    peak_idx = int(np.argmax(np.abs(window)))
    best_lag_frames = peak_idx - max_lag_frames
    return {
        "best_lag_ms": float(best_lag_frames * hop_sec * 1000.0),
        "peak_corr": float(np.clip(window[peak_idx], -1.0, 1.0)),
        "zero_lag_corr": zero_corr,
    }


def xcorr_lag_morphism(
    env_a: np.ndarray,
    env_b: np.ndarray,
    hop_sec: float,
    max_lag_sec: float = 0.25,
) -> dict[str, float]:
    """公開シンボル名を保つ純粋委譲ラッパーですわ！ Mor(R^T x R^T -> XCorrMetrics)"""
    return xcorr_lag_morphism_Complex(env_a, env_b, hop_sec, max_lag_sec)


def decay_slope_morphism(
    env: np.ndarray,
    sr: int,
    hop_length: int = 512,
) -> dict[str, float]:
    """包絡線の減衰傾き (dB/sec) と持続率 (sustain_ratio) を算出する純粋射ですわ！

    Mor(R^T -> DecayMetrics): ボーカル残響尾・ピアノペダル・ベースレガート判定に共通利用しますの。
    """
    clean = np.abs(_sanitize_1d_array(env))
    if clean.size < 4 or sr <= 0 or hop_length <= 0:
        return {"decay_db_per_sec": 0.0, "sustain_ratio": 0.0}

    peak_val = float(np.max(clean))
    if peak_val <= _EPS:
        return {"decay_db_per_sec": 0.0, "sustain_ratio": 0.0}

    # ピークの -20dB (0.1x) 以上を維持している時間割合を sustain_ratio とするわ
    sustain_ratio = float(np.mean(clean >= peak_val * 0.1))

    # 局所ピーク直後フレームにおける dB 減衰率の中央値でロバストに減衰傾きを測りますの
    db_env = 20.0 * np.log10(np.maximum(clean / peak_val, 1e-4))
    dt_sec = float(hop_length) / float(sr)
    diffs = np.diff(db_env) / max(dt_sec, _EPS)
    neg_diffs = diffs[diffs < -0.5]
    if neg_diffs.size == 0:
        return {"decay_db_per_sec": 0.0, "sustain_ratio": sustain_ratio}
    return {
        "decay_db_per_sec": float(np.median(neg_diffs)),
        "sustain_ratio": sustain_ratio,
    }
