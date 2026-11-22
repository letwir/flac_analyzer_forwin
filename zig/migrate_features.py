"""
zig/migrate_features.py
=======================
新規追加された音響分析器（Stereo Field, DIN 45692, SSM, CPP, Cutoff/LUFS等）を、
既存の PostgreSQL レコードまたは FLAC ファイルに対してオンデマンドで追加適用する
オフライン・メンテナンス治具（Maintenance Jig）スクリプトですわ！

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

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")
if hasattr(sys.stderr, "reconfigure"):
    sys.stderr.reconfigure(encoding="utf-8")

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

import numpy as np
import soundfile as sf

import flac_decode
from analyzer.audio_cutoff_lufs import extract_audio_cutoff_lufs
from analyzer.common_stereo import mono_downmix_morphism, stereo_field_morphism
from analyzer.core import AudioContext
from analyzer.psychoacoustics_din45692 import extract_psychoacoustics
from analyzer.structure_ssm import extract_structure_ssm
from analyzer.voice_cpp import extract_voice_cpp

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


def _extract_stereo_into_result(y: np.ndarray, sr: int, result: dict[str, Any]) -> np.ndarray:
    """ステレオ波形 (N,2) / (2,N) からステレオ射を抽出し、後続プラグイン用モノラル波形を返しますわ！"""
    arr = np.asarray(y, dtype=np.float32)
    if arr.ndim > 1:
        y_ch_first = arr.T if (arr.shape[-1] == 2 and arr.shape[0] != 2) else arr
        st = stereo_field_morphism(y_ch_first, sr)
        result["scalars"]["stereo"] = {
            "pan_mean": float(st["pan_mean"]),
            "pan_std": float(st["pan_std"]),
            "stereo_width": float(st["stereo_width"]),
            "lr_correlation": float(st["lr_correlation"]),
            "low_band_phase_coherence": float(st["low_band_phase_coherence"]),
        }
        result["sequences"]["pan_seq_32"] = st["pan_seq_32"]
        result["sequences"]["width_seq_32"] = st["width_seq_32"]
        return mono_downmix_morphism(y_ch_first)
    return np.ascontiguousarray(arr, dtype=np.float32)


def compute_additional_features_pure_Complex(
    y: np.ndarray,
    sr: int,
    plugins: list[str] | None = None,
) -> dict[str, Any]:
    """波形配列とサンプリングレートから、ステレオ空間特徴量および拡張プラグイン特徴量を純粋計算しますわ！

    Mor((R^{2xN} | R^N) x SR -> AdditionalFeatures)
    """
    if plugins is None:
        plugins = ["stereo", "psychoacoustics", "structure", "voice_cpp", "audio_cutoff_lufs"]

    result: dict[str, Any] = {"scalars": {}, "sequences": {}}
    y_mono = _extract_stereo_into_result(y, sr, result) if "stereo" in plugins else mono_downmix_morphism(y)

    ctx = AudioContext(y=y_mono, sr=sr, source="mix")
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


def compute_additional_features_pure(
    y: np.ndarray,
    sr: int,
    plugins: list[str] | None = None,
) -> dict[str, Any]:
    """公開シンボル名を保つ純粋委譲ラッパーですわ！"""
    return compute_additional_features_pure_Complex(y, sr, plugins)


def merge_jsonb_features_pure(
    existing_features: dict[str, Any],
    additional_features: dict[str, Any],
) -> dict[str, Any]:
    """既存の features JSONB 構造に対して、破壊せずに新規特徴量をディープマージしますわ！

    Mor(ExistingFeatures x AdditionalFeatures -> MergedFeatures)
    """
    merged = dict(existing_features)
    mix_data = dict(merged.get("mix", {})) if isinstance(merged.get("mix"), dict) else {}
    mix_scalars = dict(mix_data.get("scalars", {})) if isinstance(mix_data.get("scalars"), dict) else {}
    mix_sequences = dict(mix_data.get("sequences", {})) if isinstance(mix_data.get("sequences"), dict) else {}

    mix_scalars.update(additional_features.get("scalars", {}))
    mix_sequences.update(additional_features.get("sequences", {}))
    mix_data["scalars"] = mix_scalars
    mix_data["sequences"] = mix_sequences
    merged["mix"] = mix_data
    return merged


def get_db_connection_kwargs() -> dict[str, Any]:
    """config.toml の [database].url または環境変数から PostgreSQL 接続情報を解決しますわ。"""
    cfg_path = os.path.join(PROJECT_ROOT, "config.toml")
    if not os.path.exists(cfg_path):
        cfg_path = os.path.join(PROJECT_ROOT, "config.toml.example")

    try:
        import tomllib

        with open(cfg_path, "rb") as f:
            cfg = tomllib.load(f)
        db_sec = cfg.get("database") or cfg.get("postgres") or {}
        if db_sec.get("url"):
            return {"dsn": str(db_sec["url"]).strip()}
        if db_sec.get("host"):
            return {
                "host": db_sec.get("host", "localhost"),
                "port": db_sec.get("port", 5432),
                "user": db_sec.get("user", "postgres"),
                "password": db_sec.get("password", ""),
                "dbname": db_sec.get("dbname", "music_catalog"),
            }
    except Exception as exc:
        logger.warning(f"config.toml の読み込みに失敗いたしました: {exc}")

    env_url = (os.environ.get("FLAC_DB_URL") or os.environ.get("DATABASE_URL") or "").strip()
    if env_url:
        return {"dsn": env_url}
    return {}


def _decode_track_audio(filepath: str, track_number: int | None) -> tuple[np.ndarray, int]:
    """単曲 FLAC および CUE 埋め込みマルチトラック FLAC をステレオ保持のままデコードいたしますわ！"""
    handle = flac_decode.build_flac_handle(filepath)
    if not handle.slices:
        y, sr = sf.read(filepath, dtype="float32")
        return y, int(sr)

    target_slice = handle.slices[0]
    if track_number is not None and len(handle.slices) > 1:
        matched = [s for s in handle.slices if s.track_number == track_number]
        if len(matched) != 1:
            raise ValueError(f"track_number {track_number} not uniquely matched in {filepath}")
        target_slice = matched[0]

    y, _ = flac_decode.process_slice_with_seq_safety(
        filepath,
        target_slice.start_sample,
        target_slice.end_sample,
        handle.sample_rate,
        handle.channels,
    )
    return y, int(handle.sample_rate)


def run_file_analysis(file_path: str, plugins: list[str] | None = None) -> None:
    """単一 FLAC ファイルに対する直接解析と結果出力ですわ！"""
    if not os.path.exists(file_path):
        logger.error(f"ファイルが見つかりませんことよ: {file_path}")
        sys.exit(1)

    logger.info(f"{CYAN}音源ファイルをロードしております: {file_path}{RESET}")
    y, sr = sf.read(file_path, dtype="float32")
    t_start = time.perf_counter()
    res = compute_additional_features_pure(y, sr, plugins)
    dur = time.perf_counter() - t_start
    print(json.dumps({"status": "success", "file": file_path, "duration_sec": dur, "additional_features": res}, indent=2, ensure_ascii=False))


def _build_migration_select_sql(target_hash: str | None, target_id: int | None, limit: int | None) -> tuple[str, list[Any]]:
    """マイグレーション対象レコードを取得するパラメータ化 SQL を組み立てますわ！"""
    clauses = ["features IS NOT NULL", "features <> '{}'::jsonb"]
    params: list[Any] = []
    if target_id is not None:
        clauses.append("id = %s")
        params.append(target_id)
    if target_hash:
        clauses.append("audio_hash = %s")
        params.append(target_hash)
    sql = f"SELECT id, audio_hash, filepath, track_number, features FROM raw.library_flac WHERE {' AND '.join(clauses)} ORDER BY id DESC"
    if limit is not None and limit > 0:
        sql += " LIMIT %s"
        params.append(limit)
    return sql, params


def run_db_migration_Batch(
    dry_run: bool = False,
    limit: int | None = None,
    batch_size: int = 100,
    target_hash: str | None = None,
    target_id: int | None = None,
    plugins: list[str] | None = None,
) -> int:
    """PostgreSQL 内の既存レコードに対する差分マイグレーションを実行し、更新件数を返しますわ！"""
    import psycopg2
    import psycopg2.extras

    conn_kwargs = get_db_connection_kwargs()
    if not conn_kwargs:
        raise RuntimeError("PostgreSQL connection settings not found in config.toml or environment")

    logger.info(f"{GREEN}PostgreSQL に接続いたしますわ...{RESET}")
    conn = psycopg2.connect(**conn_kwargs)
    conn.autocommit = False

    updated_count = 0
    try:
        with conn.cursor(cursor_factory=psycopg2.extras.DictCursor) as cur:
            sql, params = _build_migration_select_sql(target_hash, target_id, limit)
            cur.execute(sql, params)
            rows = cur.fetchall()
            total_rows = len(rows)
            logger.info(f"{CYAN}対象レコード数: {total_rows} 件{RESET}")
            if total_rows == 0:
                return 0

            for idx, row in enumerate(rows, 1):
                row_id = int(row["id"])
                audio_hash = str(row["audio_hash"])
                filepath = str(row["filepath"])
                track_number = row["track_number"]
                existing_feat = row["features"] or {}

                if not os.path.exists(filepath):
                    logger.warning(f"[{idx}/{total_rows}] 実ファイルが見つかりません ({filepath})。スキップいたします。")
                    continue

                try:
                    y, sr = _decode_track_audio(filepath, track_number)
                    add_feat = compute_additional_features_pure(y, sr, plugins)
                    merged_feat = merge_jsonb_features_pure(existing_feat, add_feat)

                    if dry_run:
                        logger.info(f"[DRY-RUN] [{idx}/{total_rows}] id={row_id} hash={audio_hash} -> 差分マージ成功 (DB書き込みなし)")
                        continue

                    cur.execute(
                        "UPDATE raw.library_flac SET features = %s::jsonb, analyzed_at = CURRENT_TIMESTAMP WHERE id = %s AND audio_hash = %s",
                        (json.dumps(merged_feat), row_id, audio_hash),
                    )
                    updated_count += cur.rowcount
                    if updated_count > 0 and updated_count % batch_size == 0:
                        conn.commit()
                        logger.info(f"{GREEN}[Commit] {updated_count} 件をコミットいたしましたわ！{RESET}")
                except Exception as exc:
                    logger.exception(f"[{idx}/{total_rows}] id={row_id} hash={audio_hash} 処理中に例外が発生いたしました: {exc}")

            if not dry_run and updated_count % batch_size != 0:
                conn.commit()
                logger.info(f"{GREEN}[Final Commit] 合計 {updated_count} 件のマイグレーションが完了いたしましたわ！{RESET}")
    except Exception:
        conn.rollback()
        logger.exception(f"{RED}マイグレーション中にエラーが発生し、ロールバックいたしました{RESET}")
        raise
    finally:
        conn.close()
    return updated_count


def run_db_migration(
    dry_run: bool = False,
    limit: int | None = None,
    batch_size: int = 100,
    target_hash: str | None = None,
    target_id: int | None = None,
    plugins: list[str] | None = None,
) -> int:
    """公開シンボル名を保つ委譲ラッパーですわ！"""
    return run_db_migration_Batch(dry_run, limit, batch_size, target_hash, target_id, plugins)


def main() -> None:
    parser = argparse.ArgumentParser(description="音響解析機能の追加・マイグレーション治具")
    parser.add_argument("--migrate-features", action="store_true", help="DBレコードに対する新規特徴量マイグレーションを実行")
    parser.add_argument("--dry-run", action="store_true", help="DB更新を行わずシミュレーション実行")
    parser.add_argument("--file", type=str, help="単一FLACファイルを直接解析")
    parser.add_argument("--limit", type=int, help="マイグレーション対象レコード数の上限")
    parser.add_argument("--batch-size", type=int, default=100, help="DBコミットのバッチサイズ")
    parser.add_argument("--id", type=int, help="特定の id のみを対象に指定")
    parser.add_argument("--track-hash", type=str, help="特定の audio_hash のみを対象に指定")
    parser.add_argument("--plugins", type=str, help="カンマ区切りのプラグイン名 (stereo,psychoacoustics,structure,voice_cpp,audio_cutoff_lufs)")
    args = parser.parse_args()

    selected_plugins = args.plugins.split(",") if args.plugins else None
    if args.file:
        run_file_analysis(args.file, selected_plugins)
        return
    if args.migrate_features or args.dry_run:
        run_db_migration(
            dry_run=args.dry_run,
            limit=args.limit,
            batch_size=args.batch_size,
            target_hash=args.track_hash,
            target_id=args.id,
            plugins=selected_plugins,
        )
        return
    parser.print_help()


if __name__ == "__main__":
    main()
