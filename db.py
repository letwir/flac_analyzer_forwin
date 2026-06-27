import json
import logging
import math
import os
from typing import Any

from analyzer import DemucsFeatures, EssentiaFeatures


# === 👑 異常浮動小数点（Infinity / NaN）を null に安全変換するエンコーダー ===
class SafeAudioJSONEncoder(json.JSONEncoder):

    def default(self, o):
        try:
            import numpy as np
            if isinstance(o, (np.floating, np.integer)):
                return o.item()
            if isinstance(o, np.ndarray):
                return o.tolist()
        except ImportError:
            pass
        return super().default(o)

    def iterencode(self, o, _one_shot=False):
        """
        再帰的な内部走査時、float型やnumpy型の Infinity / NaN を検知して None (null) に差し替えますわ
        """
        try:
            import numpy as np
            has_numpy = True
        except ImportError:
            has_numpy = False

        def sanitize(obj):
            if isinstance(obj, dict):
                return {k: sanitize(v) for k, v in obj.items()}
            elif isinstance(obj, (list, tuple)):
                return [sanitize(x) for x in obj]
            elif has_numpy and isinstance(obj, (float, np.floating)):
                if np.isinf(obj) or np.isnan(obj):
                    return None  # PostgreSQLのjsonbが愛する 'null' に変換いたしますわ！
                return float(obj)
            elif not has_numpy and isinstance(obj, float):
                if math.isinf(obj) or math.isnan(obj):
                    return None
                return obj
            elif has_numpy and isinstance(obj, (int, np.integer)):
                return int(obj)
            elif has_numpy and isinstance(obj, np.ndarray):
                return [sanitize(x) for x in obj.tolist()]
            return obj

        return super().iterencode(sanitize(o), _one_shot=_one_shot)


def insert_to_postgres_actual(
    audio_hash: str,
    filepath: str,
    track_number: int | None,
    metadata_tags: dict[str, Any],
    track_features: dict[str, Any],
    essentia_features: EssentiaFeatures | None,
    demucs_features: DemucsFeatures | None,
    db_uri: str,
):
    """
    抽出された特徴量とメタデータを、指定の PostgreSQL データベースへ実際に挿入 (UPSERT) しますわ！
    """
    import psycopg2

    # ファイルパスを絶対パス化
    filepath = os.path.abspath(filepath)
    filename = os.path.basename(filepath)

    # track_number の NaN/Inf ガード
    if track_number is not None:
        try:
            if math.isnan(track_number) or math.isinf(track_number):
                track_number = None
        except (TypeError, ValueError):
            pass

    # 平坦化カラム用のメタデータを抽出
    album_artist = (
        metadata_tags.get("albumartist")
        or metadata_tags.get("album_artist")
        or metadata_tags.get("artist")
        or "Unknown"
    )
    album = metadata_tags.get("album") or "Unknown"
    artist = metadata_tags.get("artist") or "Unknown"
    title = (
        metadata_tags.get("title")
        or (f"Track {track_number}" if track_number is not None else "Unknown")
    )

    # 1. features JSONB: mix のみ格納 + demucs キーを追加 (v4 RawFeatures形式)
    features_payload: dict[str, Any] = {}
    mix_feat = track_features.get("mix")
    if mix_feat:
        dict_mix = mix_feat.to_postgres_dict(track_id="mix")
        features_payload["mix"] = {
            "scalars": dict_mix["scalars"],
            "sequences": dict_mix["sequences"]
        }
    if demucs_features:
        features_payload["demucs"] = demucs_features.to_postgres_dict()

    # 2. predictions JSONB (Essentia分類結果)
    predictions_payload = {}
    if essentia_features:
        predictions_payload = essentia_features.to_postgres_dict()

    try:
        conn = psycopg2.connect(db_uri)
        conn.autocommit = True
        cursor = conn.cursor()

        # 👑 異モードレコードのクリーンアップ ＆ 同一トラック重複削除 (整合性維持・ハッシュ更新対策)
        if track_number is not None:
            cursor.execute(
                "DELETE FROM raw.library_flac WHERE filepath = %s AND track_number IS NULL",
                (filepath,)
            )
            cursor.execute(
                "DELETE FROM raw.library_flac WHERE filepath = %s AND track_number = %s",
                (filepath, track_number)
            )
        else:
            cursor.execute(
                "DELETE FROM raw.library_flac WHERE filepath = %s AND track_number IS NOT NULL",
                (filepath,)
            )
            cursor.execute(
                "DELETE FROM raw.library_flac WHERE filepath = %s AND track_number IS NULL",
                (filepath,)
            )

        cursor.execute(
            """
            INSERT INTO raw.library_flac
              (audio_hash, filepath, filename, track_number, album_artist, album, artist, title, meta, features, predictions, collected_at, analyzed_at)
            VALUES
              (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
            ON CONFLICT (audio_hash)
            DO UPDATE SET
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
                analyzed_at = EXCLUDED.analyzed_at
            """,
            (
                audio_hash,
                filepath,
                filename,
                track_number,
                album_artist,
                album,
                artist,
                title,
                json.dumps(
                    metadata_tags, ensure_ascii=False, cls=SafeAudioJSONEncoder
                ),
                json.dumps(
                    features_payload, ensure_ascii=False, cls=SafeAudioJSONEncoder
                ),
                json.dumps(
                    predictions_payload,
                    ensure_ascii=False,
                    cls=SafeAudioJSONEncoder,
                ),
            ),
        )
        cursor.close()
        conn.close()
        logging.info(
            f"[PostgreSQL Actual INSERT] {filename} (Track: {track_number}) をDBへ挿入/更新完了いたしましたわ！"
        )
    except Exception as e:
        logging.error(
            f"[PostgreSQL Actual INSERT Error] データベース挿入失敗いたしましたわ: {e}",
            exc_info=True,
        )
        raise


