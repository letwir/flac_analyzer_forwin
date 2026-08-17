"""
zig/migrate_features.py
=======================
新規追加された音響分析器（DIN 45692, SSM, CPP, Cutoff/LUFS等）を、
既存の PostgreSQL レコードまたは FLAC ファイルに対してオンデマンドで追加適用する
オフライン・メンテナンス治具（Maintenance Jig）スクリプトですわ！

日常の Go オーケストレーター ETL パイプラインとは分離された独立治具として、
Pure Domain 計算と IO 副作用を厳格に分離して実装されておりますの。

使い方:
    # 1. 単一 FLAC ファイルの直接解析・プレビュー
    python zig/migrate_features.py --file "testFLAC/track.flac"

    # 2. DB マイグレーションのドライラン (確認のみ)
    python zig/migrate_features.py --migrate-features --dry-run --limit 5

    # 3. 実際の DB 差分マイグレーション実行 (JSONB マージ & analyzed_at 更新)
    python zig/migrate_features.py --migrate-features --batch-size 100
"""

import argparse
import json
import logging
import os
import sys
import time
from typing import Any

# UTF-8 出力保護
if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")
if hasattr(sys.stderr, "reconfigure"):
    sys.stderr.reconfigure(encoding="utf-8")

# 親ディレクトリを sys.path に追加してプロジェクト内モジュールをロード
PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

import numpy as np
import soundfile as sf

from analyzer.core import AudioContext
from analyzer.psychoacoustics_din45692 import extract_psychoacoustics
from analyzer.structure_ssm import extract_structure_ssm
from analyzer.voice_cpp import extract_voice_cpp
from analyzer.audio_cutoff_lufs import extract_audio_cutoff_lufs

# ログ設定
GREEN = "\033[32m"
YELLOW = "\033[33m"
CYAN = "\033[36m"
RED = "\033[31m"
RESET = "\033[0m"

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%H:%M:%S",
)
logger = logging.getLogger("zig.migrate_features")


# ─────────────────────────────────────────────
# 1. 純粋計算レイヤー (Pure Domain: 副作用なし)
# ─────────────────────────────────────────────
def compute_additional_features_pure(
    y: np.ndarray,
    sr: int,
    plugins: list[str] | None = None,
) -> dict[str, Any]:
    """波形配列とサンプリングレートから、新規追加プラグインの特徴量を純粋計算しますわ！

    Functor: (y, sr) -> dict[plugin_name, features]
    """
    if plugins is None:
        plugins = ["psychoacoustics", "structure", "voice_cpp", "audio_cutoff_lufs"]

    ctx = AudioContext(y=y, sr=sr, source="mix")
    result: dict[str, Any] = {"scalars": {}, "sequences": {}}

    try:
        if "psychoacoustics" in plugins:
            p_feat = extract_psychoacoustics(ctx)
            result["scalars"]["psychoacoustics"] = {
                "sharpness_acum": float(p_feat.sharpness_acum),
                "roughness_asper": float(p_feat.roughness_asper),
                "tonality": float(p_feat.tonality_val),
            }
            if p_feat.specific_loudness_seq:
                result["sequences"]["specific_loudness"] = p_feat.specific_loudness_seq

        if "structure" in plugins:
            s_feat = extract_structure_ssm(ctx)
            result["scalars"]["structure_ssm"] = {
                "chorus_start_sec": float(s_feat.chorus_start_sec),
                "chorus_end_sec": float(s_feat.chorus_end_sec),
                "drop_position_sec": float(s_feat.drop_position_sec),
                "structural_complexity": float(s_feat.structural_complexity),
            }
            if s_feat.novelty_seq:
                result["sequences"]["structure_novelty"] = s_feat.novelty_seq

        if "voice_cpp" in plugins:
            v_feat = extract_voice_cpp(ctx)
            result["scalars"]["voice_cpp"] = {
                "cpp_mean": float(v_feat.cpp_mean),
                "cpp_std": float(v_feat.cpp_std),
                "breathiness_score": float(v_feat.breathiness_score),
            }
            if v_feat.cpp_seq:
                result["sequences"]["voice_cpp_seq"] = v_feat.cpp_seq

        if "audio_cutoff_lufs" in plugins:
            c_feat = extract_audio_cutoff_lufs(ctx)
            result["scalars"]["audio_cutoff_lufs"] = {
                "cutoff_frequency_hz": float(c_feat.cutoff_frequency_hz),
                "cutoff_steepness_db_oct": float(c_feat.cutoff_steepness_db_oct),
                "is_fake_hires": bool(c_feat.is_fake_hires),
                "true_peak_dbtp": float(c_feat.true_peak_dbtp),
                "integrated_lufs": float(c_feat.integrated_lufs),
                "loudness_range_lra": float(c_feat.loudness_range_lra),
            }
    finally:
        ctx.clear()

    return result


