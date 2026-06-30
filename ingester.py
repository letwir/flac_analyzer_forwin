import sys
import json
import os
import argparse
import psycopg2
import psycopg2.extras
import logging
from mutagen.flac import FLAC

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--flac-path", required=True)
    parser.add_argument("--json-path", required=True)
    parser.add_argument("--track-hash", required=True)
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

    meta = {}
    album_artist = ""
    album = ""
    artist = ""
    title = ""
    track_number = 0

    try:
        flac = FLAC(args.flac_path)
        for key, value in flac.tags:
            meta[key] = value
        
        album_artist = flac.get("albumartist", flac.get("album artist", [""]))[0]
        album = flac.get("album", [""])[0]
        artist = flac.get("artist", [""])[0]
        title = flac.get("title", [""])[0]
        
        trck = flac.get("tracknumber", ["0"])[0]
        try:
            track_number = int(trck.split("/")[0])
        except:
            track_number = 0
            
    except Exception as e:
        logging.warning(f"Failed to extract FLAC metadata: {e}")

    db_url = os.environ.get("INGESTER_DATABASE_URL") or os.environ.get("DATABASE_URL")
    if not db_url:
        logging.error("No DATABASE_URL or INGESTER_DATABASE_URL provided.")
        sys.exit(1)

    try:
        conn = psycopg2.connect(db_url)
        cur = conn.cursor()
        
        query = """
            INSERT INTO raw.library_flac (
                audio_hash, filepath, filename, track_number, album_artist, album, artist, title, meta, features
            ) VALUES (
                %s, %s, %s, %s, %s, %s, %s, %s, %s, %s
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
                features = EXCLUDED.features;
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
            psycopg2.extras.Json(features)
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
