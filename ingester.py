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
        logging.error(f"Failed to parse JSON: {e}")
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
        logging.error(f"Failed to load DB URL from {config_path}: {e}")
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
            logging.info(f"Cleaned up JSON file: {args.json_path}")
        except Exception as e:
            logging.warning(f"Failed to delete JSON file: {e}")
            
    except Exception as e:
        logging.error(f"Database UPSERT failed: {e}")
        sys.exit(1)

if __name__ == "__main__":
    main()
