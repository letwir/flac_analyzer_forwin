"""
analyzer/librosa_tonal.py
=========================
和声・調性特徴量（Chroma CQT, Tonnetz, Key/Scale, Chords）の抽出モジュールですわ！
"""

import logging
from typing import Any
import librosa
import numpy as np

from constants import KEY_PROFILES, NOTES
from .core import AudioContext, FeatureExtractor, FIXED_SEQ_FRAMES, LIBROSA_LOCK, _resample_to_fixed_frames
from .essentia_dsp import _calc_chord_sequence
from .registry_plugins import BasePlugin, register_plugin
from .types_features import ChromaFeatures, KeyFeatures, TonnetzFeatures

logger = logging.getLogger("analyzer.librosa_tonal")


def extract_chroma_obj(ctx: AudioContext) -> ChromaFeatures:
    """Chroma STFT から ChromaFeatures オブジェクトを構築しますわ！"""
    chroma = ctx.chroma
    safe_chroma = np.nan_to_num(chroma, nan=0.0, posinf=0.0, neginf=0.0)

    means = [float(v) for v in np.mean(safe_chroma, axis=1)]
    stds = [float(v) for v in np.std(safe_chroma, axis=1)]
    peaks = [float(v) for v in np.max(safe_chroma, axis=1)]

    entropies: list[float] = []
    for row in safe_chroma:
        abs_row = np.abs(row)
        s = float(np.sum(abs_row))
        if s < 1e-10:
            entropies.append(0.0)
        else:
            p = abs_row / s
            entropies.append(float(-np.sum(p * np.log2(p + 1e-10))))

    seqs: list[list[float]] = []
    for row in safe_chroma:
        seqs.append(_resample_to_fixed_frames(row, FIXED_SEQ_FRAMES))

    # フレームごとの和声エントロピー時系列
    frame_entropies = []
    for t in range(safe_chroma.shape[1]):
        col = safe_chroma[:, t]
        s = float(np.sum(col))
        if s < 1e-10:
            frame_entropies.append(0.0)
        else:
            p = col / s
            frame_entropies.append(float(-np.sum(p * np.log2(p + 1e-10))))
    frame_entropies_arr = np.array(frame_entropies)

    return ChromaFeatures(
        mean=means,
        std=stds,
        entropy=entropies,
        seq=seqs,
        peak=peaks,
        entropy_mean=float(np.mean(frame_entropies_arr)) if len(frame_entropies_arr) > 0 else 0.0,
        entropy_std=float(np.std(frame_entropies_arr)) if len(frame_entropies_arr) > 0 else 0.0,
        entropy_entropy=0.0,
        entropy_seq=_resample_to_fixed_frames(frame_entropies_arr, FIXED_SEQ_FRAMES),
    )


def extract_tonnetz(ctx: AudioContext) -> TonnetzFeatures:
    """Tonnetz 6次元和声特徴量を算出しますわ！"""
    with LIBROSA_LOCK:
        raw_tonnetz = librosa.feature.tonnetz(chroma=ctx.chroma_cqt)
    safe_tonnetz = np.nan_to_num(raw_tonnetz, nan=0.0, posinf=0.0, neginf=0.0)

    means = [float(v) for v in np.mean(safe_tonnetz, axis=1)]
    stds = [float(v) for v in np.std(safe_tonnetz, axis=1)]

    # delta
    deltas = np.diff(safe_tonnetz, axis=1, prepend=safe_tonnetz[:, :1])
    delta_means = [float(v) for v in np.mean(deltas, axis=1)]

    # 1D seq (ユークリッドノルムの時系列補間)
    tonnetz_norm = np.linalg.norm(safe_tonnetz, axis=0)
    seq = _resample_to_fixed_frames(tonnetz_norm, FIXED_SEQ_FRAMES)

    return TonnetzFeatures(
        mean=means,
        std=stds,
        delta_mean=delta_means,
        seq=seq,
    )


def extract_key(ctx: AudioContext) -> KeyFeatures:
    """Krumhansl-Schmuckler アルゴリズムによる Key / Scale 推定ですわ！"""
    chroma_avg = np.mean(ctx.chroma_cqt, axis=1)
    norm = np.linalg.norm(chroma_avg)
    if norm > 1e-9:
        chroma_avg = chroma_avg / norm

    best_key = "Unknown"
    best_scale = "Unknown"
    best_corr = -1.0

    for mode in ["major", "minor"]:
        profile = KEY_PROFILES[mode]
        p_norm = profile / np.linalg.norm(profile)
        for shift in range(12):
            rolled = np.roll(chroma_avg, -shift)
            corr = float(np.dot(rolled, p_norm))
            if corr > best_corr:
                best_corr = corr
                best_key = NOTES[shift]
                best_scale = mode

    # フレームごとの key_strength 時系列
    key_seq: list[float] = []
    if ctx.chroma_cqt.shape[1] > 0:
        cqt_cols = ctx.chroma_cqt
        shift = NOTES.index(best_key) if best_key in NOTES else 0
        p_norm = KEY_PROFILES[best_scale if best_scale in KEY_PROFILES else "major"]
        p_norm = p_norm / np.linalg.norm(p_norm)
        for t in range(cqt_cols.shape[1]):
            col = cqt_cols[:, t]
            cn = np.linalg.norm(col)
            if cn > 1e-9:
                rolled_col = np.roll(col / cn, -shift)
                key_seq.append(float(np.dot(rolled_col, p_norm)))
            else:
                key_seq.append(0.0)

    key_strength_seq = _resample_to_fixed_frames(np.array(key_seq), FIXED_SEQ_FRAMES)
    return KeyFeatures(
        key=best_key,
        scale=best_scale,
        key_strength=best_corr if best_corr > 0 else 0.0,
        key_strength_mean=float(np.mean(key_strength_seq)) if key_strength_seq else 0.0,
        key_strength_std=float(np.std(key_strength_seq)) if key_strength_seq else 0.0,
        key_strength_seq=key_strength_seq,
    )


@register_plugin(
    name="librosa_tonal",
    description="Chroma CQT, Tonnetz, Key/Scale, Chords",
    enabled_by_default=True,
    priority=30,
)
class LibrosaTonalPlugin(BasePlugin):
    def extract(self, ctx: AudioContext) -> dict[str, Any]:
        chroma_feat = extract_chroma_obj(ctx)
        tonnetz_feat = extract_tonnetz(ctx)
        key_feat = extract_key(ctx)
        chord_seq = _calc_chord_sequence(ctx)

        # 6次元 tonnetz リスト
        with LIBROSA_LOCK:
            raw_tonnetz = librosa.feature.tonnetz(chroma=ctx.chroma_cqt)
        safe_tonnetz = np.nan_to_num(raw_tonnetz, nan=0.0, posinf=0.0, neginf=0.0)
        tonnetz_seqs = [_resample_to_fixed_frames(row, FIXED_SEQ_FRAMES) for row in safe_tonnetz]

        return {
            "chroma_entropy_mean": chroma_feat.entropy_mean,
            "chroma_entropy_std": chroma_feat.entropy_std,
            "chroma_entropy_seq": chroma_feat.entropy_seq,
            "chroma": chroma_feat.seq,
            "tonnetz": tonnetz_seqs,
            "key_feat": key_feat,
            "chord_sequence": chord_seq,
        }