def insert_to_postgres_dummy(
    audio_hash: str,
    filepath: str,
    track_number: int | None,
    metadata_tags: dict[str, Any],
    track_features: dict[str, Any],
    essentia_features: EssentiaFeatures | None,
    demucs_features: DemucsFeatures | None = None,
):
    """
    抽出された特徴量とメタデータを統合し、PostgreSQLの raw.library_flac テーブルへ
    挿入する処理を行います。環境変数が設定されている場合は実インサートを行い、
    ない場合はダミーの SQL INSERT 文を StdOut に出力しますわ！
    """
    db_uri = os.getenv("INGESTER_DATABASE_URL") or os.getenv("DATABASE_URL")
    if db_uri:
        try:
            insert_to_postgres_actual(
                audio_hash,
                filepath,
                track_number,
                metadata_tags,
                track_features,
                essentia_features,
                demucs_features,
                db_uri,
            )
            return
        except Exception:
            logging.warning(
                "[PostgreSQL Ingest] 実インサートに失敗したため、ダミーSQLの標準出力へフォールバックしますわ。"
            )

    filepath = os.path.abspath(filepath)
    filename = os.path.basename(filepath)

     # track_number の NaN/Inf ガード
    if track_number is not None:
        try:
            if math.isnan(track_number) or math.isinf(track_number):
                track_number = None
        except (TypeError, ValueError):
            pass

    # 平坦化カラム用のメタデータを抽出
    album_artist = (
        metadata_tags.get("albumartist")
        or metadata_tags.get("album_artist")
        or metadata_tags.get("artist")
        or "Unknown"
    )
    album = metadata_tags.get("album") or "Unknown"
    artist = metadata_tags.get("artist") or "Unknown"
    title = (
        metadata_tags.get("title")
        or (f"Track {track_number}" if track_number is not None else "Unknown")
    )

    print(
        f"\n--- [PostgreSQL Dummy INSERT (raw.library_flac): {filename} (Track: {track_number})] ---"
    )

    # 1. features JSONB: mix のみ格納 + demucs キーを追加 (v4 RawFeatures形式)
    features_payload: dict[str, Any] = {}
    mix_feat = track_features.get("mix")
    if mix_feat:
        dict_mix = mix_feat.to_postgres_dict(track_id="mix")
        features_payload["mix"] = {
            "scalars": dict_mix["scalars"],
            "sequences": dict_mix["sequences"]
        }
    if demucs_features:
        features_payload["demucs"] = demucs_features.to_postgres_dict()

    # 2. predictions JSONB (Essentia分類結果)
    predictions_payload = {}
    if essentia_features:
        predictions_payload = essentia_features.to_postgres_dict()
    # 3. SQLの組み立て
    meta_json = json.dumps(metadata_tags, ensure_ascii=False, cls=SafeAudioJSONEncoder)
    features_json = json.dumps(
        features_payload, ensure_ascii=False, cls=SafeAudioJSONEncoder
    )
    predictions_json = json.dumps(
        predictions_payload, ensure_ascii=False, cls=SafeAudioJSONEncoder
    )

    filepath_escaped = filepath.replace("'", "''")
    if track_number is not None:
        delete_sql = f"DELETE FROM raw.library_flac WHERE filepath = '{filepath_escaped}' AND track_number IS NULL;"
    else:
        delete_sql = f"DELETE FROM raw.library_flac WHERE filepath = '{filepath_escaped}' AND track_number IS NOT NULL;"

    sql = f"""
    {delete_sql}
    INSERT INTO raw.library_flac (audio_hash, filepath, filename, track_number, album_artist, album, artist, title, meta, features, predictions, collected_at, analyzed_at)
    VALUES (
        '{audio_hash}',
        '{filepath.replace("'", "''")}',
        '{filename.replace("'", "''")}',
        {track_number if track_number is not None else "NULL"},
        '{album_artist.replace("'", "''")}',
        '{album.replace("'", "''")}',
        '{artist.replace("'", "''")}',
        '{title.replace("'", "''")}',
        '{meta_json.replace("'", "''")}',
        '{features_json.replace("'", "''")}',
        '{predictions_json.replace("'", "''")}',
        CURRENT_TIMESTAMP,
        CURRENT_TIMESTAMP
    )
    ON CONFLICT (audio_hash)
    DO UPDATE SET
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
    print(sql)


def upsert_flac(
    conn,
    audio_hash: str,
    filepath: str,
    track_number: int | None,
    metadata_tags: dict,
    track_features: dict,
    essentia_features: EssentiaFeatures | None = None,
    demucs_features: DemucsFeatures | None = None,
):
    """Consumer から直接呼ぶ PostgreSQL UPSERT。ON CONFLICT で更新しますわ。"""
    import json

    filepath = os.path.abspath(filepath)
    filename = os.path.basename(filepath)

     # track_number の NaN/Inf ガード
    if track_number is not None:
        try:
            if math.isnan(track_number) or math.isinf(track_number):
                track_number = None
        except (TypeError, ValueError):
            pass

    album_artist = (
        metadata_tags.get("albumartist")
        or metadata_tags.get("album_artist")
        or metadata_tags.get("artist")
        or "Unknown"
    )
    album = metadata_tags.get("album") or "Unknown"
    artist = metadata_tags.get("artist") or "Unknown"
    title = (
        metadata_tags.get("title")
        or (f"Track {track_number}" if track_number is not None else "Unknown")
    )

    features_payload: dict = {}
    mix_feat = track_features.get("mix")
    if mix_feat:
        dict_mix = mix_feat.to_postgres_dict(track_id="mix")
        features_payload["mix"] = {
            "scalars": dict_mix["scalars"],
            "sequences": dict_mix["sequences"],
        }
    if demucs_features:
        features_payload["demucs"] = demucs_features.to_postgres_dict()

    predictions_payload = {}
    if essentia_features:
        predictions_payload = essentia_features.to_postgres_dict()

    with conn.cursor() as cur:
        # 同一ファイルパスかつ同一トラックの古いレコードを明示的削除 (ハッシュ更新対策)
        if track_number is not None:
            cur.execute(
                "DELETE FROM raw.library_flac WHERE filepath = %s AND track_number IS NULL",
                (filepath,)
            )
            cur.execute(
                "DELETE FROM raw.library_flac WHERE filepath = %s AND track_number = %s",
                (filepath, track_number)
            )
        else:
            cur.execute(
                "DELETE FROM raw.library_flac WHERE filepath = %s AND track_number IS NOT NULL",
                (filepath,)
            )
            cur.execute(
                "DELETE FROM raw.library_flac WHERE filepath = %s AND track_number IS NULL",
                (filepath,)
            )

        cur.execute(
            """
            INSERT INTO raw.library_flac
              (audio_hash, filepath, filename, track_number, album_artist, album, artist, title, meta, features, predictions, collected_at, analyzed_at)
            VALUES
              (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
            ON CONFLICT (audio_hash)
            DO UPDATE SET
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
                analyzed_at = EXCLUDED.analyzed_at
            """,
            (
                audio_hash,
                filepath,
                filename,
                track_number,
                album_artist,
                album,
                artist,
                title,
                json.dumps(
                    metadata_tags, ensure_ascii=False, cls=SafeAudioJSONEncoder
                ),
                json.dumps(
                    features_payload, ensure_ascii=False, cls=SafeAudioJSONEncoder
                ),
                json.dumps(
                    predictions_payload,
                    ensure_ascii=False,
                    cls=SafeAudioJSONEncoder,
                ),
            ),
        )
    conn.commit()
