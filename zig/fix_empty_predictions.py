"""
zig/fix_empty_predictions.py
============================
PostgreSQL の raw.library_flac テーブルにおいて、features は揃っているのに
predictions カラムだけが空 ('{}'::jsonb) または NULL のレコードを抽出し、
対象トラック（単曲 FLAC / CUE 埋め込みマルチトラック FLAC 両対応）のみを
デコードして Essentia ONNX 推論を実行し、predictions カラムだけを追記修復する治具ですわ。

使い方:
    python zig/fix_empty_predictions.py --id 83484 --dry-run
    python zig/fix_empty_predictions.py --id 83484
    python zig/fix_empty_predictions.py --batch-size 50 --limit 0
"""

from __future__ import annotations

import argparse
import logging
import os
import sys
import tomllib
from typing import Any

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

import flac_decode
import models
from analyzer.essentia_dsp import extract_mel_patches, run_essentia_serialized

GUARDED_UPDATE_SQL = """
UPDATE raw.library_flac
SET predictions = %s
WHERE id = %s
  AND audio_hash = %s
  AND (predictions IS NULL OR predictions = '{}'::jsonb OR predictions = 'null'::jsonb)
""".strip()


def setup_logger() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s - [%(levelname)s] [FixPredZig] - %(message)s",
        handlers=[logging.StreamHandler(sys.stderr)],
    )


def build_select_query(
    ids: list[int] | None = None,
    filepath: str | None = None,
    include_empty_features: bool = False,
    limit: int = 0,
) -> tuple[str, list[Any]]:
    """空 predictions 修復対象のレコードを取得するパラメータ化 SQL を構築しますわ。"""
    conditions = [
        "(predictions IS NULL OR predictions = '{}'::jsonb OR predictions = 'null'::jsonb)",
    ]
    params: list[Any] = []

    if not include_empty_features:
        conditions.append(
            "(features IS NOT NULL AND features <> '{}'::jsonb AND features <> 'null'::jsonb)"
        )

    if ids:
        conditions.append("id = ANY(%s)")
        params.append(list(ids))

    if filepath:
        conditions.append("filepath = %s")
        params.append(filepath)

    sql = (
        "SELECT id, audio_hash, filepath, track_number, title, artist, album\n"
        "FROM raw.library_flac\n"
        f"WHERE {' AND '.join(conditions)}\n"
        "ORDER BY filepath, track_number, id"
    )
    if limit > 0:
        sql += "\nLIMIT %s"
        params.append(limit)

    return sql, params


def select_track_slice(
    handle: flac_decode.FlacHandle,
    track_number: int | None,
) -> flac_decode.TrackSlice:
    """FlacHandle から DB レコードの track_number に対応するスライスを特定しますわ。"""
    if not handle.slices:
        raise ValueError(f"No track slices found in {handle.filepath}")

    if len(handle.slices) == 1 and (
        track_number is None
        or track_number <= 1
        or handle.slices[0].track_number == track_number
    ):
        return handle.slices[0]

    matches = [s for s in handle.slices if s.track_number == track_number]
    if len(matches) != 1:
        raise ValueError(
            f"track_number {track_number} not uniquely matched in {handle.filepath} "
            f"(matched={len(matches)}, total_slices={len(handle.slices)})"
        )
    return matches[0]


def get_db_url(config_path: str | None = None) -> str:
    """設定ファイルまたは環境変数から PostgreSQL 接続 URL を解決しますわ。"""
    candidates = [config_path] if config_path else [
        os.path.join(PROJECT_ROOT, "config.toml"),
        os.path.join(os.path.dirname(__file__), "config.toml"),
        "config.toml",
    ]
    for path in candidates:
        if not path or not os.path.exists(path):
            continue
        try:
            with open(path, "rb") as f:
                config = tomllib.load(f)
            db_url = str(config.get("database", {}).get("url", "") or "").strip()
            if db_url:
                return db_url
        except Exception as exc:
            logging.warning("config.toml (%s) からの DB URL 読込に失敗いたしましたわ: %s", path, exc)

    return (os.environ.get("FLAC_DB_URL") or os.environ.get("DATABASE_URL") or "").strip()


def extract_track_predictions(
    filepath: str,
    track_number: int | None,
    essentia_models: dict[str, Any],
    handle_cache: dict[str, flac_decode.FlacHandle],
) -> dict[str, float]:
    """対象トラックのみデコードして Essentia predictions を算出しますわ。"""
    if filepath not in handle_cache:
        # 対象一覧は filepath 順のため、直前のアルバム用キャッシュのみ解放して省メモリ化
        handle_cache.clear()
        handle_cache[filepath] = flac_decode.build_flac_handle(filepath)

    handle = handle_cache[filepath]
    slc = select_track_slice(handle, track_number)
    audio_44100, _md5 = flac_decode.process_slice_with_seq_safety(
        filepath,
        slc.start_sample,
        slc.end_sample,
        handle.sample_rate,
        handle.channels,
    )
    if audio_44100.size == 0:
        raise RuntimeError(f"Decoded empty audio slice: {filepath} (track={track_number})")

    patches = extract_mel_patches(audio_44100, 44100, n_patches=64)
    preds = run_essentia_serialized(patches, essentia_models)
    if not preds:
        raise RuntimeError(f"Essentia returned empty predictions: {filepath} (track={track_number})")
    return preds


