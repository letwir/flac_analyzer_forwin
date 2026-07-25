-- =========================================================
-- PostgreSQL DDL for FLAC Analyzer (raw.library_flac)
-- =========================================================

-- 1. スキーマの作成
CREATE SCHEMA IF NOT EXISTS raw;

-- 2. メインテーブル（常に最新状態）
CREATE TABLE IF NOT EXISTS raw.library_flac (
    id SERIAL PRIMARY KEY,
    audio_hash VARCHAR(32) NOT NULL, -- 各曲のデコード後波形(numpy)のMD5 (16進数32文字)
    filepath TEXT NOT NULL,          -- 最新のファイル絶対パス
    filename TEXT NOT NULL,          -- 最新のファイル名
    track_number INT,                -- CUEシート分割時のトラック番号（なければNULL）
    album_artist VARCHAR,            -- アルバムアーティスト (検索性能向上用平坦化カラム)
    album VARCHAR,                   -- アルバム名
    artist VARCHAR,                  -- 曲のアーティスト
    title VARCHAR,                   -- 曲のタイトル
    meta JSONB NOT NULL DEFAULT '{}'::jsonb,      -- アーティスト、アルバム、タイトル等の最新メタデータ
    features JSONB NOT NULL DEFAULT '{}'::jsonb,  -- 各ステムのLibrosa音響特徴量
    predictions JSONB NOT NULL DEFAULT '{}'::jsonb, -- Essentia分類結果 (mixのみ)
    collected_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 収集・更新検知日時
    analyzed_at TIMESTAMP WITH TIME ZONE -- 解析実行日時（解析をスキップした場合は更新されない）
);

-- 3. 履歴保存用テーブル（メインテーブル of 古いバージョンを退避）
CREATE TABLE IF NOT EXISTS raw.library_flac_history (
    history_id SERIAL PRIMARY KEY,
    library_id INT NOT NULL,
    audio_hash VARCHAR(32) NOT NULL,
    filepath TEXT NOT NULL,
    filename TEXT NOT NULL,
    track_number INT,
    album_artist VARCHAR,
    album VARCHAR,
    artist VARCHAR,
    title VARCHAR,
    meta JSONB NOT NULL,
    features JSONB NOT NULL,
    predictions JSONB NOT NULL,
    collected_at TIMESTAMP WITH TIME ZONE NOT NULL,
    analyzed_at TIMESTAMP WITH TIME ZONE,
    archived_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP -- 履歴化された日時
);

-- 4. インデックスの設計（8万件規模での高速化）
-- 同一音源に対する一意制約（audio_hash はトラックごとに固有になるため、audio_hash 単体でユニークとします）
CREATE UNIQUE INDEX IF NOT EXISTS uq_library_flac_audio_hash 
ON raw.library_flac(audio_hash);

-- パスでの高速検索用（ファイル移動の検知に必要）
CREATE INDEX IF NOT EXISTS idx_library_flac_filepath 
ON raw.library_flac(filepath);

-- 平坦化されたカラムの高速インデックス (B-Tree式)
CREATE INDEX IF NOT EXISTS idx_library_flac_album_artist ON raw.library_flac(album_artist);
CREATE INDEX IF NOT EXISTS idx_library_flac_album ON raw.library_flac(album);
CREATE INDEX IF NOT EXISTS idx_library_flac_artist ON raw.library_flac(artist);
CREATE INDEX IF NOT EXISTS idx_library_flac_title ON raw.library_flac(title);

-- 複雑な検索用の GIN インデックス
CREATE INDEX IF NOT EXISTS idx_library_flac_features_gin 
ON raw.library_flac USING gin (features);

CREATE INDEX IF NOT EXISTS idx_library_flac_predictions_gin 
ON raw.library_flac USING gin (predictions);

-- 5. 履歴自動退避トリガーの実装
CREATE OR REPLACE FUNCTION raw.archive_library_flac_history()
RETURNS TRIGGER AS $$
BEGIN
    -- メタデータ(meta)や特徴量(features)に変更があった場合のみ履歴に残す
    IF (OLD.meta IS DISTINCT FROM NEW.meta) OR (OLD.features IS DISTINCT FROM NEW.features) THEN
        INSERT INTO raw.library_flac_history (
            library_id, audio_hash, filepath, filename, track_number, 
            album_artist, album, artist, title,
            meta, features, predictions, collected_at, analyzed_at
        ) VALUES (
            OLD.id, OLD.audio_hash, OLD.filepath, OLD.filename, OLD.track_number, 
            OLD.album_artist, OLD.album, OLD.artist, OLD.title,
            OLD.meta, OLD.features, OLD.predictions, OLD.collected_at, OLD.analyzed_at
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_archive_library_flac ON raw.library_flac;
CREATE TRIGGER trg_archive_library_flac
BEFORE UPDATE ON raw.library_flac
FOR EACH ROW
EXECUTE FUNCTION raw.archive_library_flac_history();

-- 6. ロール(ROLE)およびユーザーの作成
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'etl_flac') THEN
        CREATE ROLE etl_flac WITH NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'analyzer') THEN
        CREATE ROLE analyzer WITH NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'ingester') THEN
        CREATE ROLE ingester WITH LOGIN PASSWORD 'etl';
    END IF;
END
$$;

-- 7. ロールの継承設定 (ingester が etl_flac の権限を継承しますの)
GRANT etl_flac TO ingester;

-- 8. 権限付与 (DCL)
-- スキーマの使用権限
GRANT USAGE ON SCHEMA raw TO etl_flac;
GRANT USAGE ON SCHEMA raw TO analyzer;

-- テーブルに対する具体的な権限付与
GRANT INSERT, UPDATE ON TABLE raw.library_flac TO etl_flac;
GRANT INSERT, UPDATE ON TABLE raw.library_flac_history TO etl_flac;

GRANT SELECT ON TABLE raw.library_flac TO analyzer;
GRANT SELECT ON TABLE raw.library_flac_history TO analyzer;

-- シーケンスに対する権限（INSERT時にSERIALを発番するのに必須ですわ！）
GRANT USAGE, SELECT ON SEQUENCE raw.library_flac_id_seq TO etl_flac;
GRANT USAGE, SELECT ON SEQUENCE raw.library_flac_history_history_id_seq TO etl_flac;
