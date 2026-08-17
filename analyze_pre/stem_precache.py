"""
analyze_pre/stem_precache.py
============================
ステム波形データの事前検証・キャッシュおよび AudioContext 構築ヘルパーですわ！
"""

import logging
from typing import Any
import numpy as np

from analyzer.core import AudioContext, StemContext

logger = logging.getLogger("analyze_pre.stem_precache")


def verify_and_precache_stem(
    y: np.ndarray,
    sr: int,
    stem_name: str,
    spectro_path: str | None = None,
) -> AudioContext:
    """単一ステム波形の健全性を検証し、AudioContext を構築しますわ！

    Functor: WaveformArray -> AudioContext
    """
    if y is None or len(y) == 0:
        raise ValueError(f"ステム [{stem_name}] の波形データが空または None ですわ！")

    # 無限大・NaN のガード
    if not np.isfinite(y).all():
        logger.warning(f"ステム [{stem_name}] に NaN または Inf を検出いたしました。ゼロ置換いたします。")
        y = np.nan_to_num(y, nan=0.0, posinf=0.0, neginf=0.0)

    ctx = AudioContext(y=y, sr=sr, source=stem_name, spectro_path=spectro_path)
    return ctx


def precache_all_stems(
    stems_raw: dict[str, np.ndarray],
    sr: int,
    spectro_paths: dict[str, str] | None = None,
) -> StemContext:
    """全ステムの波形辞書から StemContext を構築しますわ！"""
    stems_ctx: dict[str, AudioContext] = {}
    for name, waveform in stems_raw.items():
        spath = spectro_paths.get(name) if spectro_paths else None
        stems_ctx[name] = verify_and_precache_stem(waveform, sr, name, spath)
    return StemContext(stems=stems_ctx)
