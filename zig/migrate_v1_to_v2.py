"""
zig/migrate_v1_to_v2.py
=======================
PostgreSQL (raw.library_flac) 上の v1 解析済みレコードを検出し、
v2 スキーマ確認・適用 (`sql/migration_v2.sql`) と v2 特徴量へのマイグレーションを実行する治具ですわ！

使い方:
    # 1. v2 未完了レコードの確認 (ドライラン)
    .venv/Scripts/python.exe zig/migrate_v1_to_v2.py --dry-run --limit 10

    # 2. v1 スキーマが残っている場合に migration_v2.sql も適用しつつ orchestrator でフル再解析
    .venv/Scripts/python.exe zig/migrate_v1_to_v2.py --apply-schema --limit 50

    # 3. 特定トラックID / パスのみ高速 mix-only バックフィル (Demucs なし・mix 拡張特徴量のみ)
    .venv/Scripts/python.exe zig/migrate_v1_to_v2.py --mode mix-only --id 12345
"""

from __future__ import annotations

import argparse
from collections import OrderedDict
import json
import logging
import os
import subprocess
import sys
import tomllib
from typing import Any

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

V2_REQUIRED_SCHEMA_COLUMNS = (
    "track_number",
    "album_artist",
    "album",
    "artist",
    "title",
)

V2_MIX_SCALAR_KEYS = (
    "stereo",
    "psychoacoustics",
    "audio_cutoff_lufs",
)

logger = logging.getLogger("zig.migrate_v1_to_v2")


def setup_logger() -> None:
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8")
    if hasattr(sys.stderr, "reconfigure"):
        sys.stderr.reconfigure(encoding="utf-8")
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s [%(levelname)s] [MigrateV2Zig] %(message)s",
        datefmt="%H:%M:%S",
    )


def _dict_get_dict(obj: Any, key: str) -> dict[str, Any] | None:
    if not isinstance(obj, dict):
        return None
    val = obj.get(key)
    return val if isinstance(val, dict) else None


def is_v2_feature_complete_pure(
    features: dict[str, Any] | None,
    require_demucs: bool = True,
) -> bool:
    """features JSONB が v2 必須セクションを備えているか純粋判定いたしますわ！"""
    mix = _dict_get_dict(features, "mix")
    mix_scalars = _dict_get_dict(mix, "scalars")
    if mix_scalars is None:
        return False
    for key in V2_MIX_SCALAR_KEYS:
        if key not in mix_scalars:
            return False

    if require_demucs:
        demucs = _dict_get_dict(features, "demucs")
        vocals = _dict_get_dict(demucs, "vocals")
        vocals_scalars = _dict_get_dict(vocals, "scalars")
        if vocals_scalars is None or "cross_stem" not in vocals_scalars:
            return False

    return True


def build_v1_select_sql(
    ids: list[int] | None = None,
    filepath: str | None = None,
    require_demucs: bool = True,
    limit: int = 0,
) -> tuple[str, list[Any]]:
    """v2 特徴量が未完了の raw.library_flac レコードを取得するパラメータ化 SQL を構築いたしますわ。"""
    mix_complete_sql = (
        "("
        "jsonb_typeof(features->'mix'->'scalars') = 'object' "
        "AND jsonb_exists(features->'mix'->'scalars', 'stereo') "
        "AND jsonb_exists(features->'mix'->'scalars', 'psychoacoustics') "
        "AND jsonb_exists(features->'mix'->'scalars', 'audio_cutoff_lufs')"
        ")"
    )
    if require_demucs:
        demucs_complete_sql = (
            "("
            "jsonb_typeof(features->'demucs'->'vocals'->'scalars') = 'object' "
            "AND jsonb_exists(features->'demucs'->'vocals'->'scalars', 'cross_stem')"
            ")"
        )
        v2_incomplete_clause = f"(features IS NULL OR features = '{{}}'::jsonb OR NOT ({mix_complete_sql} AND {demucs_complete_sql}))"
    else:
        v2_incomplete_clause = f"(features IS NULL OR features = '{{}}'::jsonb OR NOT {mix_complete_sql})"

    conditions = [v2_incomplete_clause]
    params: list[Any] = []

    if ids:
        conditions.append("id = ANY(%s)")
        params.append(list(ids))
    if filepath:
        conditions.append("filepath = %s")
        params.append(filepath)

    sql = (
        "SELECT id, audio_hash, filepath, track_number, features\n"
        "FROM raw.library_flac\n"
        f"WHERE {' AND '.join(conditions)}\n"
        "ORDER BY filepath ASC, track_number ASC NULLS FIRST, id ASC"
    )
    if limit > 0:
        sql += "\nLIMIT %s"
        params.append(limit)

    return sql, params


def group_rows_by_filepath_pure(
    rows: list[dict[str, Any]],
) -> list[tuple[str, list[dict[str, Any]]]]:
    """同一 filepath（CUE マルチトラック等）を初出順を保ってグルーピングいたしますわ。"""
    grouped: OrderedDict[str, list[dict[str, Any]]] = OrderedDict()
    for row in rows:
        fp = str(row.get("filepath") or "")
        if fp not in grouped:
            grouped[fp] = []
        grouped[fp].append(row)
    return list(grouped.items())