def merge_jsonb_features_pure(
    existing_features: dict[str, Any],
    additional_features: dict[str, Any],
) -> dict[str, Any]:
    """既存の features JSONB 構造に対して、破壊せずに新規特徴量をディープマージしますわ！

    Morphism: (ExistingFeatures, AdditionalFeatures) -> MergedFeatures
    """
    merged = dict(existing_features)

    # mix レベルのマージ
    mix_data = merged.get("mix", {})
    if isinstance(mix_data, dict):
        mix_scalars = mix_data.get("scalars", {})
        mix_sequences = mix_data.get("sequences", {})

        if isinstance(mix_scalars, dict):
            mix_scalars.update(additional_features.get("scalars", {}))
            mix_data["scalars"] = mix_scalars

        if isinstance(mix_sequences, dict):
            mix_sequences.update(additional_features.get("sequences", {}))
            mix_data["sequences"] = mix_sequences

        merged["mix"] = mix_data

    return merged


# ─────────────────────────────────────────────
# 2. IO 副作用レイヤー (DB トランザクション & ファイル)
# ─────────────────────────────────────────────
def load_db_config() -> dict[str, Any]:
    """config.toml から PostgreSQL 接続設定を読み込みますわ。"""
    cfg_path = os.path.join(PROJECT_ROOT, "config.toml")
    if not os.path.exists(cfg_path):
        cfg_path = os.path.join(PROJECT_ROOT, "config.toml.example")

    try:
        try:
            import tomllib
            with open(cfg_path, "rb") as f:
                cfg = tomllib.load(f)
        except ImportError:
            import tomli
            with open(cfg_path, "rb") as f:
                cfg = tomli.load(f)
        return cfg.get("postgres", {})
    except Exception as e:
        logger.warning(f"config.toml の読み込みに失敗いたしました: {e}")
        return {}


def run_file_analysis(file_path: str, plugins: list[str] | None = None):
    """単一 FLAC ファイルに対する直接解析と結果出力ですわ！"""
    if not os.path.exists(file_path):
        logger.error(f"ファイルが見つかりませんことよ: {file_path}")
        sys.exit(1)

    logger.info(f"{CYAN}音源ファイルをロードしております: {file_path}{RESET}")
    y, sr = sf.read(file_path, dtype="float32")
    if y.ndim > 1:
        y = np.mean(y, axis=-1)

    t_start = time.perf_counter()
    res = compute_additional_features_pure(y, sr, plugins)
    dur = time.perf_counter() - t_start

    print(json.dumps({
        "status": "success",
        "file": file_path,
        "duration_sec": dur,
        "additional_features": res,
    }, indent=2, ensure_ascii=False))