def _init_essentia_or_exit(use_cpu: bool, logger: logging.Logger) -> dict[str, Any]:
    models_dir = os.path.join(PROJECT_ROOT, "models")
    if not os.path.exists(models_dir):
        logger.error("モデルディレクトリが見つかりませんわ: %s", models_dir)
        sys.exit(1)

    providers = ["CPUExecutionProvider"] if use_cpu else None
    essentia_models = models.init_worker_onnx(models_dir, providers_override=providers)
    init_err = models.GLOBAL_ONNX_SESSIONS.get("init_error")
    effnet = models.GLOBAL_ONNX_SESSIONS.get("effnet")
    if effnet is None or init_err is not None or not essentia_models:
        logger.error(
            "Essentia ONNX の初期化が不完全でございますわ (effnet=%s, classifiers=%d, init_error=%s)",
            effnet is not None,
            len(essentia_models),
            init_err,
        )
        sys.exit(1)
    return essentia_models


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Backfill empty predictions in raw.library_flac using Essentia ONNX only."
    )
    parser.add_argument("--config", default=None, help="Path to config.toml")
    parser.add_argument("--dry-run", action="store_true", help="Run inference without writing to PostgreSQL.")
    parser.add_argument("--batch-size", type=int, default=50, help="Commit every N updated records.")
    parser.add_argument("--limit", type=int, default=0, help="Max records to process (0 = all).")
    parser.add_argument("--id", type=int, action="append", dest="ids", help="Specific record id to repair (repeatable).")
    parser.add_argument("--file", dest="filepath", default=None, help="Filter by exact filepath.")
    parser.add_argument(
        "--include-empty-features",
        action="store_true",
        help="Also include rows where features is empty/null.",
    )
    parser.add_argument("--cpu", action="store_true", help="Force CPUExecutionProvider for ONNX.")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    setup_logger()
    logger = logging.getLogger("FixPredictionsBatch")
    args = parse_args(argv)

    if args.batch_size <= 0:
        logger.error("--batch-size は 1 以上を指定してくださいませ。")
        return 1

    db_url = get_db_url(args.config)
    if not db_url:
        logger.error("データベース URL が未設定でございますわ (config.toml または FLAC_DB_URL)。")
        return 1

    import psycopg2
    import psycopg2.extras

    essentia_models = _init_essentia_or_exit(args.cpu, logger)

    try:
        conn = psycopg2.connect(db_url)
        conn.autocommit = False
    except Exception as exc:
        logger.error("PostgreSQL 接続エラー: %s", exc)
        return 1

    handle_cache: dict[str, flac_decode.FlacHandle] = {}
    updated_count = 0
    skipped_count = 0
    error_count = 0

    try:
        select_sql, select_params = build_select_query(
            ids=args.ids,
            filepath=args.filepath,
            include_empty_features=args.include_empty_features,
            limit=args.limit,
        )
        with conn.cursor(cursor_factory=psycopg2.extras.RealDictCursor) as cur:
            cur.execute(select_sql, select_params)
            rows = cur.fetchall()

        total_target = len(rows)
        logger.info("空 predictions 修復対象レコード数: %d 件", total_target)
        if total_target == 0:
            return 0

        with conn.cursor() as cur:
            for idx, row in enumerate(rows, 1):
                rec_id = row["id"]
                audio_hash = row["audio_hash"]
                filepath = row["filepath"]
                track_number = row["track_number"]

                if not filepath or not os.path.exists(filepath):
                    logger.warning("[%d/%d] ファイルが存在いたしません (id=%s): %s", idx, total_target, rec_id, filepath)
                    skipped_count += 1
                    continue
                if not audio_hash:
                    logger.warning("[%d/%d] audio_hash が空のためスキップいたします (id=%s)", idx, total_target, rec_id)
                    skipped_count += 1
                    continue

                try:
                    preds = extract_track_predictions(filepath, track_number, essentia_models, handle_cache)
                    if args.dry_run:
                        logger.info(
                            "[%d/%d] [DRY-RUN] 推論成功 (id=%s, track=%s, keys=%d): %s",
                            idx, total_target, rec_id, track_number, len(preds), filepath,
                        )
                        updated_count += 1
                        continue

                    cur.execute(
                        GUARDED_UPDATE_SQL,
                        (psycopg2.extras.Json(preds), rec_id, audio_hash),
                    )
                    if cur.rowcount == 1:
                        updated_count += 1
                    else:
                        logger.info(
                            "[%d/%d] 既に更新済みまたは条件不一致のためスキップ (id=%s)",
                            idx, total_target, rec_id,
                        )
                        skipped_count += 1

                    if updated_count > 0 and updated_count % args.batch_size == 0:
                        conn.commit()
                        logger.info("中間コミット完了: %d 件更新", updated_count)
                except Exception as exc:
                    logger.error(
                        "[%d/%d] 修復エラー (id=%s, track=%s, file=%s): %s",
                        idx, total_target, rec_id, track_number, filepath, exc,
                    )
                    error_count += 1

            if not args.dry_run:
                conn.commit()
                logger.info(
                    "修復バッチ完了: updated=%d, skipped=%d, errors=%d",
                    updated_count, skipped_count, error_count,
                )
            else:
                logger.info(
                    "[DRY-RUN] 完了: would_update=%d, skipped=%d, errors=%d",
                    updated_count, skipped_count, error_count,
                )
    except Exception as exc:
        logger.error("バッチ処理中に致命的なエラーが発生いたしましたわ: %s", exc)
        conn.rollback()
        return 1
    finally:
        handle_cache.clear()
        conn.close()

    return 0 if error_count == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