def build_orchestrator_command_pure(
    orchestrator_exe: str,
    filepath: str,
    config_path: str | None = None,
    force: bool = True,
) -> list[str]:
    """orchestrator single-file 再解析コマンド列を純粋構築いたしますわ。"""
    cmd = [orchestrator_exe, "-single-file", filepath]
    if config_path:
        cmd.extend(["-config", config_path])
    if force:
        cmd.append("-force")
    return cmd


def get_db_connection_kwargs(config_path: str | None = None) -> dict[str, Any]:
    """config.toml または環境変数から PostgreSQL 接続設定を解決いたしますわ。"""
    candidates = [config_path] if config_path else [
        os.path.join(PROJECT_ROOT, "config.toml"),
        os.path.join(PROJECT_ROOT, "config.toml.example"),
    ]
    for cfg_path in candidates:
        if not cfg_path or not os.path.exists(cfg_path):
            continue
        try:
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
            logger.warning("config.toml の読み込みに失敗いたしました (%s): %s", cfg_path, exc)

    env_url = (os.environ.get("FLAC_DB_URL") or os.environ.get("DATABASE_URL") or "").strip()
    if env_url:
        return {"dsn": env_url}
    return {}


def check_v2_schema(cur: Any) -> list[str]:
    """raw.library_flac に v2 必須カラムが揃っているか検査し、不足カラム名一覧を返しますわ。"""
    cur.execute(
        """
        SELECT column_name
        FROM information_schema.columns
        WHERE table_schema = 'raw' AND table_name = 'library_flac'
        """
    )
    existing = {str(r[0]) for r in cur.fetchall()}
    return [col for col in V2_REQUIRED_SCHEMA_COLUMNS if col not in existing]


def apply_v2_schema_migration(conn: Any, sql_path: str | None = None) -> None:
    """sql/migration_v2.sql をトランザクション内で適用いたしますわ。"""
    target_sql = sql_path or os.path.join(PROJECT_ROOT, "sql", "migration_v2.sql")
    if not os.path.exists(target_sql):
        raise FileNotFoundError(f"migration_v2.sql が見つかりませんわ: {target_sql}")
    with open(target_sql, encoding="utf-8") as f:
        migration_sql = f.read()
    with conn.cursor() as cur:
        cur.execute(migration_sql)
    conn.commit()
    logger.info("v2 スキーママイグレーション (%s) を適用いたしましたわ！", target_sql)