def run_db_migration(
    dry_run: bool = False,
    limit: int | None = None,
    batch_size: int = 100,
    target_hash: str | None = None,
    plugins: list[str] | None = None,
):
    """PostgreSQL 内の既存レコードに対する差分マイグレーションを実行しますわ！"""
    import psycopg2
    import psycopg2.extras

    db_cfg = load_db_config()
    host = db_cfg.get("host", "localhost")
    port = db_cfg.get("port", 5432)
    user = db_cfg.get("user", "postgres")
    password = db_cfg.get("password", "")
    dbname = db_cfg.get("dbname", "music_catalog")

    logger.info(f"{GREEN}PostgreSQL ({host}:{port}/{dbname}) に接続いたしますわ...{RESET}")
    conn = psycopg2.connect(
        host=host,
        port=port,
        user=user,
        password=password,
        dbname=dbname,
    )
    conn.autocommit = False

    try:
        with conn.cursor(cursor_factory=psycopg2.extras.DictCursor) as cur:
            # 対象レコードの取得
            if target_hash:
                cur.execute(
                    "SELECT track_hash, file_path, features FROM raw.library_flac WHERE track_hash = %s",
                    (target_hash,)
                )
            else:
                query = "SELECT track_hash, file_path, features FROM raw.library_flac"
                if limit:
                    query += f" LIMIT {limit}"
                cur.execute(query)

            rows = cur.fetchall()
            total_rows = len(rows)
            logger.info(f"{CYAN}対象レコード数: {total_rows} 件{RESET}")

            if total_rows == 0:
                logger.info("更新対象のレコードはございませんでしたわ。")
                return

            updated_count = 0
            for idx, row in enumerate(rows, 1):
                track_hash = row["track_hash"]
                file_path = row["file_path"]
                existing_feat = row["features"] or {}

                if not os.path.exists(file_path):
                    logger.warning(f"[{idx}/{total_rows}] 実ファイルが見つかりません ({file_path})。スキップいたします。")
                    continue

                try:
                    y, sr = sf.read(file_path, dtype="float32")
                    if y.ndim > 1:
                        y = np.mean(y, axis=-1)

                    add_feat = compute_additional_features_pure(y, sr, plugins)
                    merged_feat = merge_jsonb_features_pure(existing_feat, add_feat)

                    if dry_run:
                        logger.info(f"[DRY-RUN] [{idx}/{total_rows}] {track_hash} -> 差分マージ成功 (DB書き込みなし)")
                    else:
                        cur.execute(
                            """
                            UPDATE raw.library_flac
                            SET features = %s,
                                analyzed_at = CURRENT_TIMESTAMP
                            WHERE track_hash = %s
                            """,
                            (json.dumps(merged_feat), track_hash)
                        )
                        updated_count += 1

                        if updated_count % batch_size == 0:
                            conn.commit()
                            logger.info(f"{GREEN}[Commit] {updated_count} 件をコミットいたしましたわ！{RESET}")

                except Exception as e:
                    logger.exception(f"[{idx}/{total_rows}] {track_hash} 処理中に例外が発生いたしました: {e}")

            if not dry_run and updated_count % batch_size != 0:
                conn.commit()
                logger.info(f"{GREEN}[Final Commit] 合計 {updated_count} 件のマイグレーションが完了いたしましたわ！{RESET}")

    except Exception as e:
        conn.rollback()
        logger.exception(f"{RED}マイグレーション中にエラーが発生し、ロールバックいたしました: {e}{RESET}")
        raise e
    finally:
        conn.close()


def main():
    parser = argparse.ArgumentParser(description="音響解析機能の追加・マイグレーション治具")
    parser.add_argument("--migrate-features", action="store_true", help="DBレコードに対する新規特徴量マイグレーションを実行")
    parser.add_argument("--dry-run", action="store_true", help="DB更新を行わずシミュレーション実行")
    parser.add_argument("--file", type=str, help="単一FLACファイルを直接解析")
    parser.add_argument("--limit", type=int, help="マイグレーション対象レコード数の上限")
    parser.add_argument("--batch-size", type=int, default=100, help="DBコミットのバッチサイズ")
    parser.add_argument("--track-hash", type=str, help="特定の track_hash のみを対象に指定")
    parser.add_argument("--plugins", type=str, help="カンマ区切りのプラグイン名 (psychoacoustics,structure,voice_cpp,audio_cutoff_lufs)")
    args = parser.parse_args()

    selected_plugins = args.plugins.split(",") if args.plugins else None

    if args.file:
        run_file_analysis(args.file, selected_plugins)
    elif args.migrate_features or args.dry_run:
        run_db_migration(
            dry_run=args.dry_run,
            limit=args.limit,
            batch_size=args.batch_size,
            target_hash=args.track_hash,
            plugins=selected_plugins,
        )
    else:
        parser.print_help()


if __name__ == "__main__":
    main()
