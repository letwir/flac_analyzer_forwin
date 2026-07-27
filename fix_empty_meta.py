"""
fix_empty_meta.py
=================
PostgreSQL の raw.library_flac テーブル内において、meta カラムが空 ('{}'::jsonb) または NULL になっている
過去のレコードを一括スキャンし、FLAC ファイルの VorbisComment からメタデータタグを再読み込みして更新する修正バッチスクリプトですの。

使い方:
    python fix_empty_meta.py [--dry-run] [--batch-size 1000] [--limit 0]
"""

import os
import sys
import argparse
import logging
import tomllib
import psycopg2
import psycopg2.extras
from mutagen.flac import FLAC

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s - [%(levelname)s] - %(message)s",
        handlers=[logging.StreamHandler(sys.stderr)]
    )

def get_db_url():
    config_path = os.path.join(os.path.dirname(__file__), "config.toml")
    db_url = None
    if os.path.exists(config_path):
        try:
            with open(config_path, "rb") as f:
                config = tomllib.load(f)
            db_url = config.get("database", {}).get("url", "")
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

    parser = argparse.ArgumentParser(description="Fix empty meta JSONB records in PostgreSQL raw.library_flac")
    parser.add_argument("--dry-run", action="store_true", help="実際の DB 更新を行わずに修正対象件数とプレビューのみ表示いたしますわ")
    parser.add_argument("--batch-size", type=int, default=1000, help="コミット単位の件数 (デフォルト: 1000)")
    parser.add_argument("--limit", type=int, default=0, help="処理する最大レコード数 (0 の場合は全件)")
    args = parser.parse_args()

    db_url = get_db_url()
    if not db_url:
        logger.error("DB URL が取得できませんでしたわ！ config.toml または環境変数 (FLAC_DB_URL/DATABASE_URL) をご確認くださいませ。")
        sys.exit(1)

    logger.info("PostgreSQL へ接続しておりますわ...")
    try:
        conn = psycopg2.connect(db_url)
        cur = conn.cursor(cursor_factory=psycopg2.extras.DictCursor)
    except Exception as e:
        logger.exception(f"PostgreSQL への接続に失敗いたしましたわ: {e}")
        sys.exit(1)

    # meta が空 ('{}'::jsonb) または NULL のレコードを抽出
    query = """
        SELECT id, audio_hash, filepath, title, artist, album, album_artist
        FROM raw.library_flac
        WHERE meta IS NULL OR meta = '{}'::jsonb
        ORDER BY id ASC
    """
    if args.limit > 0:
        query += f" LIMIT {args.limit}"

    logger.info("meta カラムが空の対象レコードを検索しておりますわ...")
    cur.execute(query)
    rows = cur.fetchall()
    total_targets = len(rows)

    logger.info(f"対象レコード数: {total_targets} 件 を発見いたしましたわ！")
    if total_targets == 0:
        logger.info("更新の必要なレコードは存在いたしませんわ。お見事でございますの！")
        cur.close()
        conn.close()
        sys.exit(0)

    if args.dry_run:
        logger.info("【DRY-RUN モード】実際の更新は行いませんわ。先頭5件のプレビューを表示いたしますの:")
        for r in rows[:5]:
            filepath = r["filepath"]
            exists = os.path.exists(filepath)
            tag_count = 0
            if exists:
                try:
                    tags = extract_vorbis_meta(filepath)
                    tag_count = len(tags)
                except Exception as e:
                    tag_count = f"エラー({e})"
            logger.info(f"  - ID: {r['id']} | Hash: {r['audio_hash']} | File: {filepath} (存在: {exists}, 検出タグ数: {tag_count})")
        logger.info("DRY-RUN 完了いたしましたわ。")
        cur.close()
        conn.close()
        sys.exit(0)

    success_count = 0
    missing_file_count = 0
    error_count = 0

    update_query = """
        UPDATE raw.library_flac
        SET meta = %s
        WHERE audio_hash = %s
    """

    logger.info("メタデータの再抽出と PostgreSQL 更新バッチを開始いたしますわ！")
    for i, row in enumerate(rows, 1):
        audio_hash = row["audio_hash"]
        filepath = row["filepath"]

        if not os.path.exists(filepath):
            logger.warning(f"[{i}/{total_targets}] ファイルが存在いたしませんわ: {filepath}")
            missing_file_count += 1
            continue

        try:
            meta_dict = extract_vorbis_meta(filepath)
            if not meta_dict:
                logger.warning(f"[{i}/{total_targets}] タグが空でございますわ: {filepath}")

            cur.execute(update_query, (psycopg2.extras.Json(meta_dict), audio_hash))
            success_count += 1

            if i % args.batch_size == 0:
                conn.commit()
                logger.info(f"進捗: [{i}/{total_targets}] 件の更新をコミットいたしましたわ ✨")

        except Exception as e:
            logger.error(f"[{i}/{total_targets}] ID {row['id']} のメタデータ更新中にエラーが発生いたしましたわ: {e}")
            error_count += 1

    # 残りのトランザクションをコミット
    conn.commit()
    cur.close()
    conn.close()

    logger.info("==========================================================")
    logger.info(f"🎉 バッチ更新が無事に完了いたしましたわ！")
    logger.info(f"  - 成功: {success_count} 件")
    logger.info(f"  - ファイル不在スキップ: {missing_file_count} 件")
    logger.info(f"  - エラー: {error_count} 件")
    logger.info("==========================================================")

if __name__ == "__main__":
    main()
