"""
zig/fix_empty_meta.py
=====================
PostgreSQL の raw.library_flac テーブル内において、meta カラムが空 ('{}'::jsonb) または NULL になっている
過去のレコードを一括スキャンし、FLAC ファイルの VorbisComment からメタデータタグを再読み込みして更新する修正バッチスクリプトですの。

使い方:
    python zig/fix_empty_meta.py [--dry-run] [--batch-size 1000] [--limit 0]
"""

import os
import sys
import argparse
import logging
import tomllib
import psycopg2
import psycopg2.extras
from mutagen.flac import FLAC

# 親ディレクトリを sys.path に追加してプロジェクト内モジュールを安全にロード
PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s - [%(levelname)s] [FixMetaZig] - %(message)s",
        handlers=[logging.StreamHandler(sys.stderr)]
    )

def get_db_url():
    candidates = [
        os.path.join(PROJECT_ROOT, "config.toml"),
        os.path.join(os.path.dirname(__file__), "config.toml"),
        "config.toml"
    ]
    db_url = None
    for config_path in candidates:
        if os.path.exists(config_path):
            try:
                with open(config_path, "rb") as f:
                    config = tomllib.load(f)
                db_url = config.get("database", {}).get("url", "")
                if db_url:
                    break
            except Exception as e:
                logging.warning(f"config.toml からの DB URL 読込に失敗いたしましたわ: {e}")
            
    if not db_url:
        db_url = os.environ.get("FLAC_DB_URL") or os.environ.get("DATABASE_URL")
    return db_url

def extract_vorbis_meta(filepath: str) -> dict:
    """mutagen を使って FLAC ファイルから VorbisComment タグ全件を抽出いたしますの"""
    audio = FLAC(filepath)
    vorbis_meta = {}
    for k, v in audio.items():
        val_list = [str(x) for x in v]
        key_lower = k.lower()
        if len(val_list) == 1:
            vorbis_meta[key_lower] = val_list[0]
        elif len(val_list) == 0:
            vorbis_meta[key_lower] = ""
        else:
            vorbis_meta[key_lower] = val_list
    return vorbis_meta

def main():
    setup_logger()
    logger = logging.getLogger("FixMetaBatch")

    parser = argparse.ArgumentParser(description="Fix empty or null meta in PostgreSQL raw.library_flac from FLAC files.")
    parser.add_argument("--dry-run", action="store_true", help="Do not commit changes to PostgreSQL.")
    parser.add_argument("--batch-size", type=int, default=500, help="Number of records to update per transaction commit.")
    parser.add_argument("--limit", type=int, default=0, help="Limit number of rows to process (0 = all).")
    args = parser.parse_args()

    db_url = get_db_url()
    if not db_url:
        logger.error("データベースURLが設定されておりませんわ！ config.toml または FLAC_DB_URL をご確認くださいませ。")
        sys.exit(1)

    logger.info("PostgreSQL に接続して空メタデータレコードを検索いたしますわ...")
    try:
        conn = psycopg2.connect(db_url)
        conn.autocommit = False
    except Exception as e:
        logger.error(f"PostgreSQL 接続エラー: {e}")
        sys.exit(1)

    try:
        with conn.cursor(cursor_factory=psycopg2.extras.RealDictCursor) as cur:
            query = """
                SELECT audio_hash, filepath, filename, track_number, title, artist, album 
                FROM raw.library_flac 
                WHERE meta IS NULL OR meta = '{}'::jsonb OR meta = 'null'::jsonb
                ORDER BY analyzed_at DESC NULLS LAST
            """
            if args.limit > 0:
                query += f" LIMIT {args.limit}"

            cur.execute(query)
            rows = cur.fetchall()

        total_target = len(rows)
        logger.info(f"空 meta 対象レコード件数: {total_target} 件")

        if total_target == 0:
            logger.info("修正対象のレコードはございませんでしたわ。処理を終了いたします。")
            conn.close()
            return

        updated_count = 0
        skipped_count = 0
        error_count = 0

        update_query = """
            UPDATE raw.library_flac 
            SET meta = %s
            WHERE audio_hash = %s
        """

        with conn.cursor() as cur:
            for idx, row in enumerate(rows, 1):
                audio_hash = row["audio_hash"]
                filepath = row["filepath"]

                if not filepath or not os.path.exists(filepath):
                    logger.warning(f"[{idx}/{total_target}] FLACファイルが存在いたしませんの: {filepath} (Hash: {audio_hash})")
                    skipped_count += 1
                    continue

                try:
                    meta_dict = extract_vorbis_meta(filepath)
                    if not meta_dict:
                        logger.warning(f"[{idx}/{total_target}] タグ情報が空でございました: {filepath}")
                        skipped_count += 1
                        continue

                    if not args.dry_run:
                        cur.execute(update_query, (psycopg2.extras.Json(meta_dict), audio_hash))
                    
                    updated_count += 1

                    if idx % 100 == 0 or idx == total_target:
                        logger.info(f"進捗: {idx}/{total_target} 完了 (更新成功: {updated_count}, スキップ: {skipped_count}, エラー: {error_count})")

                    if not args.dry_run and updated_count > 0 and updated_count % args.batch_size == 0:
                        conn.commit()
                        logger.info(f"中間コミット完了: {updated_count} 件")

                except Exception as e:
                    logger.error(f"[{idx}/{total_target}] タグ抽出/更新エラー ({filepath}): {e}")
                    error_count += 1

            if not args.dry_run:
                conn.commit()
                logger.info(f"最終コミット完了！ 合計更新: {updated_count} 件")
            else:
                logger.info(f"[DRY-RUN] 完了。更新予定件数: {updated_count} 件 (コミットはスキップされました)")

    except Exception as e:
        logger.error(f"バッチ処理中に予期せぬエラーが発生いたしましたわ: {e}")
        conn.rollback()
        sys.exit(1)
    finally:
        conn.close()

if __name__ == "__main__":
    main()
