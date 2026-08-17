"""
analyzer/config_generator.py
============================
外だし設定ファイル analyzer.toml / analyzer.toml.example の自動生成、
エディタ自動起動 (notepad / sakura / code 等)、および安全弁 (execute=false) 管理モジュールですわ！
"""

import logging
import os
import subprocess
import sys
from typing import Any

from .registry_plugins import PluginRegistry

logger = logging.getLogger("analyzer.config_generator")

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))


def generate_default_analyzer_toml_content(execute: bool = False) -> str:
    """現行解析機のみを有効化し、追加分析器を無効化した TOML 設定文字列を生成しますわ！"""
    lines = [
        "# ==============================================================================",
        "# FLAC Analyzer Plugin Configuration (analyzer.toml)",
        "# ==============================================================================",
        "# 【安全確認】解析を実行するには execute を true に変更してください。",
        f"execute = {'true' if execute else 'false'}",
        "",
        "# ─────────────────────────────────────────────",
        "# 現行解析機 (Current Active Analyzers)",
        "# ─────────────────────────────────────────────",
        "[plugins.librosa_dynamics]",
        "enabled = true",
        "",
        "[plugins.librosa_spectral]",
        "enabled = true",
        "",
        "[plugins.librosa_tonal]",
        "enabled = true",
        "",
        "[plugins.librosa_rhythm]",
        "enabled = true",
        "",
        "[plugins.librosa_timbre]",
        "enabled = true",
        "",
        "[plugins.librosa_vocalpitch]",
        "enabled = true",
        "",
        "[plugins.scipy_stats]",
        "enabled = true",
        "",
        "# ─────────────────────────────────────────────",
        "# 追加分析器 (Additional Analyzers: 治具または個別有効化用)",
        "# ─────────────────────────────────────────────",
        "[plugins.psychoacoustics]",
        "enabled = false",
        "calc_sharpness = true",
        "calc_roughness = true",
        "calc_tonality = true",
        "",
        "[plugins.structure]",
        "enabled = false",
        "calc_chorus = true",
        "calc_drop = true",
        "",
        "[plugins.voice_cpp]",
        "enabled = false",
        "",
        "[plugins.audio_cutoff_lufs]",
        "enabled = false",
        "detect_cutoff = true",
        "calc_true_peak = true",
        "calc_ebur128 = true",
        "",
    ]
    return "\n".join(lines)


def create_analyzer_toml_if_missing(
    target_path: str | None = None,
    example_path: str | None = None,
) -> tuple[str, bool]:
    """analyzer.toml および analyzer.toml.example を生成しますわ。"""
    if target_path is None:
        target_path = os.path.join(PROJECT_ROOT, "analyzer.toml")
    if example_path is None:
        example_path = os.path.join(PROJECT_ROOT, "analyzer.toml.example")

    content = generate_default_analyzer_toml_content(execute=False)

    # 1. analyzer.toml.example を常に最新状態で作成
    with open(example_path, "w", encoding="utf-8") as f:
        f.write(content)
    logger.info(f"analyzer.toml.example を生成いたしました: {example_path}")

    # 2. analyzer.toml が未存在の場合に自動生成
    created = False
    if not os.path.exists(target_path):
        with open(target_path, "w", encoding="utf-8") as f:
            f.write(content)
        created = True
        logger.info(f"analyzer.toml を新規作成いたしました (execute=false): {target_path}")

    return target_path, created


def open_in_configured_editor(file_path: str):
    """config.toml に指定されたエディタ（notepad/sakura/code等）で設定ファイルを開きますわ！"""
    cfg_path = os.path.join(PROJECT_ROOT, "config.toml")
    editor_cmd = "notepad.exe"

    if os.path.exists(cfg_path):
        try:
            try:
                import tomllib
                with open(cfg_path, "rb") as f:
                    cfg = tomllib.load(f)
            except ImportError:
                import tomli
                with open(cfg_path, "rb") as f:
                    cfg = tomli.load(f)

            configured_editor = cfg.get("editor") or cfg.get("tools", {}).get("editor")
            if configured_editor:
                editor_cmd = configured_editor
        except Exception as e:
            logger.warning(f"config.toml からのエディタ取得に失敗いたしました: {e}")

    logger.info(f"エディタ [{editor_cmd}] で設定ファイルを開きますわ: {file_path}")
    try:
        if sys.platform == "win32":
            subprocess.Popen([editor_cmd, file_path], shell=False)
        else:
            subprocess.Popen([editor_cmd, file_path])
    except Exception as e:
        logger.warning(f"エディタ [{editor_cmd}] の起動に失敗いたしました。notepad.exe で再試行いたします: {e}")
        try:
            subprocess.Popen(["notepad.exe", file_path])
        except Exception:
            pass


def load_analyzer_toml(config_path: str | None = None) -> dict[str, Any]:
    """analyzer.toml を読み込み、安全弁 (execute=true) を検証しますわ！"""
    if config_path is None:
        config_path = os.path.join(PROJECT_ROOT, "analyzer.toml")

    if not os.path.exists(config_path):
        # 未存在時は自動生成してエディタを起動
        cfg_file, _ = create_analyzer_toml_if_missing(target_path=config_path)
        open_in_configured_editor(cfg_file)
        raise RuntimeError(
            f"analyzer.toml が見つからなかったため新規生成いたしました ({config_path})。\n"
            f"安全弁 execute = false に設定されております。内容を確認し execute = true に更新してください。"
        )

    try:
        try:
            import tomllib
            with open(config_path, "rb") as f:
                cfg = tomllib.load(f)
        except ImportError:
            import tomli
            with open(config_path, "rb") as f:
                cfg = tomli.load(f)
    except Exception as e:
        raise RuntimeError(f"analyzer.toml の構文解析に失敗いたしましたわ: {e}")

    # 安全弁チェック
    execute_flag = cfg.get("execute", False)
    if not execute_flag:
        open_in_configured_editor(config_path)
        raise RuntimeError(
            f"【安全弁作動】analyzer.toml の execute が false になっております。\n"
            f"設定ファイル ({config_path}) を確認の上、execute = true に書き換えて再実行してくださいませ。"
        )

    return cfg
