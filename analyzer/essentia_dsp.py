"""
Analyzer Essentia & High-Level DSP Module
==========================================
コード進行推論 (_calc_chord_sequence)、ボーカルピッチ検出 (_calc_vocal_f0_seq)、
および Essentia 分類結果ラッパー処理を定義しますの。
"""

import logging
from typing import cast

import librosa
import numpy as np

from constants import CHORDS_DIC, NOTES
from .core import AudioContext, FIXED_SEQ_FRAMES, LIBROSA_LOCK, _resample_to_fixed_frames
from .types import EssentiaFeatures


def _calc_chord_sequence(ctx: AudioContext) -> list[str]:
    """12Dクロマと24コードのピアソン相関から32フレームのコード名時系列を生成しますわ！"""
    if ctx.source not in ("mix", "bass", "vocal", "piano", "guitar", "other"):
        return ["C" for _ in range(FIXED_SEQ_FRAMES)]

    try:
        chroma_cqt = ctx.chroma_cqt
        T_len = chroma_cqt.shape[1]
        if T_len == 0:
            return ["C" for _ in range(FIXED_SEQ_FRAMES)]

        chords_dic: dict[str, list[str]] = cast(
            dict[str, list[str]], CHORDS_DIC["chords_dic"]
        )
        chord_names: list[str] = sorted(chords_dic.keys())
        chord_vectors: list[np.ndarray] = []
        for cn in chord_names:
            notes_in_chord = chords_dic[cn]
            vec = np.zeros(12)
            for n in notes_in_chord:
                idx = NOTES.index(n)
                vec[idx] = 1.0
            chord_vectors.append(vec)

        chroma_norm = chroma_cqt / (
            np.max(np.abs(chroma_cqt), axis=0, keepdims=True) + 1e-10
        )
        frame_chords: list[str] = []
        for t in range(T_len):
            frame_vec = cast(np.ndarray, chroma_norm[:, t])
            best_corr = -2.0
            best_chord = "C"
            for cv, cn in zip(chord_vectors, chord_names):
                corr = float(
                    np.dot(frame_vec, cv)
                    / (np.linalg.norm(frame_vec) * np.linalg.norm(cv) + 1e-10)
                )
                if corr > best_corr:
                    best_corr = corr
                    best_chord = cn
            frame_chords.append(best_chord)

        chord_seq = _resample_to_fixed_frames(
            np.array([chord_names.index(c) for c in frame_chords])
        )
        result: list[str] = []
        for idx in chord_seq:
            idx_int = int(round(float(idx))) % len(chord_names)
            result.append(chord_names[idx_int])
        return result[:FIXED_SEQ_FRAMES]

    except Exception as e:
        logging.exception(
            f"[ChordSequence] コード列推定エラー (source: {ctx.source}): {e}"
        )
        return ["C" for _ in range(FIXED_SEQ_FRAMES)]


def _calc_vocal_f0_seq(ctx: AudioContext) -> list[float] | None:
    """vocalsステムに対してYINピッチ検出を行い、32フレームのピッチ時系列(Hz)を生成しますわ！"""
    if ctx.source != "vocals":
        return None

    try:
        with LIBROSA_LOCK:
            f0 = librosa.yin(
                ctx.y,
                sr=ctx.sr,
                fmin=float(librosa.note_to_hz("C2")),
                fmax=float(librosa.note_to_hz("C7")),
            )
        valid_f0 = f0[f0 > 0.0]
        if len(valid_f0) == 0:
            return [0.0] * FIXED_SEQ_FRAMES
        seq = _resample_to_fixed_frames(f0)
        return seq
    except Exception as e:
        logging.exception(
            f"[VocalF0] ピッチ検出にてエラーが発生いたしましたわ (source: {ctx.source}): {e}"
        )
        return [0.0] * FIXED_SEQ_FRAMES
