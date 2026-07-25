-- =========================================================
-- Migration SQL v2: Rename track_num and add search columns
-- =========================================================

-- 1. カラム名の変更 (track_num -> track_number)
ALTER TABLE raw.library_flac RENAME COLUMN track_num TO track_number;
ALTER TABLE raw.library_flac_history RENAME COLUMN track_num TO track_number;

-- 2. カラムの追加
ALTER TABLE raw.library_flac ADD COLUMN IF NOT EXISTS album_artist VARCHAR;
ALTER TABLE raw.library_flac ADD COLUMN IF NOT EXISTS album VARCHAR;
ALTER TABLE raw.library_flac ADD COLUMN IF NOT EXISTS artist VARCHAR;
ALTER TABLE raw.library_flac ADD COLUMN IF NOT EXISTS title VARCHAR;

ALTER TABLE raw.library_flac_history ADD COLUMN IF NOT EXISTS album_artist VARCHAR;
ALTER TABLE raw.library_flac_history ADD COLUMN IF NOT EXISTS album VARCHAR;
ALTER TABLE raw.library_flac_history ADD COLUMN IF NOT EXISTS artist VARCHAR;
ALTER TABLE raw.library_flac_history ADD COLUMN IF NOT EXISTS title VARCHAR;

-- 3. B-Tree インデックスの新規作成 (検索性向上)
CREATE INDEX IF NOT EXISTS idx_library_flac_album_artist ON raw.library_flac(album_artist);
CREATE INDEX IF NOT EXISTS idx_library_flac_album ON raw.library_flac(album);
CREATE INDEX IF NOT EXISTS idx_library_flac_artist ON raw.library_flac(artist);
CREATE INDEX IF NOT EXISTS idx_library_flac_title ON raw.library_flac(title);

-- 4. 古い式インデックスの削除 (クリーンアップ)
DROP INDEX IF EXISTS raw.idx_library_flac_meta_artist;
DROP INDEX IF EXISTS raw.idx_library_flac_meta_album;

-- 5. トリガー関数の再定義（新しいカラムを history に退避できるようにします）
CREATE OR REPLACE FUNCTION raw.archive_library_flac_history()
RETURNS TRIGGER AS $$
BEGIN
    -- メタデータ(meta)や特徴量(features)に変更があった場合のみ履歴に残す
    IF (OLD.meta IS DISTINCT FROM NEW.meta) OR (OLD.features IS DISTINCT FROM NEW.features) THEN
        INSERT INTO raw.library_flac_history (
            library_id, audio_hash, filepath, filename, track_number, 
            album_artist, album, artist, title,
            meta, features, predictions, 
            collected_at, analyzed_at
        ) VALUES (
            OLD.id, OLD.audio_hash, OLD.filepath, OLD.filename, OLD.track_number, 
            OLD.album_artist, OLD.album, OLD.artist, OLD.title,
            OLD.meta, OLD.features, OLD.predictions, 
            OLD.collected_at, OLD.analyzed_at
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

