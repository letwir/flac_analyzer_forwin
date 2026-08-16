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
    parser.add_argument("--check-hash", action="store_true", help="Check if track_hash exists in PostgreSQL")
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO, stream=sys.stderr, format='%(asctime)s - %(levelname)s - %(message)s')

def get_db_url() -> str:
    db_url = ""
    config_path = os.path.join(os.path.dirname(__file__), "config.toml")
    try:
        if os.path.exists(config_path):
            with open(config_path, "rb") as f:
                config = tomllib.load(f)
            db_url = config.get("database", {}).get("url", "")
    except Exception as e:
        logging.warning(f"{config_path} からの DB URL 読込中に問題が発生いたしましたわ: {e}")

    if not db_url:
        db_url = os.environ.get("FLAC_DB_URL", "")

    if not db_url:
        logging.error(f"DB URLが空でございますわ！ {config_path} の [database].url または環境変数 FLAC_DB_URL をご確認くださいませ。")
        sys.exit(1)
        
    return db_url

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
    parser.add_argument("--check-hash", action="store_true", help="Check if track_hash exists in PostgreSQL")
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO, stream=sys.stderr, format='%(asctime)s - %(levelname)s - %(message)s')

    if args.check_hash:
        db_url = get_db_url()

        try:
            conn = psycopg2.connect(db_url)
            cur = conn.cursor()
            cur.execute("SELECT 1 FROM raw.library_flac WHERE audio_hash = %s", (args.track_hash,))
            exists = cur.fetchone() is not None
            cur.close()
            conn.close()
            print(json.dumps({"exists": exists}))
            sys.exit(0)
        except Exception as e:
            logging.exception("DB内でのハッシュ照合中にエラーが発生いたしましたわ！")
            sys.exit(1)

    if not os.path.exists(args.json_path):
        logging.error(f"指定されたJSONファイルが存在いたしませんわ: {args.json_path}")
        sys.exit(1)

    try:
        with open(args.json_path, "r", encoding="utf-8") as f:
            json_data = json.load(f)
    except Exception as e:
        logging.exception("JSONデータのパースに失敗いたしましたわ！")
        sys.exit(1)

    features = json_data.get("features", {})
    if not features:
        logging.warning("JSON内に特徴量データが含まれておりませんわ。")

    predictions = {}
    if args.predictions_json_path and os.path.exists(args.predictions_json_path):
        try:
            with open(args.predictions_json_path, "r", encoding="utf-8") as f:
                pred_data = json.load(f)
                predictions = pred_data.get("predictions", {})
        except Exception as e:
            logging.warning(f"推論結果JSONのパースに失敗いたしましたわ: {e}")

    tensor_features = {}
    if args.tensor_json_path and os.path.exists(args.tensor_json_path):
        try:
            with open(args.tensor_json_path, "r", encoding="utf-8") as f:
                tensor_data = json.load(f)
                tensor_features = tensor_data.get("features", {})
        except Exception as e:
            logging.warning(f"テンソル特徴量JSONのパースに失敗いたしましたわ: {e}")

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

    # Base metadata fallback from args
    title = args.title
    artist = args.artist
    album = args.album
    album_artist = args.album_artist
    track_number = args.track_number

    meta = json_data.get("meta", {})

    # Extract tag metadata directly from FLAC file using mutagen
    try:
        audio = FLAC(args.flac_path)
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
        vorbis_meta.update(meta)
        meta = vorbis_meta

        if not title:
            title = audio.get("title", [""])[0]
        if not artist:
            artist = audio.get("artist", [""])[0]
        if not album:
            album = audio.get("album", [""])[0]
        if not album_artist:
            album_artist = audio.get("albumartist", audio.get("album_artist", [""]))[0]
        if track_number == 0:
            tn_str = audio.get("tracknumber", ["0"])[0]
            try:
                track_number = int(tn_str.split('/')[0])
            except:
                track_number = 0
            
    except Exception as e:
        logging.warning(f"FLACメタデータタグの読み込みに失敗いたしましたわ: {e}")

    db_url = get_db_url()

    try:
        t_conn_start = time.perf_counter()
        conn = psycopg2.connect(db_url)
        t_conn_sec = time.perf_counter() - t_conn_start
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
        
        t_query_start = time.perf_counter()
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
        t_query_sec = time.perf_counter() - t_query_start
        cur.close()
        conn.close()
        
        logging.info(f"PostgreSQL への UPSERT が無事に完了いたしましたわ！ (ハッシュ: {args.track_hash})")
        
        try:
            if os.path.exists(args.json_path):
                os.remove(args.json_path)
            if args.predictions_json_path and os.path.exists(args.predictions_json_path):
                os.remove(args.predictions_json_path)
            if args.tensor_json_path and os.path.exists(args.tensor_json_path):
                os.remove(args.tensor_json_path)
            logging.info("中間JSONファイルのクリーンアップが完了いたしましたわ！")
            
            # Clean up the precache .npy directory
            import shutil
            import tempfile
            cache_dir = os.path.join(tempfile.gettempdir(), "flac_analyzer_cache", args.track_hash)
            if os.path.exists(cache_dir):
                shutil.rmtree(cache_dir)
                logging.info(f"キャッシュディレクトリの削除が無事に完了いたしましたわ: {cache_dir}")
        except Exception as e:
            logging.warning(f"一時ファイルの削除中に問題が発生いたしましたわ: {e}")

        # 出力 JSON に profile を含めて終了
        print(json.dumps({
            "status": "success",
            "profile": {
                "db_connect": t_conn_sec,
                "db_query": t_query_sec
            }
        }))
            
    except Exception as e:
        logging.exception("PostgreSQLへのUPSERT中にエラーが発生いたしましたわ。DLQ (send_failed.db) へ退避いたしますわ！")
        
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
            
            logging.info(f"DLQ (send_failed.db) へのデータ退避が無事に完了いたしましたわ！ (ハッシュ: {args.track_hash})")
            
            # Still clean up local JSON files since they are safe in DLQ
            try:
                if os.path.exists(args.json_path):
                    os.remove(args.json_path)
                if args.predictions_json_path and os.path.exists(args.predictions_json_path):
                    os.remove(args.predictions_json_path)
                if args.tensor_json_path and os.path.exists(args.tensor_json_path):
                    os.remove(args.tensor_json_path)
                import shutil
                import tempfile
                cache_dir = os.path.join(tempfile.gettempdir(), "flac_analyzer_cache", args.track_hash)
                if os.path.exists(cache_dir):
                    shutil.rmtree(cache_dir)
            except Exception as cleanup_e:
                logging.warning(f"DLQ退避後の一時ファイルクリーンアップにて問題が発生いたしましたわ: {cleanup_e}")
                
            sys.exit(2) # Return special exit code to orchestrator
            
        except Exception as dlq_e:
            logging.exception("DLQ SQLite への書き込みにも失敗してしまいましたわ！致命的でございますわ。")
            sys.exit(1)

if __name__ == "__main__":
    main()
