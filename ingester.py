import sys
import json
import os
import argparse
import psycopg2
import psycopg2.extras
import logging
import tomllib
from mutagen.flac import FLAC

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--flac-path", required=True)
    parser.add_argument("--json-path", required=True)
    parser.add_argument("--predictions-json-path", required=False, default="")
    parser.add_argument("--tensor-json-path", required=False, default="")
    parser.add_argument("--track-hash", required=True)
    parser.add_argument("--track-number", type=int, default=0)
    parser.add_argument("--title", type=str, default="")
    parser.add_argument("--artist", type=str, default="")
    parser.add_argument("--album", type=str, default="")
    parser.add_argument("--album-artist", type=str, default="")
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO, stream=sys.stdout, format='%(asctime)s - %(levelname)s - %(message)s')

    if not os.path.exists(args.json_path):
        logging.error(f"JSON path does not exist: {args.json_path}")
        sys.exit(1)

    try:
        with open(args.json_path, "r", encoding="utf-8") as f:
            json_data = json.load(f)
    except Exception as e:
        logging.exception("Failed to parse JSON")
        sys.exit(1)

    features = json_data.get("features", {})
    if not features:
        logging.warning("No features found in JSON.")

    predictions = {}
    if args.predictions_json_path and os.path.exists(args.predictions_json_path):
        try:
            with open(args.predictions_json_path, "r", encoding="utf-8") as f:
                pred_data = json.load(f)
                predictions = pred_data.get("predictions", {})
        except Exception as e:
            logging.warning(f"Failed to parse predictions JSON: {e}")

    tensor_features = {}
    if args.tensor_json_path and os.path.exists(args.tensor_json_path):
        try:
            with open(args.tensor_json_path, "r", encoding="utf-8") as f:
                tensor_data = json.load(f)
                tensor_features = tensor_data.get("features", {})
        except Exception as e:
            logging.warning(f"Failed to parse tensor JSON: {e}")

    # Merge tensor features into the main features dict
    for stem_name, stem_feats in tensor_features.items():
        if stem_name == "mix":
            if "mix" not in features:
                features["mix"] = {}
            features["mix"].update(stem_feats)
        elif stem_name == "demucs":
            if "demucs" not in features:
                features["demucs"] = {}
            for sub_stem, sub_feats in stem_feats.items():
                if sub_stem not in features["demucs"]:
                    features["demucs"][sub_stem] = {}
                features["demucs"][sub_stem].update(sub_feats)
        else:
            if stem_name not in features:
                features[stem_name] = {}
            features[stem_name].update(stem_feats)

    meta = {}
    album_artist = args.album_artist
    album = args.album
    artist = args.artist
    title = args.title
    track_number = args.track_number

    try:
        flac = FLAC(args.flac_path)
        for key, value in flac.tags:
            meta[key] = value
        
        if not album_artist:
            album_artist = flac.get("albumartist", flac.get("album artist", [""]))[0]
        if not album:
            album = flac.get("album", [""])[0]
        if not artist:
            artist = flac.get("artist", [""])[0]
        if not title:
            title = flac.get("title", [""])[0]
        
        if track_number == 0:
            trck = flac.get("tracknumber", ["0"])[0]
            try:
                track_number = int(trck.split("/")[0])
            except:
                track_number = 0
            
    except Exception as e:
        logging.warning(f"Failed to read FLAC tags: {e}")

    try:
        config_path = os.path.join(os.path.dirname(__file__), "config.toml")
        with open(config_path, "rb") as f:
            config = tomllib.load(f)
        db_url = config.get("database", {}).get("url", "")
    except Exception as e:
        logging.exception(f"Failed to load DB URL from {config_path}")
        sys.exit(1)

    if not db_url:
        logging.error("DB URL is empty.")
        sys.exit(1)

    try:
        conn = psycopg2.connect(db_url)
        cur = conn.cursor()
        
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
        
        filename = os.path.basename(args.flac_path)
        
        cur.execute(query, (
            args.track_hash,
            args.flac_path,
            filename,
            track_number,
            album_artist,
            album,
            artist,
            title,
            psycopg2.extras.Json(meta),
            psycopg2.extras.Json(features),
            psycopg2.extras.Json(predictions)
        ))
        
        conn.commit()
        cur.close()
        conn.close()
        
        logging.info(f"Successfully UPSERTed {args.track_hash} into PostgreSQL.")
        
        try:
            os.remove(args.json_path)
            if args.tensor_json_path and os.path.exists(args.tensor_json_path):
                os.remove(args.tensor_json_path)
            logging.info(f"Cleaned up JSON files")
            
            # Clean up the precache .npy directory
            import shutil
            import tempfile
            cache_dir = os.path.join(tempfile.gettempdir(), "flac_analyzer_cache", args.track_hash)
            if os.path.exists(cache_dir):
                shutil.rmtree(cache_dir)
                logging.info(f"Cleaned up cache directory: {cache_dir}")
        except Exception as e:
            logging.warning(f"Failed to clean up temporary files: {e}")
            
    except Exception as e:
        logging.exception("Database UPSERT failed. Falling back to DLQ SQLite...")
        
        # DLQ Fallback
        import sqlite3
        dlq_db_path = os.path.join(os.path.dirname(__file__), "send_failed.db")
        
        try:
            dlq_conn = sqlite3.connect(dlq_db_path)
            dlq_cur = dlq_conn.cursor()
            dlq_cur.execute("""
                CREATE TABLE IF NOT EXISTS failed_payloads (
                    audio_hash TEXT PRIMARY KEY,
                    filepath TEXT,
                    filename TEXT,
                    track_number INTEGER,
                    album_artist TEXT,
                    album TEXT,
                    artist TEXT,
                    title TEXT,
                    meta JSON,
                    features JSON,
                    predictions JSON,
                    failed_at DATETIME DEFAULT CURRENT_TIMESTAMP
                )
            """)
            
            # Re-read or just dump the objects
            dlq_cur.execute("""
                INSERT OR REPLACE INTO failed_payloads (
                    audio_hash, filepath, filename, track_number, album_artist, album, artist, title, meta, features, predictions
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """, (
                args.track_hash,
                args.flac_path,
                os.path.basename(args.flac_path),
                track_number,
                album_artist,
                album,
                artist,
                title,
                json.dumps(meta) if isinstance(meta, dict) else meta,
                json.dumps(features) if isinstance(features, dict) else features,
                json.dumps(predictions) if isinstance(predictions, dict) else predictions
            ))
            dlq_conn.commit()
            dlq_conn.close()
            
            logging.info(f"Successfully saved {args.track_hash} to DLQ (send_failed.db).")
            
            # Still clean up local JSON files since they are safe in DLQ
            try:
                os.remove(args.json_path)
                if args.tensor_json_path and os.path.exists(args.tensor_json_path):
                    os.remove(args.tensor_json_path)
                import shutil
                import tempfile
                cache_dir = os.path.join(tempfile.gettempdir(), "flac_analyzer_cache", args.track_hash)
                if os.path.exists(cache_dir):
                    shutil.rmtree(cache_dir)
            except Exception as cleanup_e:
                logging.warning(f"Failed to clean up temporary files after DLQ: {cleanup_e}")
                
            sys.exit(2) # Return special exit code to orchestrator
            
        except Exception as dlq_e:
            logging.exception("Failed to write to DLQ SQLite")
            sys.exit(1)

if __name__ == "__main__":
    main()
