"""
analyzer/common_cross_stem.py
=============================
mix と複数ステム間の相対バランス・マスキング・分離残差 SNR・編曲アーク・リズムポケットを
純粋に算出するクロスステム共通射モジュールですわ！
Sep(IO_Effect, PureDomain): SHM やファイル IO を持たず、純粋波形群の直積からの射のみを提供しますの。
"""

from __future__ import annotations

from typing import Any, Mapping

import numpy as np

from .common_signal import subband_energy_morphism, xcorr_lag_morphism
from .core import FIXED_SEQ_FRAMES, _resample_to_fixed_frames

_EPS = 1e-10
_STEM_NAMES = ("vocals", "drums", "bass", "guitar", "piano", "other")


def _align_stems_length_Batch(
    mix_mono: np.ndarray,
    stems_mono: Mapping[str, np.ndarray],
) -> tuple[np.ndarray, dict[str, np.ndarray]]:
    """mix と全ステム波形を同一長・有限 float32 へ安全に揃えますわ！ Mor(Prod -> AlignedProd)"""
    mix_arr = np.nan_to_num(np.asarray(mix_mono, dtype=np.float32).ravel(), nan=0.0, posinf=0.0, neginf=0.0)
    lengths = [mix_arr.size] + [np.asarray(v).size for v in stems_mono.values()]
    target_len = min([ln for ln in lengths if ln > 0], default=0)
    if target_len == 0:
        return np.empty(0, dtype=np.float32), {}

    aligned: dict[str, np.ndarray] = {}
    for name, wav in stems_mono.items():
        if name == "mix":
            continue
        arr = np.nan_to_num(np.asarray(wav, dtype=np.float32).ravel(), nan=0.0, posinf=0.0, neginf=0.0)
        aligned[name] = arr[:target_len]
    return mix_arr[:target_len], aligned


def _frame_rms_curve(y: np.ndarray, n_frames: int = FIXED_SEQ_FRAMES) -> np.ndarray:
    """波形全体 [0, N) を1サンプルも捨てず n_frames 区間へ均等分割し RMS 包絡曲線を算出しますわ！ Mor(R^N -> R^T)"""
    if y.size == 0:
        return np.zeros(n_frames, dtype=np.float32)
    if y.size < n_frames:
        rms_vals = np.sqrt(y.astype(np.float64) ** 2)
        return np.asarray(_resample_to_fixed_frames(rms_vals, n_frames), dtype=np.float32)
    edges = np.linspace(0, y.size, n_frames + 1, dtype=np.int64)
    curve = np.zeros(n_frames, dtype=np.float32)
    for i in range(n_frames):
        seg = y[int(edges[i]) : int(edges[i + 1])]
        acc = 0.0
        for off in range(0, seg.size, 1048576):
            blk = seg[off : min(seg.size, off + 1048576)].astype(np.float64)
            acc += float(np.dot(blk, blk))
        curve[i] = float(np.sqrt(acc / float(seg.size)))
    return curve


