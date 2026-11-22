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
    """波形から固定フレーム数の RMS 包絡曲線を算出しますわ！ Mor(R^N -> R^T)"""
    if y.size == 0:
        return np.zeros(n_frames, dtype=np.float32)
    chunk = max(1, y.size // n_frames)
    usable = (y.size // chunk) * chunk
    if usable < chunk:
        val = float(np.sqrt(np.mean(y * y)))
        return np.full(n_frames, val, dtype=np.float32)
    blocks = y[:usable].reshape(-1, chunk)
    rms = np.sqrt(np.mean(blocks.astype(np.float64) ** 2, axis=1))
    return np.asarray(_resample_to_fixed_frames(rms, n_frames), dtype=np.float32)


def _bandpass_rms_envelope(y: np.ndarray, sr: int, f_lo: float, f_hi: float, hop_sec: float = 0.02) -> np.ndarray:
    """FFT 帯域フィルタ後に短時間 RMS 包絡線を抽出しますわ！ Mor(R^N x SR -> R^T)"""
    if y.size < 64 or sr <= 0:
        return np.empty(0, dtype=np.float32)
    step = max(1, int(round(sr * hop_sec)))
    n_frames = max(1, y.size // step)
    usable = n_frames * step
    # 長尺でも高速に帯域抽出するため 1 次元 rFFT フィルタを一括適用するわ
    spec = np.fft.rfft(y[:usable].astype(np.float64))
    freqs = np.fft.rfftfreq(usable, d=1.0 / float(sr))
    mask = (freqs >= f_lo) & (freqs <= f_hi)
    filtered = np.fft.irfft(spec * mask, n=usable)
    blocks = filtered.reshape(n_frames, step)
    return np.sqrt(np.mean(blocks * blocks, axis=1)).astype(np.float32)


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
    accomp_y: np.ndarray,
    sr: int,
) -> tuple[float, float]:
    """ボーカル対伴奏ラウドネス比 (dB) と 300Hz-4kHz 帯域マスキング度を算出しますわ！"""
    if vocals_y is None or vocals_y.size == 0:
        return -60.0, 0.0
    e_v = float(np.mean(vocals_y.astype(np.float64) ** 2))
    e_a = float(np.mean(accomp_y.astype(np.float64) ** 2))
    v_to_accomp_db = float(np.clip(10.0 * np.log10((e_v + _EPS) / (e_a + _EPS)), -60.0, 60.0))

    bands = {"vocal_core": (300.0, 4000.0)}
    v_band = subband_energy_morphism(vocals_y, sr, bands)["vocal_core_energy"]
    a_band = subband_energy_morphism(accomp_y, sr, bands)["vocal_core_energy"]
    # 伴奏側の中音域衝突比: 伴奏がボーカル中核帯域をどれだけ埋めているか (0..1)
    masking = float(np.clip(a_band / (v_band + a_band + _EPS), 0.0, 1.0)) if (v_band + a_band) > _EPS else 0.0
    return v_to_accomp_db, masking


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

    # 分離残差 (mix - sum(stems)) から Demucs 再構成信頼度 SNR を算出するわ
    stem_sum = np.zeros_like(mix_arr, dtype=np.float64)
    stem_energies: dict[str, float] = {}
    for name, wav in aligned.items():
        w64 = wav.astype(np.float64)
        stem_sum += w64
        stem_energies[name] = float(np.mean(w64 * w64))

    residual = mix_arr.astype(np.float64) - stem_sum
    e_res = float(np.mean(residual * residual))
    e_mix = float(np.mean(mix_arr.astype(np.float64) ** 2))
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
    accomp_y = np.sum(accomp_parts, axis=0) if accomp_parts else np.zeros_like(mix_arr)
    v_to_accomp_db, masking = _compute_vocal_masking_metrics(vocals_y, np.asarray(accomp_y, dtype=np.float32), sr)

    rhythm_pocket = (
        rhythm_pocket_and_ducking_morphism(aligned["drums"], aligned["bass"], sr)
        if ("drums" in aligned and "bass" in aligned)
        else {"kick_bass_lag_ms": 0.0, "pocket_sync_score": 0.0, "sidechain_ducking_score": 0.0}
    )

    from .common_pitch_harmony import harmonic_bus_chroma_morphism

    harm_bus_full = harmonic_bus_chroma_morphism(aligned, sr)
    harm_bus_json = {k: v for k, v in harm_bus_full.items() if k != "harmonic_chroma"}

    return {
        "residual_snr_db": residual_snr_db,
        "v_to_accomp_db": v_to_accomp_db,
        "vocal_band_masking": masking,
        "rhythm_pocket": rhythm_pocket,
        "harmonic_bus": harm_bus_json,
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
