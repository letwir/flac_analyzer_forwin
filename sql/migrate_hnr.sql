-- ============================================================================
-- SQL HNR / NAP Migration Script for raw.library_flac
-- ============================================================================
-- 概要:
-- raw.library_flac の features JSONB 内にある旧 NAP 値 (0.0 <= hnr <= 1.0) を
-- nap (0.0〜1.0) と hnr_db (10*log10(clamped/(1-clamped)))、および hnr (dB値) へ
-- インプレース変換するストアド関数および更新クエリですわ！
-- ============================================================================

CREATE OR REPLACE FUNCTION raw.fn_calc_hnr_db(nap NUMERIC)
RETURNS NUMERIC AS $$
DECLARE
    clamped NUMERIC;
BEGIN
    IF nap IS NULL THEN
        RETURN NULL;
    END IF;
    IF nap <= 0.0 THEN
        RETURN -40.0;
    END IF;
    -- Clamp between 0.0001 and 0.9999
    clamped := LEAST(GREATEST(nap, 0.0001), 0.9999);
    RETURN ROUND(10.0 * LOG(clamped / (1.0 - clamped)), 4);
END;
$$ LANGUAGE plpgsql IMMUTABLE;

CREATE OR REPLACE FUNCTION raw.fn_calc_nap_from_hnr_db(hnr_db NUMERIC)
RETURNS NUMERIC AS $$
DECLARE
    exp_val NUMERIC;
BEGIN
    IF hnr_db IS NULL THEN
        RETURN NULL;
    END IF;
    IF hnr_db > 30.0 THEN
        RETURN 1.0;
    END IF;
    IF hnr_db < -30.0 THEN
        RETURN 0.0;
    END IF;
    exp_val := POWER(10.0, hnr_db / 10.0);
    RETURN ROUND(exp_val / (1.0 + exp_val), 6);
END;
$$ LANGUAGE plpgsql IMMUTABLE;

-- 動作確認用クエリ例:
-- SELECT
--     id,
--     title,
--     features->'mix'->'scalars'->>'hnr' AS old_hnr,
--     raw.fn_calc_hnr_db((features->'mix'->'scalars'->>'hnr')::numeric) AS new_hnr_db
-- FROM raw.library_flac
-- WHERE features->'mix'->'scalars'->>'hnr' IS NOT NULL
-- LIMIT 5;