def _bandpass_rms_envelope(y: np.ndarray, sr: int, f_lo: float, f_hi: float, hop_sec: float = 0.02) -> np.ndarray:
    """Parseval の定理に基づくチャンク分割短時間 rFFT 帯域積分で RMS 包絡線を抽出しますわ！

    Mor(R^N x SR -> R^T): 全長 FFT+iFFT による GB 級メモリスパイクを排除いたしますの。
    """
    if y.size < 64 or sr <= 0:
        return np.empty(0, dtype=np.float32)
    step = max(1, int(round(sr * hop_sec)))
    n_frames = max(1, y.size // step)
    usable = n_frames * step
    freqs = np.fft.rfftfreq(step, d=1.0 / float(sr))
    mask = (freqs >= f_lo) & (freqs <= f_hi)
    if not np.any(mask):
        return np.zeros(n_frames, dtype=np.float32)

    # Parseval の定理: 帯域内パワー合計 * (2 / N^2) が時間領域の平均二乗エネルギーに近似対応するわ
    blocks_all = y[:usable].reshape(n_frames, step)
    out = np.zeros(n_frames, dtype=np.float32)
    scale = 2.0 / float(step * step)
    chunk_frames = 4096
    for start_f in range(0, n_frames, chunk_frames):
        end_f = min(n_frames, start_f + chunk_frames)
        spec = np.fft.rfft(blocks_all[start_f:end_f].astype(np.float64), axis=1)
        band_power = np.sum(np.abs(spec[:, mask]) ** 2, axis=1) * scale
        out[start_f:end_f] = np.sqrt(band_power).astype(np.float32)
    return out


def rhythm_pocket_and_ducking_morphism(
    drums_y: np.ndarray,
    bass_y: np.ndarray,
    sr: int,
) -> dict[str, float]:
    """Drums 低域(Kick 30-120Hz) と Bass 包絡の相互相関からポケット同期度・ラグ・ダッキング強度を算出しますわ！

    Mor(R^N x R^N x SR -> PocketMetrics)
    """
    n = min(np.asarray(drums_y).size, np.asarray(bass_y).size)
    if n < 256 or sr <= 0:
        return {"kick_bass_lag_ms": 0.0, "pocket_sync_score": 0.0, "sidechain_ducking_score": 0.0}

    hop_sec = 0.02
    kick_env = _bandpass_rms_envelope(np.asarray(drums_y[:n], dtype=np.float32), sr, 30.0, 120.0, hop_sec=hop_sec)
    bass_env = _bandpass_rms_envelope(np.asarray(bass_y[:n], dtype=np.float32), sr, 20.0, 250.0, hop_sec=hop_sec)
    xcorr = xcorr_lag_morphism(kick_env, bass_env, hop_sec=hop_sec, max_lag_sec=0.25)

    # 正相関が高いほど Kick と Bass が同時アタックでタイトに同期、負相関が強いほどサイドチェイン圧縮が強いわ
    peak_c = float(xcorr["peak_corr"])
    zero_c = float(xcorr["zero_lag_corr"])
    return {
        "kick_bass_lag_ms": float(xcorr["best_lag_ms"]),
        "pocket_sync_score": float(np.clip(max(0.0, peak_c), 0.0, 1.0)),
        "sidechain_ducking_score": float(np.clip(max(0.0, -zero_c, -peak_c), 0.0, 1.0)),
    }


def _compute_vocal_masking_metrics(
    vocals_y: np.ndarray | None,
    accomp_parts: list[np.ndarray],
    sr: int,
    n_samples: int,
) -> tuple[float, float]:
    """チャンク積和により全長伴奏バッファを確保せずボーカル対伴奏比(dB)とマスキング度を算出しますわ！"""
    if vocals_y is None or vocals_y.size == 0 or n_samples == 0:
        return -60.0, 0.0
    chunk = 1048576
    sum_v2 = 0.0
    sum_a2 = 0.0
    for start in range(0, n_samples, chunk):
        end = min(n_samples, start + chunk)
        v_blk = vocals_y[start:end].astype(np.float64)
        sum_v2 += float(np.dot(v_blk, v_blk))
        a_blk = np.zeros(end - start, dtype=np.float64)
        for part in accomp_parts:
            a_blk += part[start:end]
        sum_a2 += float(np.dot(a_blk, a_blk))

    e_v = sum_v2 / float(n_samples)
    e_a = sum_a2 / float(n_samples)
    v_to_accomp_db = float(np.clip(10.0 * np.log10((e_v + _EPS) / (e_a + _EPS)), -60.0, 60.0))

    # 帯域エネルギー比較は均等配置した有限サンプル窓のみで伴奏合算を行えば <4MB で済むわ
    win_len = min(4096, n_samples)
    max_windows = 128
    n_avail = n_samples // win_len
    starts = (
        np.arange(0, n_avail * win_len, win_len, dtype=np.int64)
        if n_avail <= max_windows
        else np.linspace(0, n_samples - win_len, max_windows, dtype=np.int64)
    )
    if starts.size == 0:
        return v_to_accomp_db, 0.0

    v_win = np.concatenate([vocals_y[s : s + win_len] for s in starts])
    a_win = np.zeros_like(v_win, dtype=np.float32)
    for part in accomp_parts:
        a_win += np.concatenate([part[s : s + win_len] for s in starts])

    bands = {"vocal_core": (300.0, 4000.0)}
    v_band = subband_energy_morphism(v_win, sr, bands)["vocal_core_energy"]
    a_band = subband_energy_morphism(a_win, sr, bands)["vocal_core_energy"]
    masking = float(np.clip(a_band / (v_band + a_band + _EPS), 0.0, 1.0)) if (v_band + a_band) > _EPS else 0.0
    return v_to_accomp_db, masking


def _compute_chunked_energies_Large(
    mix_arr: np.ndarray,
    aligned: dict[str, np.ndarray],
) -> tuple[float, float, dict[str, float]]:
    """チャンク分割積和により全長 float64 コピーなしで mix・各ステム・分離残差のエネルギーを算出しますわ！"""
    n_samples = mix_arr.size
    chunk = 1048576
    sum_mix2 = 0.0
    sum_res2 = 0.0
    stem_sums2: dict[str, float] = {name: 0.0 for name in aligned}

    for start in range(0, n_samples, chunk):
        end = min(n_samples, start + chunk)
        m_blk = mix_arr[start:end].astype(np.float64)
        sum_mix2 += float(np.dot(m_blk, m_blk))
        s_blk = np.zeros(end - start, dtype=np.float64)
        for name, wav in aligned.items():
            w_blk = wav[start:end].astype(np.float64)
            stem_sums2[name] += float(np.dot(w_blk, w_blk))
            s_blk += w_blk
        res_blk = m_blk - s_blk
        sum_res2 += float(np.dot(res_blk, res_blk))

    inv_n = 1.0 / float(n_samples)
    stem_energies = {name: val * inv_n for name, val in stem_sums2.items()}
    return sum_mix2 * inv_n, sum_res2 * inv_n, stem_energies


def cross_stem_foundation_morphism_Large(
    mix_mono: np.ndarray,
    stems_mono: Mapping[str, np.ndarray],
    sr: int,
) -> dict[str, Any]:
    """mix と Demucs ステム群からエネルギー占有率・稼働率・残差SNR・マスキング・編曲アークを算出する純粋射ですわ！

    Mor(R^N x Prod_{s}(R^N) x SR -> CrossStemMetrics)
    """
    mix_arr, aligned = _align_stems_length_Batch(mix_mono, stems_mono)
    if mix_arr.size == 0 or not aligned:
        return {
            "residual_snr_db": 0.0,
            "v_to_accomp_db": -60.0,
            "vocal_band_masking": 0.0,
            "per_stem": {},
            "arrangement_arc_32": {"stems": {}, "active_stem_count_seq_32": [0] * FIXED_SEQ_FRAMES},
        }

    # チャンク分割演算により長尺でも全長 float64 中間配列を確保しないわ
    e_mix, e_res, stem_energies = _compute_chunked_energies_Large(mix_arr, aligned)
    residual_snr_db = float(np.clip(10.0 * np.log10((e_mix + _EPS) / (e_res + _EPS)), -40.0, 60.0))

    total_stem_e = float(sum(stem_energies.values()))
    mix_rms_global = float(np.sqrt(max(e_mix, _EPS)))
    active_thresh = mix_rms_global * 0.05

    per_stem: dict[str, dict[str, float]] = {}
    arc_stems: dict[str, list[float]] = {}
    active_counts = np.zeros(FIXED_SEQ_FRAMES, dtype=np.int32)

    for name, wav in aligned.items():
        e_s = stem_energies[name]
        rms_curve = _frame_rms_curve(wav, FIXED_SEQ_FRAMES)
        active_ratio = float(np.mean(rms_curve >= active_thresh))
        stem_snr_db = float(np.clip(10.0 * np.log10((e_s + _EPS) / (e_res + _EPS)), -40.0, 60.0))
        per_stem[name] = {
            "energy_share": float(e_s / (total_stem_e + _EPS)) if total_stem_e > _EPS else 0.0,
            "active_ratio": active_ratio,
            "snr_db": stem_snr_db,
        }
        norm_curve = rms_curve / (float(np.max(rms_curve)) + _EPS) if float(np.max(rms_curve)) > _EPS else rms_curve
        arc_stems[name] = [float(v) for v in norm_curve]
        active_counts += (rms_curve >= active_thresh).astype(np.int32)

    vocals_y = aligned.get("vocals")
    accomp_parts = [w for k, w in aligned.items() if k != "vocals"]
    v_to_accomp_db, masking = _compute_vocal_masking_metrics(vocals_y, accomp_parts, sr, mix_arr.size)

    rhythm_pocket = (
        rhythm_pocket_and_ducking_morphism(aligned["drums"], aligned["bass"], sr)
        if ("drums" in aligned and "bass" in aligned)
        else {"kick_bass_lag_ms": 0.0, "pocket_sync_score": 0.0, "sidechain_ducking_score": 0.0}
    )

    from .common_pitch_harmony import (
        cross_stem_harmonic_tension_morphism,
        harmonic_bus_chroma_morphism,
    )

    harm_bus_full = harmonic_bus_chroma_morphism(aligned, sr)
    harm_bus_json = {k: v for k, v in harm_bus_full.items() if k != "harmonic_chroma"}
    harm_tension = cross_stem_harmonic_tension_morphism(
        aligned.get("bass"),
        aligned.get("vocals"),
        harm_bus_full["harmonic_chroma"],
        sr,
    )

    from .structure_ssm import multistem_structure_ssm_morphism

    multistem_struct = multistem_structure_ssm_morphism(
        mix_arr, aligned, sr, harmonic_chroma=harm_bus_full["harmonic_chroma"]
    )

    return {
        "residual_snr_db": residual_snr_db,
        "v_to_accomp_db": v_to_accomp_db,
        "vocal_band_masking": masking,
        "rhythm_pocket": rhythm_pocket,
        "harmonic_bus": harm_bus_json,
        "harmonic_tension": harm_tension,
        "multistem_structure": multistem_struct,
        "per_stem": per_stem,
        "arrangement_arc_32": {
            "stems": arc_stems,
            "active_stem_count_seq_32": [int(c) for c in active_counts],
        },
    }


def cross_stem_foundation_morphism(
    mix_mono: np.ndarray,
    stems_mono: Mapping[str, np.ndarray],
    sr: int,
) -> dict[str, Any]:
    """公開シンボル名を保つ純粋委譲ラッパーですわ！ Mor(R^N x Prod_{s}(R^N) x SR -> CrossStemMetrics)"""
    return cross_stem_foundation_morphism_Large(mix_mono, stems_mono, sr)
