"""
analyze_pre/shm_prewarm.py
==========================
Demucs分離後のステム波形に対する Pre-warming および共有メモリ参照の密結合レイヤーですわ！
AudioContext の遅延プロパティを事前に計算・キャッシュして、並列抽出時の contention を根絶しますの。
"""

import logging
import time
from typing import Any

from analyzer.core import AudioContext

logger = logging.getLogger("analyze_pre.shm_prewarm")

DEFAULT_WARMUP_PROPERTIES = [
    "stft",
    "spectro",
    "power",
    "chroma",
    "mel",
    "nap",
    "hnr_db",
    "hnr",
    "chroma_cqt",
    "tempobeat",
    "onset_env",
    "tempogram",
    "centroid",
]


def prewarm_audio_context(
    ctx: AudioContext,
    properties: list[str] | None = None,
) -> float:
    """単一の AudioContext に対し指定された遅延プロパティを評価・キャッシュしますわ！

    Mor: AudioContext -> DurationSeconds
    """
    if properties is None:
        properties = DEFAULT_WARMUP_PROPERTIES

    t_start = time.perf_counter()
    for prop in properties:
        try:
            _ = getattr(ctx, prop)
        except Exception as e:
            logger.warning(
                f"[Prewarm] ステム [{ctx.source}] のプロパティ '{prop}' 計算中に例外を捕捉いたしましたわ: {e}"
            )
            # 例外を握りつぶさず警告ログとして残す
    return time.perf_counter() - t_start


def prewarm_stem_contexts(
    stems_dict: dict[str, AudioContext],
    properties_by_stem: dict[str, list[str]] | None = None,
) -> dict[str, float]:
    """全ステムの AudioContext を一括で Pre-warming いたしますわ！"""
    profile: dict[str, float] = {}
    for stem_name, ctx in stems_dict.items():
        props = (
            properties_by_stem.get(stem_name, DEFAULT_WARMUP_PROPERTIES)
            if properties_by_stem
            else DEFAULT_WARMUP_PROPERTIES
        )
        profile[stem_name] = prewarm_audio_context(ctx, props)
    return profile