def run_migration(args: argparse.Namespace) -> int:
    import psycopg2
    import psycopg2.extras

    conn_kwargs = get_db_connection_kwargs(args.config)
    if not conn_kwargs:
        raise RuntimeError("PostgreSQL connection settings not found in config.toml or environment")

    require_demucs = args.mode == "orchestrator"
    conn = psycopg2.connect(**conn_kwargs)
    conn.autocommit = False
    try:
        try:
            with conn.cursor() as cur:
                missing_cols = check_v2_schema(cur)
            if missing_cols:
                if args.apply_schema and not args.dry_run:
                    apply_v2_schema_migration(conn)
                elif args.dry_run:
                    logger.warning("[DRY-RUN] raw.library_flac に v2 カラム不足を検出いたしました: %s", missing_cols)
                    if "track_number" in missing_cols:
                        logger.warning("[DRY-RUN] --apply-schema 実行前はクエリ対象の track_number が存在しないためここで終了いたします。")
                        return 0
                else:
                    raise RuntimeError(
                        f"raw.library_flac lacks v2 columns {missing_cols}; re-run with --apply-schema or apply sql/migration_v2.sql"
                    )

            sql, params = build_v1_select_sql(
                ids=args.id,
                filepath=args.filepath,
                require_demucs=require_demucs,
                limit=args.limit,
            )
            with conn.cursor(cursor_factory=psycopg2.extras.DictCursor) as cur:
                cur.execute(sql, params)
                rows = [dict(r) for r in cur.fetchall()]
        finally:
            if args.mode == "orchestrator":
                conn.close()

        rows = [r for r in rows if not is_v2_feature_complete_pure(r.get("features"), require_demucs=require_demucs)]
        logger.info("検出された v1/未完了レコード: %d 件 (mode=%s)", len(rows), args.mode)
        if not rows:
            return 0

        if args.mode == "orchestrator":
            grouped = group_rows_by_filepath_pure(rows)
            logger.info("一意ファイル数: %d 件", len(grouped))
            if not args.dry_run and not os.path.exists(args.orchestrator_exe):
                raise FileNotFoundError(f"orchestrator executable not found: {args.orchestrator_exe}")

            succeeded = 0
            failed = 0
            for idx, (fp, file_rows) in enumerate(grouped, 1):
                if not os.path.exists(fp):
                    logger.warning("[%d/%d] 実ファイルが存在しないためスキップいたします: %s", idx, len(grouped), fp)
                    continue
                cmd = build_orchestrator_command_pure(
                    args.orchestrator_exe,
                    fp,
                    config_path=args.config,
                    force=True,
                )
                if args.dry_run:
                    logger.info("[DRY-RUN] [%d/%d] (%d tracks) %s", idx, len(grouped), len(file_rows), " ".join(cmd))
                    succeeded += 1
                    continue
                logger.info("[%d/%d] 再解析実行 (%d tracks): %s", idx, len(grouped), len(file_rows), fp)
                proc = subprocess.run(cmd, cwd=PROJECT_ROOT, check=False)
                if proc.returncode == 0:
                    succeeded += 1
                else:
                    failed += 1
                    logger.error("[%d/%d] orchestrator が失敗いたしました (exit=%d): %s", idx, len(grouped), proc.returncode, fp)
                    if args.stop_on_error:
                        return 1
            logger.info("orchestrator モード完了: 成功=%d, 失敗=%d", succeeded, failed)
            return 1 if failed > 0 else 0

        # mix-only mode: reuse zig.migrate_features helpers lazily
        from zig.migrate_features import (
            _decode_track_audio,
            compute_additional_features_pure,
            merge_jsonb_features_pure,
        )

        updated_count = 0
        failed = 0
        with conn.cursor() as cur:
            for idx, row in enumerate(rows, 1):
                row_id = int(row["id"])
                audio_hash = str(row["audio_hash"])
                fp = str(row["filepath"])
                track_number = row["track_number"]
                existing_feat = row.get("features") or {}
                if not os.path.exists(fp):
                    logger.warning("[%d/%d] 実ファイルが存在しないためスキップいたします: %s", idx, len(rows), fp)
                    continue
                if args.dry_run:
                    logger.info("[DRY-RUN] [%d/%d] id=%d track=%s (%s)", idx, len(rows), row_id, track_number, fp)
                    continue
                try:
                    y, sr = _decode_track_audio(fp, track_number)
                    add_feat = compute_additional_features_pure(y, sr)
                    merged_feat = merge_jsonb_features_pure(existing_feat, add_feat)
                    cur.execute("SAVEPOINT sp_mix_row")
                    cur.execute(
                        "UPDATE raw.library_flac SET features = %s::jsonb, analyzed_at = CURRENT_TIMESTAMP WHERE id = %s AND audio_hash = %s",
                        (json.dumps(merged_feat), row_id, audio_hash),
                    )
                    cur.execute("RELEASE SAVEPOINT sp_mix_row")
                    updated_count += cur.rowcount
                    if updated_count > 0 and updated_count % args.batch_size == 0:
                        conn.commit()
                        logger.info("[Commit] %d 件を確定いたしましたわ。", updated_count)
                except Exception as exc:
                    failed += 1
                    conn.rollback()
                    logger.exception("[%d/%d] id=%d の mix-only マイグレーションに失敗いたしました: %s", idx, len(rows), row_id, exc)
                    if args.stop_on_error:
                        return 1
        if not args.dry_run and updated_count % args.batch_size != 0:
            conn.commit()

        logger.info("mix-only モード完了: 更新=%d, 失敗=%d", updated_count, failed)
        return 1 if failed > 0 else 0
    except Exception:
        if args.mode != "orchestrator":
            conn.rollback()
        raise
    finally:
        if args.mode != "orchestrator":
            conn.close()


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="v1 -> v2 migration maintenance jig for PostgreSQL raw.library_flac & FLAC pipeline."
    )
    parser.add_argument(
        "--mode",
        choices=("orchestrator", "mix-only"),
        default="orchestrator",
        help="orchestrator: full v2 Demucs+stem+feature re-analysis via orchestrator.exe; mix-only: fast mix extended plugin backfill.",
    )
    parser.add_argument("--dry-run", action="store_true", help="Inspect v1 targets without mutating DB or running orchestrator.")
    parser.add_argument("--apply-schema", action="store_true", help="Apply sql/migration_v2.sql if v2 schema columns are missing.")
    parser.add_argument("--id", type=int, nargs="*", help="Filter specific raw.library_flac IDs.")
    parser.add_argument("--filepath", type=str, default=None, help="Filter a specific filepath.")
    parser.add_argument("--limit", type=int, default=0, help="Maximum candidate rows (0 = unlimited).")
    parser.add_argument("--batch-size", type=int, default=50, help="DB commit batch size for mix-only mode.")
    parser.add_argument("--config", type=str, default=None, help="Path to config.toml.")
    parser.add_argument(
        "--orchestrator-exe",
        type=str,
        default=os.path.join(PROJECT_ROOT, "orchestrator", "orchestrator.exe"),
        help="Path to compiled orchestrator binary for orchestrator mode.",
    )
    parser.add_argument("--stop-on-error", action="store_true", help="Abort immediately on first failure.")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    setup_logger()
    args = parse_args(argv)
    return run_migration(args)


if __name__ == "__main__":
    raise SystemExit(main())
