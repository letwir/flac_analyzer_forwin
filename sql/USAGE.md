# 今の構造
features
├── mix / bass / drums / other / piano / guitar / vocals  ← Demucsステム！
　   ├── スカラー: bpm, hnr, snr, zcr, energy, rolloff,
　   │            flatness, rms_mean, rms_peak,
　   │            beat_regularity, spectral_centroid_mean/sd,
　   │            spectral_bandwidth, dominant_pitch
　   ├── 配列: mfcc[8], contrast[7], tonnetz[192],
　   │         tonnetz_mean[6], tonnetz_std[6], tonnetz_delta_mean[6]
　   └── ※ drums/guitars は tonnetz なし

features -> {stem} -> sequences -> {
  rms:              float[32]     # フレーム別音量
  zcr:              float[32]     # ゼロ交差率
  mfcc:             float[32][n]  # 2D: フレーム×係数
  chroma:           float[32][12] # 2D: フレーム×音名
  rolloff:          float[32]
  tonnetz:          float[32][6]
  centroid:         float[32]
  centroid_delta:   float[32]
  chord_sequence:   str[32]       # "E", "Am" etc.
  dynamics_range:   float[32]
  onset_autocorr:   float[32]
  onset_strength:   float[32]
  tempogram_tempo:  float[32]
}

# JSONBの扱い方
JSONB配列アクセスの基本構文
まず構文を理解してもらいますわ。
sql-- 添字アクセス：フレーム0のRMS
features -> 'mix' -> 'sequences' -> 'rms' -> 0

-- ::float でキャスト（計算に使う時）
(features -> 'mix' -> 'sequences' -> 'rms' -> 0)::float

-- unnest展開：32フレームを32行に展開
SELECT
    jsonb_array_elements(
        features -> 'mix' -> 'sequences' -> 'rms'
    )::float AS rms_val
FROM your_table
WHERE id = 122;
-> はJSONBのまま、->> はtextになりますわ。数値計算するなら -> n)::float が正解ですわよ。
