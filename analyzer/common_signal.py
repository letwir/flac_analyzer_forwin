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


def _sample_native_windows(clean: np.ndarray, win_len: int = 4096, max_windows: int = 128) -> np.ndarray:
    """長尺波形のメモリ爆発を防ぐため、原サンプリングレートのまま均等配置した連続窓 (M, win_len) を切り出しますわ！"""
    if clean.size <= win_len:
        return clean[np.newaxis, :]
    n_avail = clean.size // win_len
    if n_avail <= max_windows:
        return clean[: n_avail * win_len].reshape(n_avail, win_len)
    starts = np.linspace(0, clean.size - win_len, max_windows, dtype=np.int64)
    return np.stack([clean[s : s + win_len] for s in starts], axis=0)


def subband_energy_morphism(
    y: np.ndarray,
    sr: int,
    bands: Mapping[str, tuple[float, float]],
) -> dict[str, float]:
    """有界ウィンドウ平均 rFFT パワースペクトル (Welch型) により帯域エネルギー占有比を算出する純粋射ですわ！

    Mor(R^N x SR x Bands -> SubbandEnergies): 長尺トラックでも NumPy FFT の float64 昇格メモリを定数上限 (<4MB) に抑えますの。
    """
    clean = _sanitize_1d_array(y)
    if clean.size == 0 or sr <= 0 or not bands:
        return {f"{name}_ratio": 0.0 for name in bands} | {f"{name}_energy": 0.0 for name in bands}

    win_len = min(4096, clean.size)
    windows = _sample_native_windows(clean, win_len=win_len, max_windows=128)
    hann = np.hanning(win_len).astype(np.float64) if win_len >= 4 else np.ones(win_len, dtype=np.float64)
    # 窓単位の rFFT 平均電力 (M <= 128, win_len <= 4096) に限定し、全長 FFT による GB 級メモリ確保を根絶するわ
    spec = np.fft.rfft(windows.astype(np.float64) * hann[np.newaxis, :], axis=1)
    power = np.mean(np.abs(spec) ** 2, axis=0)
    freqs = np.fft.rfftfreq(win_len, d=1.0 / float(sr))
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
    """2つの包絡時系列から ±max_lag_sec の有界ラグのみ O(K*N) で相互相関算出する純粋射ですわ！

    Mor(R^T x R^T -> XCorrMetrics): np.correlate(mode='full') の O(N^2) 爆発を回避し、長尺でも <1ms で完了いたしますの。
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
    zero_corr = float(np.clip(float(np.dot(a, b)) / denom, -1.0, 1.0))

    best_lag_frames = 0
    best_corr = zero_corr
    for lag in range(-max_lag_frames, max_lag_frames + 1):
        if lag == 0:
            corr = zero_corr
        elif lag > 0:
            corr = float(np.dot(a[lag:], b[: n - lag])) / denom
        else:
            corr = float(np.dot(a[: n + lag], b[-lag:])) / denom
        if abs(corr) > abs(best_corr):
            best_corr = corr
            best_lag_frames = lag

    return {
        "best_lag_ms": float(best_lag_frames * hop_sec * 1000.0),
        "peak_corr": float(np.clip(best_corr, -1.0, 1.0)),
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


def _count_band_onset_peaks(env: np.ndarray, hop_sec: float) -> tuple[float, list[float]]:
    """帯域 RMS 包絡の立ち上がり差分ピークから打撃密度 (hits/sec) と時刻リスト(s)を抽出しますわ！"""
    if env.size < 4 or hop_sec <= 0.0:
        return 0.0, []
    diff = np.maximum(0.0, np.diff(env, prepend=env[0]))
    peak_val = float(np.max(diff))
    if peak_val <= _EPS:
        return 0.0, []
    thresh = max(peak_val * 0.2, float(np.mean(diff) + 1.5 * np.std(diff)))
    min_gap_frames = max(1, int(round(0.08 / hop_sec)))
    peaks: list[int] = []
    for idx in range(1, diff.size - 1):
        is_local = diff[idx] >= diff[idx - 1] and diff[idx] > diff[idx + 1] and diff[idx] >= thresh
        if not is_local:
            continue
        if peaks and (idx - peaks[-1]) < min_gap_frames:
            if diff[idx] > diff[peaks[-1]]:
                peaks[-1] = idx
            continue
        peaks.append(idx)
    dur_sec = float(env.size) * hop_sec
    times = [float(p) * hop_sec for p in peaks]
    return float(len(peaks) / max(dur_sec, _EPS)), times


def drum_band_onset_morphism_Large(y: np.ndarray, sr: int) -> dict[str, float]:
    """drums ステムを Kick(30-120Hz)/Snare(150-400Hz+1.5-4kHz)/HiHat(6-16kHz) へ帯域分解する純粋射ですわ！

    Mor(R^N x SR -> DrumProfile): 打撃密度・帯域バランス・マイクロタイミング揺らぎ・トランジェント鋭さを算出いたしますの。
    """
    clean = _sanitize_1d_array(y)
    if clean.size < 256 or sr <= 0:
        return {
            "kick_energy_ratio": 0.0,
            "snare_energy_ratio": 0.0,
            "hihat_energy_ratio": 0.0,
            "kick_hits_per_sec": 0.0,
            "snare_hits_per_sec": 0.0,
            "hihat_hits_per_sec": 0.0,
            "human_feel_std_ms": 0.0,
            "transient_crest": 0.0,
        }

    hop_sec = 0.01
    step = max(1, int(round(sr * hop_sec)))
    n_frames = max(1, clean.size // step)
    usable = n_frames * step
    freqs = np.fft.rfftfreq(step, d=1.0 / float(sr))

    kick_mask = (freqs >= 30.0) & (freqs <= 120.0)
    snare_mask = ((freqs >= 150.0) & (freqs <= 400.0)) | ((freqs >= 1500.0) & (freqs <= 4000.0))
    hihat_mask = (freqs >= 6000.0) & (freqs <= 16000.0)

    # 長尺トラックでの 2D rFFT メモリスパイクを防ぐため 4096 フレーム単位にチャンク分割して処理するわ
    kick_env = np.zeros(n_frames, dtype=np.float32)
    snare_env = np.zeros(n_frames, dtype=np.float32)
    hihat_env = np.zeros(n_frames, dtype=np.float32)
    blocks_all = clean[:usable].reshape(n_frames, step)
    chunk_frames = 4096
    for start_f in range(0, n_frames, chunk_frames):
        end_f = min(n_frames, start_f + chunk_frames)
        spec = np.abs(np.fft.rfft(blocks_all[start_f:end_f].astype(np.float64), axis=1)) ** 2
        if np.any(kick_mask):
            kick_env[start_f:end_f] = np.sqrt(np.mean(spec[:, kick_mask], axis=1)).astype(np.float32)
        if np.any(snare_mask):
            snare_env[start_f:end_f] = np.sqrt(np.mean(spec[:, snare_mask], axis=1)).astype(np.float32)
        if np.any(hihat_mask):
            hihat_env[start_f:end_f] = np.sqrt(np.mean(spec[:, hihat_mask], axis=1)).astype(np.float32)

    kick_rate, kick_times = _count_band_onset_peaks(kick_env, hop_sec)
    snare_rate, _ = _count_band_onset_peaks(snare_env, hop_sec)
    hihat_rate, _ = _count_band_onset_peaks(hihat_env, hop_sec)

    # マイクロタイミング揺らぎ: Kick 打撃間隔 (IOI) の中央値グリッドからの偏差標準偏差 (ms)
    human_feel_ms = 0.0
    if len(kick_times) >= 3:
        iois = np.diff(np.asarray(kick_times, dtype=np.float64))
        grid = float(np.median(iois))
        if grid > _EPS:
            human_feel_ms = float(np.std(iois - grid) * 1000.0)

    total_rms = float(np.sqrt(np.mean(clean.astype(np.float64) ** 2)))
    transient_crest = float(np.max(np.abs(clean)) / (total_rms + _EPS)) if total_rms > _EPS else 0.0
    ratios = subband_energy_morphism(
        clean,
        sr,
        {"kick": (30.0, 120.0), "snare_lo": (150.0, 400.0), "snare_hi": (1500.0, 4000.0), "hihat": (6000.0, 16000.0)},
    )
    return {
        "kick_energy_ratio": float(ratios["kick_ratio"]),
        "snare_energy_ratio": float(ratios["snare_lo_ratio"] + ratios["snare_hi_ratio"]),
        "hihat_energy_ratio": float(ratios["hihat_ratio"]),
        "kick_hits_per_sec": kick_rate,
        "snare_hits_per_sec": snare_rate,
        "hihat_hits_per_sec": hihat_rate,
        "human_feel_std_ms": human_feel_ms,
        "transient_crest": transient_crest,
    }


def drum_band_onset_morphism(y: np.ndarray, sr: int) -> dict[str, float]:
    """公開シンボル名を保つ純粋委譲ラッパーですわ！ Mor(R^N x SR -> DrumProfile)"""
    return drum_band_onset_morphism_Large(y, sr)
