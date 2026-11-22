"""
analyzer/common_dispatch.py
===========================
ステム名 (mix / vocals / drums / bass / guitar / piano / other) と設定 (analyzer.toml) から、
適用すべき Warmup 集合・プラグイン群・特化射を宣言的に合成する純粋ディスパッチ関手モジュールですわ！
Sep(IO_Effect, PureDomain): ステムごとの無駄な計算を排除しつつ統一された RawFeatures へ射影いたしますの。
"""

from __future__ import annotations

import logging
from functools import lru_cache
from typing import Any

from .audio_cutoff_lufs import (
    compute_ebur128_lufs_lra,
    compute_true_peak_dbtp,
    extract_audio_cutoff_lufs,
)
from .config_generator import load_analyzer_toml
from .core import AudioContext, FeatureExtractor
from .psychoacoustics_din45692 import extract_psychoacoustics
from .structure_ssm import extract_structure_ssm
from .types_features import AudioCutoffLufsFeatures, RawFeatures
from .voice_cpp import extract_voice_cpp

logger = logging.getLogger("analyzer.common_dispatch")

_COMMON_WARMUP = [
    "stft",
    "spectro",
    "power",
    "mel",
    "nap",
    "hnr_db",
    "hnr",
    "tempobeat",
    "onset_env",
    "tempogram",
]
_HARMONIC_WARMUP = ["chroma", "chroma_cqt"]

STEM_PLUGIN_MATRIX: dict[str, tuple[str, ...]] = {
    "mix": ("voice_cpp", "audio_cutoff_lufs", "psychoacoustics", "structure"),
    "vocals": ("voice_cpp", "audio_cutoff_lufs", "psychoacoustics"),
    "drums": ("audio_cutoff_lufs", "psychoacoustics"),
    "bass": ("audio_cutoff_lufs",),
    "guitar": ("audio_cutoff_lufs", "psychoacoustics"),
    "piano": ("audio_cutoff_lufs",),
    "other": ("audio_cutoff_lufs",),
}


@lru_cache(maxsize=1)
def _get_cached_analyzer_toml() -> dict[str, Any]:
    """analyzer.toml を一度だけ読み込み、以降の純粋ディスパッチ判定に再利用いたしますわ！"""
    try:
        return load_analyzer_toml()
    except Exception as exc:
        logger.warning(f"[common_dispatch] analyzer.toml ロードに失敗したためデフォルト構成を使用いたしますわ: {exc}")
        return {"execute": True, "plugins": {}}


def resolve_stem_warmup_pure(stem_name: str) -> list[str]:
    """ステム名に応じて事前計算 (Warmup) すべき必須プロパティ一覧を返却する純粋射ですわ！

    Mor(StemName -> list[str])
    """
    if stem_name == "drums":
        return list(_COMMON_WARMUP)
    return list(_COMMON_WARMUP) + list(_HARMONIC_WARMUP)


def resolve_stem_plugins_pure(
    stem_name: str,
    toml_cfg: dict[str, Any] | None = None,
) -> list[str]:
    """ステム名と設定辞書から有効な拡張プラグイン名一覧を決定する純粋射ですわ！

    Mor(StemName x TomlCfg -> list[PluginName])
    """
    cfg = toml_cfg if toml_cfg is not None else _get_cached_analyzer_toml()
    if not cfg.get("execute", True):
        return []
    plugins_cfg = cfg.get("plugins", {})
    candidates = STEM_PLUGIN_MATRIX.get(stem_name, ("audio_cutoff_lufs",))
    active: list[str] = []
    for pname in candidates:
        section = plugins_cfg.get(pname, {})
        if section.get("enabled", True):
            active.append(pname)
    return active


def _extract_stem_lufs_only(ctx: AudioContext) -> AudioCutoffLufsFeatures:
    """ステム向け軽量品質指標（TruePeak / EBU R128 LUFS & LRA のみ）を抽出いたしますわ！"""
    tp = compute_true_peak_dbtp(ctx.y)
    lufs, lra = compute_ebur128_lufs_lra(ctx.y, ctx.sr)
    return AudioCutoffLufsFeatures(
        cutoff_frequency_hz=0.0,
        cutoff_steepness_db_oct=0.0,
        is_fake_hires=False,
        true_peak_dbtp=tp,
        integrated_lufs=lufs,
        loudness_range_lra=lra,
    )


def _extract_cutoff_or_lufs_for_source(ctx: AudioContext) -> AudioCutoffLufsFeatures:
    """mix には偽ハイレゾ検査を含む完全抽出を、ステムには軽量 LUFS/LRA のみを適用いたしますわ！"""
    if ctx.source == "mix":
        return extract_audio_cutoff_lufs(ctx)
    return _extract_stem_lufs_only(ctx)


def extract_stem_plugins_morphism(
    ctx: AudioContext,
    toml_cfg: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """AudioContext.source に適合した拡張プラグイン群のみを実行し、結果辞書を返す純粋射ですわ！

    Mor(AudioContext -> dict[str, Any])
    """
    active = resolve_stem_plugins_pure(ctx.source, toml_cfg=toml_cfg)
    results: dict[str, Any] = {}
    if "voice_cpp" in active:
        results["voice_cpp_feat"] = extract_voice_cpp(ctx)
    if "audio_cutoff_lufs" in active:
        results["audio_cutoff_lufs_feat"] = _extract_cutoff_or_lufs_for_source(ctx)
    if "psychoacoustics" in active:
        results["psychoacoustics_feat"] = extract_psychoacoustics(ctx)
    if "structure" in active:
        results["structure_ssm_feat"] = extract_structure_ssm(ctx)
    return results


def build_stem_extractor_morphism(
    stem_name: str,
    base_extractor: FeatureExtractor[RawFeatures],
    toml_cfg: dict[str, Any] | None = None,
) -> FeatureExtractor[RawFeatures]:
    """基底 FeatureExtractor に対して、ステム特化プラグイン射を関手合成いたしますわ！

    Mor(FeatureExtractor[RawFeatures] -> FeatureExtractor[RawFeatures])
    """
    def _run_composed(ctx: AudioContext) -> RawFeatures:
        raw = base_extractor.run(ctx)
        plugin_feats = extract_stem_plugins_morphism(ctx, toml_cfg=toml_cfg)
        if "voice_cpp_feat" in plugin_feats:
            raw.voice_cpp_feat = plugin_feats["voice_cpp_feat"]
        if "audio_cutoff_lufs_feat" in plugin_feats:
            raw.audio_cutoff_lufs_feat = plugin_feats["audio_cutoff_lufs_feat"]
        if "psychoacoustics_feat" in plugin_feats:
            raw.psychoacoustics_feat = plugin_feats["psychoacoustics_feat"]
        if "structure_ssm_feat" in plugin_feats:
            raw.structure_ssm_feat = plugin_feats["structure_ssm_feat"]
        return raw

    return FeatureExtractor(_run_composed, f"stem_dispatch[{stem_name}]")
