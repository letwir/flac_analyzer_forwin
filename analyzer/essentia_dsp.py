"""
Analyzer Essentia & High-Level DSP Module
==========================================
コード進行推論 (_calc_chord_sequence)、ボーカルピッチ検出 (_calc_vocal_f0_seq)、
および Essentia 分類結果ラッパー処理を定義しますの。
"""

import logging
from typing import Any, cast

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


def _resample_to_16k(audio: np.ndarray, sr: int, target_sr: int = 16000) -> np.ndarray:
    """16kHz への高速リサンプリングですわ。"""
    if sr == target_sr:
        return audio
    import soxr
    return soxr.resample(audio, sr, target_sr)


def extract_mel_patches(
    audio: np.ndarray,
    sr: int,
    n_patches: int = 64,
    resample_sr: int = 16000,
    n_fft: int = 512,
    hop_length: int = 256,
    n_mels: int = 96,
    patch_size: int = 128,
    patch_hop: int = 62,
) -> np.ndarray:
    """Essentia EffNet 用の Mel スペクトログラムパッチを切り出す純粋計測関数ですわ！"""
    if audio.ndim > 1:
        if audio.shape[0] == 2:
            audio = np.mean(audio, axis=0)
        elif audio.shape[-1] == 2:
            audio = np.mean(audio, axis=-1)
        else:
            audio = np.mean(audio, axis=0)

    audio_16k = _resample_to_16k(audio, sr, target_sr=resample_sr)
    with LIBROSA_LOCK:
        mel = librosa.feature.melspectrogram(
            y=audio_16k,
            sr=resample_sr,
            n_fft=n_fft,
            hop_length=hop_length,
            n_mels=n_mels,
            power=2.0,
        )
    log_mel = np.log10(10000.0 * mel + 1.0).T

    if log_mel.shape[0] < patch_size:
        log_mel = np.pad(log_mel, ((0, patch_size - log_mel.shape[0]), (0, 0)))

    idxs = range(0, log_mel.shape[0] - patch_size + 1, patch_hop)
    raw = np.stack(
        [log_mel[i : i + patch_size] for i in idxs] or [log_mel[:patch_size]]
    )
    total_m = len(raw)

    if total_m < n_patches:
        raw = np.tile(raw, ((n_patches // total_m) + 1, 1, 1))
        raw = raw[:n_patches]
    elif total_m > n_patches:
        idx = np.linspace(0, total_m - 1, n_patches, dtype=int)
        raw = raw[idx]

    return raw.astype(np.float32)


def run_essentia_serialized(
    patches: np.ndarray,
    essentia_models: dict,
    sessions: dict | None = None,
    lock: Any = None,
) -> dict[str, float]:
    """ONNX(Essentia)による分類結果の生確率 dict を直列実行で抽出しますの。"""
    import re
    import models

    sess_dict = sessions if sessions is not None else models.GLOBAL_ONNX_SESSIONS
    sync_lock = lock if lock is not None else models.ONNX_LOCK

    predictions: dict[str, float] = {}
    effnet = sess_dict.get("effnet")
    if effnet is None:
        return predictions

    with sync_lock:
        try:
            embeddings = effnet.run(
                [sess_dict["eff_out"]],
                {sess_dict["eff_in"]: patches},
            )[0]
            embeddings = np.asarray(embeddings, dtype=np.float32)
            emb_mean = embeddings.mean(axis=0).astype(np.float32)
            emb_2d = emb_mean.reshape(1, -1)
        except Exception as e:
            logging.error(f"effnet backbone エラー: {e}", exc_info=True)
            return predictions

        for key, clf_sess in sess_dict.get("classifiers", {}).items():
            if key not in essentia_models:
                continue
            classes = essentia_models[key]["classes"]
            try:
                clf_input = clf_sess.get_inputs()[0]
                clf_output = clf_sess.get_outputs()[0]
                clf_in_name = clf_input.name
                clf_out_name = clf_output.name
                clf_shape = clf_input.shape

                if len(clf_shape) == 1:
                    inp = emb_mean
                else:
                    batch_dim = clf_shape[0]
                    if isinstance(batch_dim, int) and batch_dim > 1:
                        n = embeddings.shape[0]
                        if n < batch_dim:
                            inp = np.tile(embeddings, ((batch_dim // n) + 1, 1))[
                                :batch_dim
                            ]
                        else:
                            idx = np.linspace(0, n - 1, batch_dim, dtype=int)
                            inp = embeddings[idx]
                        inp = inp.astype(np.float32)
                    else:
                        inp = emb_2d

                preds = clf_sess.run([clf_out_name], {clf_in_name: inp})[0]
                preds = np.asarray(preds)

                if preds.ndim > 1:
                    prob = preds.mean(axis=0)
                else:
                    prob = preds

                from constants import CLASS_ALIAS
                for i, cls_name in enumerate(classes):
                    cls_name = CLASS_ALIAS.get(cls_name, cls_name)
                    if len(cls_name) <= 3:
                        continue
                    safe = re.sub(r"[^a-zA-Z0-9_]", "_", cls_name).upper()
                    predictions[f"ESSENTIA_{key.upper()}_{safe}"] = float(prob[i])

            except Exception as e:
                logging.error(f"分類器 [{key}] エラー: {e}", exc_info=True)

    return predictions
