WITH latest AS (
    -- 1. タイムスタンプを維持したまま、最新30件をセレクティブ・ロードしますの！
    SELECT
        analyzed_at,
        title,
        features,
        features->'demucs'
    FROM raw.library_flac
    ORDER BY analyzed_at DESC
    LIMIT 10
),
nested_stems AS (
    -- 2. analyzed_at を保持したまま、各ステムの配列を行にトランスフォームいたしますわ
    SELECT analyzed_at, title, 'mix' AS stem, features->'mix'->'sequences'->'chord_sequence' AS chord FROM latest
    UNION ALL
    SELECT analyzed_at, title, 'bass' AS stem, features->'demucs'->'bass'->'sequences'->'chord_sequence' AS chord FROM latest
    UNION ALL
    SELECT analyzed_at, title, 'drums' AS stem, features->'demucs'->'drums'->'sequences'->'chord_sequence' AS chord FROM latest
    UNION ALL
    SELECT analyzed_at, title, 'other' AS stem, features->'demucs'->'other'->'sequences'->'chord_sequence' AS chord FROM latest
    UNION ALL
    SELECT analyzed_at, title, 'piano' AS stem, features->'demucs'->'piano'->'sequences'->'chord_sequence' AS chord FROM latest
    UNION ALL
    SELECT analyzed_at, title, 'guitar' AS stem, features->'demucs'->'guitar'->'sequences'->'chord_sequence' AS chord FROM latest
    UNION ALL
    SELECT analyzed_at, title, 'vocals' AS stem, features->'demucs'->'vocals'->'sequences'->'chord_sequence' AS chord FROM latest
)
-- 3. 展開し終えた最終結果を、時系列順かつステム順に美しく並べて出力いたしますわ！
SELECT
    analyzed_at,
    title,
    stem,
    chord
FROM nested_stems
WHERE chord IS NOT NULL
ORDER BY analyzed_at DESC;



-- vocal_hnr（声の倍音比）× Essentiaのgender予測の分布
SELECT
    ROUND((features -> 'vocals' ->> 'hnr')::numeric, 2) AS vocal_hnr,
    (predictions ->> 'essentia_gender_female')::float    AS p_female,
    title, artist
FROM raw.library_flac
WHERE features -> 'vocals' -> 'hnr' IS NOT NULL
ORDER BY p_female DESC
LIMIT 20;


-- ジャンルランキング
SELECT
    genre_tag,
    COUNT(*) AS cnt
FROM raw.library_flac,
     jsonb_array_elements_text(meta->'genre') AS genre_tag
WHERE meta->'genre' IS NOT NULL
  AND jsonb_typeof(meta->'genre') = 'array'
GROUP BY genre_tag
ORDER BY cnt DESC
LIMIT 30;


-- ① ステム別エネルギー比率（Demucs分離品質の指標）
SELECT
    meta->>'title'                            AS title,
    meta->>'artist'                           AS artist,
    ROUND((features->'vocals'->>'energy')::numeric, 4)  AS e_vocals,
    ROUND((features->'drums' ->>'energy')::numeric, 4)  AS e_drums,
    ROUND((features->'bass'  ->>'energy')::numeric, 4)  AS e_bass,
    ROUND((features->'piano' ->>'energy')::numeric, 4)  AS e_piano,
    ROUND((features->'guitar'->>'energy')::numeric, 4)  AS e_guitar,
    ROUND((features->'mix'   ->>'energy')::numeric, 4)  AS e_mix
FROM raw.library_flac
ORDER BY e_vocals DESC
LIMIT 20;

-- ② ESSENTIAの classical確率が高い曲をピックアップ
SELECT
    meta->>'title'   AS title,
    meta->>'artist'  AS artist,
    ROUND((predictions->>'essentia_genre_rosamerica_classical')::numeric, 3) AS p_classical,
    ROUND((predictions->>'essentia_voice_instrumental_instrumental')::numeric, 3) AS p_instrumental
