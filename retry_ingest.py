import os
import sys
import sqlite3
import psycopg2
import psycopg2.extras
import json
import logging
import argparse

logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(levelname)s - %(message)s')

def get_db_url():
    db_url = None
    try:
        config_path = os.path.join(os.path.dirname(__file__), "config.toml")
        with open(config_path, "rb") as f:
            import tomllib
            config = tomllib.load(f)
        db_url = config.get("database", {}).get("url", "")
    except Exception:
        pass
    if not db_url:
        db_url = os.environ.get("FLAC_DB_URL")
    if not db_url:
        db_url = "postgresql://postgres:postgres@localhost:5432/postgres"
    return db_url

def main():
    parser = argparse.ArgumentParser(description="Retry Failed Ingestions from DLQ SQLite.")
    parser.add_argument("--dlq-db", default="send_failed.db", help="Path to the DLQ SQLite database")
    args = parser.parse_args()

    dlq_db_path = os.path.join(os.path.dirname(__file__), args.dlq_db)
    
    if not os.path.exists(dlq_db_path):
        logging.info("No DLQ database found. Nothing to retry.")
        sys.exit(0)

    try:
        dlq_conn = sqlite3.connect(dlq_db_path)
        dlq_conn.row_factory = sqlite3.Row
        dlq_cur = dlq_conn.cursor()
        
        dlq_cur.execute("SELECT name FROM sqlite_master WHERE type='table' AND name='failed_payloads'")
        if not dlq_cur.fetchone():
            logging.info("Table 'failed_payloads' does not exist in DLQ database. Nothing to retry.")
            sys.exit(0)
            
        dlq_cur.execute("SELECT * FROM failed_payloads")
        rows = dlq_cur.fetchall()
        
        if not rows:
            logging.info("DLQ is empty. Nothing to retry.")
            sys.exit(0)
            
        logging.info(f"Found {len(rows)} records in DLQ. Attempting retry...")
        
    except Exception as e:
        logging.error(f"Failed to read from DLQ database: {e}")
        sys.exit(1)

    db_url = get_db_url()
    
    try:
        pg_conn = psycopg2.connect(db_url)
        pg_cur = pg_conn.cursor()
    except Exception as e:
        logging.error(f"Failed to connect to PostgreSQL: {e}")
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
                row["album_artist"],
                row["album"],
                row["artist"],
                row["title"],
                psycopg2.extras.Json(json.loads(row["meta"]) if row["meta"] else {}),
                psycopg2.extras.Json(json.loads(row["features"]) if row["features"] else {}),
                psycopg2.extras.Json(json.loads(row["predictions"]) if row["predictions"] else {})
            ))
            pg_conn.commit()
            
            dlq_cur.execute("DELETE FROM failed_payloads WHERE audio_hash = ?", (audio_hash,))
            dlq_conn.commit()
            
            logging.info(f"Successfully retried and inserted {audio_hash}.")
            success_count += 1
            
        except Exception as e:
            pg_conn.rollback()
            logging.error(f"Retry failed for {audio_hash}: {e}")
            fail_count += 1

    pg_cur.close()
    pg_conn.close()
    dlq_cur.close()
    dlq_conn.close()

    logging.info(f"Retry complete. Success: {success_count}, Failed: {fail_count}")

if __name__ == "__main__":
    main()
