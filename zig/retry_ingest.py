"""
zig/retry_ingest.py
===================
PostgreSQL への送信失敗時に SQLite DLQ (send_failed.db) に退避された
未送信解析ペイロードを PostgreSQL (raw.library_flac) へ再送・リカバリする治具スクリプトですわ！

Orchestrator の起動時および定期実行（10分間隔）から自動呼び出しされます。
"""

import os
import sys
import sqlite3
import psycopg2
import psycopg2.extras
import json
import logging
import argparse
import tomllib

# 親ディレクトリを sys.path に追加してプロジェクト内モジュールを安全にロード
PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

def setup_logger():
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s - [%(levelname)s] [DLQRetry] %(message)s",
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
    if not db_url:
        db_url = "postgresql://postgres:postgres@localhost:5432/postgres"
    return db_url

def main():
    setup_logger()
    parser = argparse.ArgumentParser(description="Retry Failed Ingestions from DLQ SQLite.")
    parser.add_argument("--dlq-db", default="send_failed.db", help="Path to the DLQ SQLite database")
    args = parser.parse_args()

    # Search for DLQ db in current dir and PROJECT_ROOT
    dlq_db_path = args.dlq_db
    if not os.path.exists(dlq_db_path):
        candidate = os.path.join(PROJECT_ROOT, args.dlq_db)
        if os.path.exists(candidate):
            dlq_db_path = candidate
        else:
            dlq_db_path = candidate
    
    if not os.path.exists(dlq_db_path):
        logging.info("DLQデータベースが見つかりませんわ。リトライするタスクはございませんの。")
        sys.exit(0)

    try:
        dlq_conn = sqlite3.connect(dlq_db_path)
        dlq_conn.row_factory = sqlite3.Row
        dlq_cur = dlq_conn.cursor()
        
        dlq_cur.execute("SELECT name FROM sqlite_master WHERE type='table' AND name='failed_payloads'")
        if not dlq_cur.fetchone():
            logging.info("DLQデータベース内に 'failed_payloads' テーブルが存在いたしませんわ。")
            sys.exit(0)
            
        dlq_cur.execute("SELECT * FROM failed_payloads")
        rows = dlq_cur.fetchall()
        
        if not rows:
            logging.info("DLQは空でございますわ。リトライするタスクはございませんの。")
            sys.exit(0)
            
        logging.info(f"DLQ内に {len(rows)} 件の未処理レコードを発見いたしましたわ！再送信を開始いたしますわ...")
        
    except Exception as e:
        logging.error(f"DLQデータベースからの読み込みに失敗いたしましたわ: {e}")
        sys.exit(1)

    db_url = get_db_url()
    
    try:
        pg_conn = psycopg2.connect(db_url)
        pg_cur = pg_conn.cursor()
    except Exception as e:
        logging.error(f"PostgreSQL への接続に失敗いたしましたわ: {e}")
        sys.exit(1)

    success_count = 0
    fail_count = 0

    for row in rows:
        audio_hash = row["audio_hash"]
        try:
            query = """
                INSERT INTO raw.library_flac (
                    audio_hash, filepath, filename, track_number, album_artist, album, artist, title, meta, features, predictions, analyzed_at
                ) VALUES (
                    %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, CURRENT_TIMESTAMP
                )
                ON CONFLICT (audio_hash) DO UPDATE SET
                    filepath = EXCLUDED.filepath,
                    filename = EXCLUDED.filename,
                    track_number = EXCLUDED.track_number,
                    album_artist = EXCLUDED.album_artist,
                    album = EXCLUDED.album,
                    artist = EXCLUDED.artist,
                    title = EXCLUDED.title,
                    meta = EXCLUDED.meta,
                    features = EXCLUDED.features,
                    predictions = EXCLUDED.predictions,
                    analyzed_at = EXCLUDED.analyzed_at;
            """
            
            pg_cur.execute(query, (
                audio_hash,
                row["filepath"],
                row["filename"],
                row["track_number"],
                (row["album_artist"] or "")[:255],
                (row["album"] or "")[:255],
                (row["artist"] or "")[:255],
                (row["title"] or "")[:255],
                psycopg2.extras.Json(json.loads(row["meta"]) if row["meta"] else {}),
                psycopg2.extras.Json(json.loads(row["features"]) if row["features"] else {}),
                psycopg2.extras.Json(json.loads(row["predictions"]) if row["predictions"] else {})
            ))
            pg_conn.commit()
            
            dlq_cur.execute("DELETE FROM failed_payloads WHERE audio_hash = ?", (audio_hash,))
            dlq_conn.commit()
            
            logging.info(f"リトライに成功し、PostgreSQL への登録が完了いたしましたわ: {audio_hash}")
            success_count += 1
            
        except Exception as e:
            pg_conn.rollback()
            logging.error(f"{audio_hash} のリトライに失敗いたしましたわ: {e}")
            fail_count += 1

    pg_cur.close()
    pg_conn.close()
    dlq_cur.close()
    dlq_conn.close()

    logging.info(f"リトライ処理が無事に完了いたしましたわ！ 成功: {success_count}件, 失敗: {fail_count}件")

if __name__ == "__main__":
    main()