FROM raw.library_flac
WHERE (predictions->>'essentia_genre_rosamerica_classical')::float > 0.9
ORDER BY p_classical DESC
LIMIT 20;

-- ③ HNR（調波性）でジャンル別の「音の綺麗さ」比較
--    ※ genre_tagテーブルとJOINが必要なら教えてくださいな
SELECT
    meta->>'genre'                                    AS genre,
    COUNT(*)                                          AS cnt,
    ROUND(AVG((features->'mix'->>'hnr')::numeric), 4) AS hnr_mean,
    ROUND(AVG((features->'mix'->>'zcr')::numeric), 4) AS zcr_mean
FROM raw.library_flac
GROUP BY meta->>'genre'
ORDER BY hnr_mean DESC;


--① コード進行の最頻コードを出す
SELECT
    meta->>'title' AS title,
    chord,
    COUNT(*) AS freq
FROM raw.library_flac,
     jsonb_array_elements_text(
         features -> 'mix' -> 'sequences' -> 'chord_sequence'
     ) AS chord
WHERE id = 122
GROUP BY title, chord
ORDER BY freq DESC;


--② 曲全体の「エネルギー推移」を前半・後半で比較
WITH frames AS (
    SELECT
        id,
        meta->>'title' AS title,
        ordinality - 1 AS frame_idx,
        val::float      AS rms
    FROM raw.library_flac,
         jsonb_array_elements(
             features -> 'mix' -> 'sequences' -> 'rms'
         ) WITH ORDINALITY AS t(val, ordinality)
)
SELECT
    title,
    ROUND(AVG(rms) FILTER (WHERE frame_idx < 16)::numeric, 4) AS rms_first_half,
    ROUND(AVG(rms) FILTER (WHERE frame_idx >= 16)::numeric, 4) AS rms_second_half,
    ROUND((AVG(rms) FILTER (WHERE frame_idx >= 16) -
           AVG(rms) FILTER (WHERE frame_idx < 16))::numeric, 4) AS rms_delta
FROM frames
GROUP BY id, title
ORDER BY rms_delta DESC
LIMIT 20;


--③ 曲中のコード変化量（転調っぽさ）
WITH chords AS (
    SELECT
        id,
        meta->>'title'                AS title,
        jsonb_array_elements_text(
            features -> 'mix' -> 'sequences' -> 'chord_sequence'
        ) AS chord
    FROM raw.library_flac
)
SELECT
    title,
    COUNT(DISTINCT chord) AS unique_chords,
    COUNT(*) AS total_frames
FROM chords
GROUP BY id, title
ORDER BY unique_chords DESC
LIMIT 20;

-- 実行計画を確認
EXPLAIN ANALYZE
WITH chords AS (
    SELECT
        id,
        meta->>'title' AS title,
        jsonb_array_elements_text(
            features -> 'mix' -> 'sequences' -> 'chord_sequence'
        ) AS chord
    FROM raw.library_flac
)
SELECT title, COUNT(DISTINCT chord) AS unique_chords, COUNT(*) AS total_frames
FROM chords
GROUP BY id, title
ORDER BY unique_chords DESC
LIMIT 20;


SELECT
    analyzed_at,
    jsonb_array_length(
        features -> 'mix' -> 'sequences' -> 'mfcc'
    ) AS frames,
    jsonb_array_length(
        features -> 'mix' -> 'sequences' -> 'mfcc' -> 0
    ) AS coeffs_per_frame,
    jsonb_array_length(
        features -> 'mix' -> 'sequences' -> 'chroma' -> 0
    ) AS chroma_per_frame,
    jsonb_array_length(
        features -> 'mix' -> 'sequences' -> 'tonnetz' -> 0
    ) AS tonnetz_per_frame
FROM raw.library_flac
ORDER BY analyzed_at DESC
LIMIT 1;