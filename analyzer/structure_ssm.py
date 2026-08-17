"""
analyzer/structure_ssm.py
=========================
自己類似度行列 (Self-Similarity Matrix: SSM) および Novelty 曲線による
楽曲構造・サビ (Chorus) 検出・展開複雑度解析モジュールですわ！
"""

import logging
import math
from typing import Any
import librosa
import numpy as np
from scipy.ndimage import gaussian_filter1d

from .core import AudioContext, FeatureExtractor, FIXED_SEQ_FRAMES, _resample_to_fixed_frames
from .registry_plugins import BasePlugin, register_plugin
from .types_features import StructureSsmFeatures

logger = logging.getLogger("analyzer.structure_ssm")


def compute_ssm(features: np.ndarray) -> np.ndarray:
    """特徴量シーケンス (d x T) から自己類似度行列 (T x T) をコサイン類似度で算出しますわ！"""
    if features.shape[1] == 0:
        return np.zeros((1, 1), dtype=np.float32)
    # L2 正規化
    norms = np.linalg.norm(features, axis=0, keepdims=True)
    norms = np.where(norms < 1e-9, 1.0, norms)
    norm_feat = features / norms
    # Dot product
    ssm = np.dot(norm_feat.T, norm_feat)
    return np.clip(ssm, 0.0, 1.0)


def compute_novelty_curve(ssm: np.ndarray, kernel_size: int = 16) -> np.ndarray:
    """Checkerboard カーネルとの畳み込みにより構造変化点 (Novelty 曲線) を算出しますわ！"""
    n = ssm.shape[0]
    if n < kernel_size:
        return np.zeros(n, dtype=np.float32)

    # 2D Checkerboard kernel (-1 / +1)
    k_half = kernel_size // 2
    kernel = np.ones((kernel_size, kernel_size), dtype=np.float32)
    kernel[:k_half, :k_half] = 1.0
    kernel[k_half:, k_half:] = 1.0
    kernel[:k_half, k_half:] = -1.0
    kernel[k_half:, :k_half] = -1.0

    # ガウス窓による平滑テーパー
    gauss = np.outer(
        np.exp(-0.5 * (np.linspace(-2, 2, kernel_size) ** 2)),
        np.exp(-0.5 * (np.linspace(-2, 2, kernel_size) ** 2))
    )
    kernel *= gauss

    novelty = np.zeros(n, dtype=np.float32)
    for i in range(k_half, n - k_half):
        sub_ssm = ssm[i - k_half : i + k_half, i - k_half : i + k_half]
        novelty[i] = np.sum(sub_ssm * kernel)

    novelty = np.maximum(0.0, novelty)
    max_val = np.max(novelty)
    if max_val > 1e-9:
        novelty /= max_val
    return novelty


def detect_chorus_and_drop(
    novelty: np.ndarray,
    energy_env: np.ndarray,
    duration_sec: float,
) -> tuple[float, float, float, float]:
    """Novelty 曲線とエネルギーから Chorus 開始・終了秒数、ドロップ位置、展開複雑度を特定しますわ！"""
    n_frames = len(novelty)
    if n_frames == 0 or duration_sec <= 0:
        return 0.0, 0.0, 0.0, 0.0

    time_per_frame = duration_sec / n_frames

    # ドロップ位置: 最大エネルギー立ち上がり点
    if len(energy_env) > 1:
        energy_diff = np.diff(energy_env, prepend=energy_env[0])
        drop_frame = int(np.argmax(energy_diff))
        drop_pos_sec = float(drop_frame * (duration_sec / len(energy_env)))
    else:
        drop_pos_sec = float(duration_sec * 0.3)

    # サビ区間推定: 楽曲の 30%〜75% の区間で最も平均エネルギーが高く、境界が Novelty ピークである区間
    start_search = int(n_frames * 0.25)
    end_search = int(n_frames * 0.85)

    if start_search < end_search and len(energy_env) >= n_frames:
        chorus_mid = start_search + int(np.argmax(energy_env[start_search:end_search]))
        chorus_start_frame = max(0, chorus_mid - int(15.0 / time_per_frame))
        chorus_end_frame = min(n_frames, chorus_mid + int(15.0 / time_per_frame))
    else:
        chorus_start_frame = int(n_frames * 0.35)
        chorus_end_frame = int(n_frames * 0.55)

    chorus_start_sec = float(chorus_start_frame * time_per_frame)
    chorus_end_sec = float(chorus_end_frame * time_per_frame)

    # 展開複雑度 (Structural Complexity = Novelty のエントロピー / 分散)
    complexity = float(np.std(novelty) * 10.0)

    return chorus_start_sec, chorus_end_sec, drop_pos_sec, complexity


def extract_structure_ssm(ctx: AudioContext) -> StructureSsmFeatures:
    """SSM 構造解析を実行しますわ！"""
    duration_sec = float(len(ctx.y) / ctx.sr) if ctx.sr > 0 else 0.0
    if duration_sec < 1.0:
        return StructureSsmFeatures()

    # Chroma + MFCC 複合特徴量による SSM 構築
    chroma = ctx.chroma
    with_mfcc = np.vstack([chroma, ctx.centroid[np.newaxis, :]])
    ssm = compute_ssm(with_mfcc)
    novelty = compute_novelty_curve(ssm)

    rms_curve = librosa.feature.rms(S=ctx.spectro)[0]
    chorus_s, chorus_e, drop_pos, complexity = detect_chorus_and_drop(
        novelty, rms_curve, duration_sec
    )

    novelty_seq = _resample_to_fixed_frames(novelty, FIXED_SEQ_FRAMES)

    return StructureSsmFeatures(
        chorus_start_sec=chorus_s,
        chorus_end_sec=chorus_e,
        drop_position_sec=drop_pos,
        structural_complexity=complexity,
        novelty_seq=novelty_seq,
    )


@register_plugin(
    name="structure",
    description="SSM (Self-Similarity Matrix), Novelty curve, Chorus detection, Drop, Structural complexity",
    enabled_by_default=False,
    priority=85,
    options={"calc_chorus": True, "calc_drop": True},
)
class StructureSsmPlugin(BasePlugin):
    def extract(self, ctx: AudioContext) -> dict[str, Any]:
        feat = extract_structure_ssm(ctx)
        return {
            "structure_ssm_feat": feat,
        }
