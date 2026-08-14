"""
Analyzer Types Module
=====================
すべての特徴量データクラス（RawFeatures, StemFeatures, TonnetzFeatures等）
およびシリアライズ/変換ヘルパー関数を定義しますの。
"""

from dataclasses import dataclass, field
from typing import Any


# ─────────────────────────────────────────────
# 型変換ヘルパー関数
# ─────────────────────────────────────────────
def _safe_int(val: Any, multiplier: float = 1.0, default: int = 0) -> int:
    if val is None:
        return default
    try:
        import math

        f_val = float(val)
        if math.isnan(f_val) or math.isinf(f_val):
            return default
        return int(f_val * multiplier)
    except (TypeError, ValueError, OverflowError):
        return default


def _safe_float_str(val: Any, default: str = "0.0") -> str:
    if val is None:
        return default
    try:
        import math

        f_val = float(val)
        if math.isnan(f_val) or math.isinf(f_val):
            return default
        return str(f_val)
    except (TypeError, ValueError):
        return default


# ─────────────────────────────────────────────
# 特徴量データクラス群
# ─────────────────────────────────────────────
@dataclass
class TonnetzFeatures:
    """Tonnetz和声特徴量を保持するデータクラスですわ。"""

    mean: list[float]
    std: list[float]
    delta_mean: list[float]
    seq: list[float]


@dataclass
class SectionFeatures:
    """セクション構造特徴量。"""

    section_count: int = 0
    section_length_std: float = 0.0
    drop_position: float = 0.0


@dataclass
class GrooveFeatures:
    """Groove / Syncopation 指標。"""

    swing_ratio: float = 1.0
    syncopation_index: float = 0.0
    groove_class: str = "straight"


@dataclass
class TemporalSeqFeatures:
    """固定フレーム (FIXED_SEQ_FRAMES=32) 時系列特徴量群。"""

    centroid_mean: float
    centroid_std: float
    centroid_seq: list[float]
    rms_seq: list[float]
    chroma_entropy_mean: float
    chroma_entropy_std: float
    chroma_entropy_seq: list[float]
    centroid_delta_mean: float
    centroid_delta_std: float
    centroid_delta_seq: list[float]
    dynamics_range_seq: list[float]


@dataclass
class MfccFeatures:
    """MFCC詳細特徴量群ですわ。"""

    mean: list[float]
    std: list[float]
    entropy: list[float]
    seq: list[list[float]]


@dataclass
class SpectralCentroidFeatures:
    """Spectral Centroid詳細特徴量群ですわ。"""

    mean: float
    std: float
    entropy: float
    seq: list[float]
    peak: float


@dataclass
class RmsFeatures:
    """RMS詳細特徴量群ですわ。"""

    mean: float
    std: float
    entropy: float
    seq: list[float]
    peak: float


@dataclass
class SpectralRolloffFeatures:
    """Spectral Rolloff詳細特徴量群ですわ。"""

    mean: float = 0.0
    std: float = 0.0
    seq: list[float] = field(default_factory=list)


@dataclass
class ZcrFeatures:
    """Zero Crossing Rate詳細特徴量群ですわ。"""

    mean: float = 0.0
    std: float = 0.0
    seq: list[float] = field(default_factory=list)


@dataclass
class ChromaFeatures:
    """Chroma詳細特徴量群ですわ。"""

    mean: list[float]
    std: list[float]
    entropy: list[float]
    seq: list[list[float]]
    peak: list[float]
    entropy_mean: float
    entropy_std: float
    entropy_entropy: float
    entropy_seq: list[float]


@dataclass
class DemucsFeatures:
    """各分離ステムの詳細特徴量を包含するクラスですわ！"""

    stems: dict[str, Any] = field(default_factory=dict)
    energy_ratios: dict[str, float] = field(default_factory=dict)

    def to_flac_tags(self, prefix: str = "") -> dict[str, str]:
        p = f"{prefix}_" if prefix else ""
        tags = {}
        for name, ratio in self.energy_ratios.items():
            tags[f"{p}DEMUCS_{name.upper()}_ENERGY_RATIO"] = str(int(ratio * 1000))
        for name, feat in self.stems.items():
            if hasattr(feat, "to_flac_tags"):
                tags.update(feat.to_flac_tags(prefix=f"{p}DEMUCS_{name.upper()}"))
        return tags

    def to_postgres_dict(self) -> dict[str, Any]:
        res = {}
        for name, feat in self.stems.items():
            if hasattr(feat, "to_postgres_dict"):
                dict_feat = feat.to_postgres_dict(track_id=name)
                dict_feat["scalars"]["energy_ratio"] = self.energy_ratios.get(name, 0.0)
                res[name] = {
                    "scalars": dict_feat["scalars"],
                    "sequences": dict_feat["sequences"],
                }
        return res


@dataclass
class KeyFeatures:
    """Key / Scale 推定結果。"""

    key: str = "Unknown"
    scale: str = "Unknown"
    key_strength: float = 0.0
    key_strength_mean: float = 0.0
    key_strength_std: float = 0.0
    key_strength_seq: list[float] = field(default_factory=list)


@dataclass
class TempogramFeatures:
    """Tempogram統計値。"""

    mean: float = 0.0
    std: float = 0.0
    peak: float = 0.0
    entropy: float = 0.0
    tempo_seq: list[float] = field(default_factory=list)


@dataclass
class OnsetFeatures:
    """Onset Strength 統計および自己相関。"""

    mean: float = 0.0
    std: float = 0.0
    max: float = 0.0
    p25: float = 0.0
    p50: float = 0.0
    p75: float = 0.0
    crest: float = 0.0
    autocorr: list[float] = field(default_factory=list)
    skew: float = 0.0
    kurt: float = 0.0
    onset_strength_seq: list[float] = field(default_factory=list)


@dataclass
class ScipyStatsFeatures:
    """Scipy周波数スペクトル統計特徴量群ですわ。"""

    skewness_mean: float = 0.0
    skewness_std: float = 0.0
    skewness_peak: float = 0.0
    skewness_min: float = 0.0
    skewness_seq: list[float] = field(default_factory=list)
    kurtosis_mean: float = 0.0
    kurtosis_std: float = 0.0
    kurtosis_peak: float = 0.0
    kurtosis_min: float = 0.0
    kurtosis_seq: list[float] = field(default_factory=list)


@dataclass
class HilbertFeatures:
    """Scipy Hilbert変換特徴量群ですわ。"""

    env_mean: float = 0.0
    env_std: float = 0.0
    env_peak: float = 0.0
    env_min: float = 0.0
    env_seq: list[float] = field(default_factory=list)
    inst_freq_mean: float = 0.0
    inst_freq_std: float = 0.0
    inst_freq_peak: float = 0.0
    inst_freq_min: float = 0.0
    inst_freq_seq: list[float] = field(default_factory=list)


@dataclass
class PeakFeatures:
    """Scipy ピーク特徴量群ですわ。"""

    spectral_mean: float = 0.0
    spectral_std: float = 0.0
    spectral_peak: float = 0.0
    spectral_min: float = 0.0
    spectral_seq: list[float] = field(default_factory=list)
    temporal_mean: float = 0.0
    temporal_std: float = 0.0
    temporal_peak: float = 0.0
    temporal_min: float = 0.0
    temporal_seq: list[float] = field(default_factory=list)


@dataclass
class EssentiaFeatures:
    """ONNX(Essentia)分類結果を保持するデータクラスですわ！"""

    predictions: dict[str, float]

    def __init__(self, predictions: dict[str, float]):
        self.predictions = predictions

    def to_flac_tags(self, prefix: str = "") -> dict[str, str]:
        p = f"{prefix}_" if prefix else ""
        return {f"{p}{k}": str(_safe_int(v, 1000)) for k, v in self.predictions.items()}

    def to_postgres_dict(self) -> dict[str, Any]:
        try:
            import math

            return {
                k.lower(): (
                    None if math.isnan(float(v)) or math.isinf(float(v)) else float(v)
                )
                for k, v in self.predictions.items()
            }
        except (TypeError, ValueError):
            return {k.lower(): 0.0 for k, v in self.predictions.items()}


@dataclass
class StemFeatures:
    """各ステムから抽出する最小限の特徴量を保持するデータクラスですわ。"""

    energy: float = 0.0
    zcr: float = 0.0
    nap: float = 0.0
    hnr_db: float = 0.0
    hnr: float = 0.0
    spectral_centroid_mean: float = 0.0
    rms_mean: float = 0.0


# ─────────────────────────────────────────────
# RawFeatures (v4: Single Source of Truth)
# ─────────────────────────────────────────────
@dataclass
class RawFeatures:
    """v4: 特徴量統合データクラスですわ！"""

    # スカラー
    energy: float = 0.0
    bpm: float = 0.0
    crest_factor: float = 0.0
    snr: float | None = None
    nap: float = 0.0
    hnr_db: float = 0.0
    hnr: float = 0.0

    # RMS
    rms_mean: float = 0.0
    rms_std: float = 0.0
    rms_peak: float = 0.0
    rms_max: float = 0.0
    rms_min: float = 0.0
    rms_median: float = 0.0
    rms_entropy: float = 0.0

    # Centroid
    centroid_mean: float = 0.0
    centroid_std: float = 0.0
    centroid_peak: float = 0.0
    centroid_max: float = 0.0
    centroid_min: float = 0.0
    centroid_median: float = 0.0
    centroid_entropy: float = 0.0

    # その他
    beat_regularity: float | None = None
    dominant_pitch: str = "Unknown"
    onset_feat: OnsetFeatures | None = None
    tempogram_feat: TempogramFeatures | None = None

    rolloff_mean: float = 0.0
    rolloff_std: float = 0.0
    rolloff_seq: list[float] = field(default_factory=list)
    beat_stability: float = 0.0

    spectral_bandwidth: float = 0.0
    flatness: float = 0.0
    zcr_mean: float = 0.0
    zcr_std: float = 0.0
    zcr_seq: list[float] = field(default_factory=list)
    contrast_bands: list[float] = field(default_factory=list)
    mfccs: list[float] = field(default_factory=list)

    # 構造/調性
    section: SectionFeatures | None = None
    groove: GrooveFeatures | None = None
    key_feat: KeyFeatures | None = None

    # 時系列
    rms_seq: list[float] = field(default_factory=list)
    centroid_seq: list[float] = field(default_factory=list)
    centroid_delta_seq: list[float] = field(default_factory=list)
    dynamics_range_seq: list[float] = field(default_factory=list)
    tempogram_tempo: list[float] = field(default_factory=list)
    key_strength_seq: list[float] = field(default_factory=list)

    tonnetz: list[list[float]] = field(default_factory=list)
    chroma: list[list[float]] = field(default_factory=list)
    mfcc: list[list[float]] = field(default_factory=list)

    chord_sequence: list[str] = field(default_factory=list)
    vocal_f0_seq: list[float] | None = None

    scipy_stats_feat: ScipyStatsFeatures | None = None
    hilbert_feat: HilbertFeatures | None = None
    peak_feat: PeakFeatures | None = None

    def to_postgres_dict(self, track_id: str) -> dict[str, Any]:
        return {
            "source": track_id,
            "scalars": _stem_filter_scalars(self, track_id),
            "sequences": _stem_filter_sequences(self, track_id),
        }

    def to_flac_tags(self, prefix: str = "") -> dict[str, str]:
        p = f"{prefix}_" if prefix else ""
        tags = {
            f"{p}LIBROSA_RMS_MEAN": str(_safe_int(self.rms_mean, 100)),
            f"{p}LIBROSA_RMS_PEAK": str(_safe_int(self.rms_peak, 100)),
            f"{p}LIBROSA_ENERGY": str(_safe_int(self.energy, 100)),
            f"{p}LIBROSA_BPM": str(_safe_int(self.bpm)),
            f"{p}LIBROSA_DOMINANT_PITCH": self.dominant_pitch,
            f"{p}LIBROSA_SPECTRAL_CENTROID_MEAN": str(_safe_int(self.centroid_mean)),
            f"{p}LIBROSA_SPECTRAL_CENTROID_SD": str(_safe_int(self.centroid_std)),
            f"{p}LIBROSA_SPECTRAL_BANDWIDTH": str(_safe_int(self.spectral_bandwidth)),
            f"{p}LIBROSA_FLATNESS": str(_safe_int(self.flatness, 100)),
            f"{p}LIBROSA_ROLLOFF": _safe_float_str(self.rolloff_mean),
            f"{p}LIBROSA_ZCR_MEAN": _safe_float_str(self.zcr_mean),
            f"{p}LIBROSA_ZCR_STD": _safe_float_str(self.zcr_std),
            f"{p}LIBROSA_ZCR": _safe_float_str(self.zcr_mean),
            f"{p}LIBROSA_NAP": _safe_float_str(self.nap),
            f"{p}LIBROSA_HNR_DB": _safe_float_str(self.hnr_db),
            f"{p}LIBROSA_HNR": _safe_float_str(self.hnr_db),
            f"{p}LIBROSA_SECTION_COUNT": str(self.section.section_count)
            if self.section
            else "0",
            f"{p}LIBROSA_SECTION_LENGTH_STD": str(
                _safe_int(self.section.section_length_std, 100)
            )
            if self.section
            else "0",
            f"{p}LIBROSA_DROP_POSITION": str(
                _safe_int(self.section.drop_position, 1000)
            )
            if self.section
            else "0",
            f"{p}LIBROSA_SWING_RATIO": str(_safe_int(self.groove.swing_ratio, 100))
            if self.groove
            else "100",
            f"{p}LIBROSA_SYNCOPATION_INDEX": str(
                _safe_int(self.groove.syncopation_index, 1000)
            )
            if self.groove
            else "0",
            f"{p}LIBROSA_GROOVE_CLASS": self.groove.groove_class
            if self.groove
            else "straight",
            f"{p}LIBROSA_CREST_FACTOR": str(_safe_int(self.crest_factor, 100)),
        }
        if self.beat_regularity is not None:
            tags[f"{p}LIBROSA_BEAT_REGULARITY"] = str(
                _safe_int(self.beat_regularity, 100)
            )
        if self.beat_stability is not None:
            tags[f"{p}LIBROSA_BEAT_STABILITY"] = str(
                _safe_int(self.beat_stability, 1000)
            )
        if self.snr is not None:
            tags[f"{p}LIBROSA_SNR"] = _safe_float_str(self.snr)
        for bi, val in enumerate(self.contrast_bands):
            tags[f"{p}LIBROSA_CONTRAST_B{bi}"] = str(_safe_int(val, 100))
        for ci, val in enumerate(self.mfccs):
            tags[f"{p}LIBROSA_MFCC{ci:02d}"] = str(_safe_int(val, 100))

        if self.key_feat is not None:
            kf = self.key_feat
            tags[f"{p}LIBROSA_KEY"] = kf.key
            tags[f"{p}LIBROSA_SCALE"] = kf.scale
            tags[f"{p}LIBROSA_KEY_STRENGTH"] = str(_safe_int(kf.key_strength, 1000))
            tags[f"{p}LIBROSA_KEY_STRENGTH_MEAN"] = str(
                _safe_int(kf.key_strength_mean, 1000)
            )
            tags[f"{p}LIBROSA_KEY_STRENGTH_STD"] = str(
                _safe_int(kf.key_strength_std, 1000)
            )
        if self.tempogram_feat is not None:
            tf = self.tempogram_feat
            tags[f"{p}LIBROSA_TEMPOGRAM_MEAN"] = str(_safe_int(tf.mean, 1000))
            tags[f"{p}LIBROSA_TEMPOGRAM_STD"] = str(_safe_int(tf.std, 1000))
            tags[f"{p}LIBROSA_TEMPOGRAM_PEAK"] = str(_safe_int(tf.peak, 1000))
            tags[f"{p}LIBROSA_TEMPOGRAM_ENTROPY"] = str(_safe_int(tf.entropy, 1000))
        if self.onset_feat is not None:
            of = self.onset_feat
            tags[f"{p}LIBROSA_ONSET_MEAN"] = str(_safe_int(of.mean, 1000))
            tags[f"{p}LIBROSA_ONSET_STD"] = str(_safe_int(of.std, 1000))
            tags[f"{p}LIBROSA_ONSET_MAX"] = str(_safe_int(of.max, 1000))
            tags[f"{p}LIBROSA_ONSET_P25"] = str(_safe_int(of.p25, 1000))
            tags[f"{p}LIBROSA_ONSET_P50"] = str(_safe_int(of.p50, 1000))
            tags[f"{p}LIBROSA_ONSET_P75"] = str(_safe_int(of.p75, 1000))
            tags[f"{p}LIBROSA_ONSET_CREST"] = str(_safe_int(of.crest, 1000))
            tags[f"{p}LIBROSA_ONSET_SKEW"] = str(_safe_int(of.skew, 1000))
            tags[f"{p}LIBROSA_ONSET_KURT"] = str(_safe_int(of.kurt, 1000))

        tags[f"{p}LIBROSA_RMS_STD"] = str(_safe_int(self.rms_std, 100))
        tags[f"{p}LIBROSA_RMS_ENTROPY"] = str(_safe_int(self.rms_entropy, 1000))
        tags[f"{p}LIBROSA_SPECTRAL_CENTROID_ENTROPY"] = str(
            _safe_int(self.centroid_entropy, 1000)
        )
        tags[f"{p}LIBROSA_SPECTRAL_CENTROID_PEAK"] = str(_safe_int(self.centroid_peak))
        tags[f"{p}LIBROSA_ROLLOFF_MEAN"] = str(_safe_int(self.rolloff_mean))
        tags[f"{p}LIBROSA_ROLLOFF_STD"] = str(_safe_int(self.rolloff_std))

        if self.scipy_stats_feat is not None:
            ssf = self.scipy_stats_feat
            tags[f"{p}SKEWNESS_MEAN"] = str(_safe_int(ssf.skewness_mean, 1000))
            tags[f"{p}SKEWNESS_STD"] = str(_safe_int(ssf.skewness_std, 1000))
            tags[f"{p}SKEWNESS_PEAK"] = str(_safe_int(ssf.skewness_peak, 1000))
            tags[f"{p}SKEWNESS_MIN"] = str(_safe_int(ssf.skewness_min, 1000))
            tags[f"{p}KURTOSIS_MEAN"] = str(_safe_int(ssf.kurtosis_mean, 1000))
            tags[f"{p}KURTOSIS_STD"] = str(_safe_int(ssf.kurtosis_std, 1000))
            tags[f"{p}KURTOSIS_PEAK"] = str(_safe_int(ssf.kurtosis_peak, 1000))
            tags[f"{p}KURTOSIS_MIN"] = str(_safe_int(ssf.kurtosis_min, 1000))

        if self.hilbert_feat is not None:
            hf = self.hilbert_feat
            tags[f"{p}HILBERT_ENV_MEAN"] = str(_safe_int(hf.env_mean, 1000))
            tags[f"{p}HILBERT_ENV_STD"] = str(_safe_int(hf.env_std, 1000))
            tags[f"{p}HILBERT_ENV_PEAK"] = str(_safe_int(hf.env_peak, 1000))
            tags[f"{p}HILBERT_ENV_MIN"] = str(_safe_int(hf.env_min, 1000))
            tags[f"{p}HILBERT_INST_FREQ_MEAN"] = str(_safe_int(hf.inst_freq_mean, 1000))
            tags[f"{p}HILBERT_INST_FREQ_STD"] = str(_safe_int(hf.inst_freq_std, 1000))
            tags[f"{p}HILBERT_INST_FREQ_PEAK"] = str(_safe_int(hf.inst_freq_peak, 1000))
            tags[f"{p}HILBERT_INST_FREQ_MIN"] = str(_safe_int(hf.inst_freq_min, 1000))

        if self.peak_feat is not None:
            pf = self.peak_feat
            tags[f"{p}PEAK_SPECTRAL_MEAN"] = str(_safe_int(pf.spectral_mean, 1000))
            tags[f"{p}PEAK_SPECTRAL_STD"] = str(_safe_int(pf.spectral_std, 1000))
            tags[f"{p}PEAK_SPECTRAL_PEAK"] = str(_safe_int(pf.spectral_peak, 1000))
            tags[f"{p}PEAK_SPECTRAL_MIN"] = str(_safe_int(pf.spectral_min, 1000))
            tags[f"{p}PEAK_TEMPORAL_MEAN"] = str(_safe_int(pf.temporal_mean, 1000))
            tags[f"{p}PEAK_TEMPORAL_STD"] = str(_safe_int(pf.temporal_std, 1000))
            tags[f"{p}PEAK_TEMPORAL_PEAK"] = str(_safe_int(pf.temporal_peak, 1000))
            tags[f"{p}PEAK_TEMPORAL_MIN"] = str(_safe_int(pf.temporal_min, 1000))

        return tags


# ─────────────────────────────────────────────
# Stem フィルタリング関数
# ─────────────────────────────────────────────
def _stem_filter_scalars(raw: RawFeatures, track_id: str) -> dict[str, Any]:
    scalars: dict[str, Any] = {
        "energy": float(raw.energy),
        "bpm": float(raw.bpm),
        "crest_factor": float(raw.crest_factor),
        "snr": float(raw.snr) if raw.snr is not None else None,
        "nap": float(raw.nap),
        "hnr_db": float(raw.hnr_db),
        "hnr": float(raw.hnr),
        "rms_mean": float(raw.rms_mean),
        "rms_std": float(raw.rms_std),
        "rms_peak": float(raw.rms_peak),
        "rms_max": float(raw.rms_max),
        "rms_min": float(raw.rms_min),
        "rms_median": float(raw.rms_median),
        "rms_entropy": float(raw.rms_entropy),
        "centroid_mean": float(raw.centroid_mean),
        "centroid_std": float(raw.centroid_std),
        "centroid_peak": float(raw.centroid_peak),
        "centroid_max": float(raw.centroid_max),
        "centroid_min": float(raw.centroid_min),
        "centroid_median": float(raw.centroid_median),
        "centroid_entropy": float(raw.centroid_entropy),
        "rolloff_mean": float(raw.rolloff_mean),
        "rolloff_std": float(raw.rolloff_std),
        "spectral_bandwidth": float(raw.spectral_bandwidth),
        "flatness": float(raw.flatness),
        "zcr_mean": float(raw.zcr_mean),
        "zcr_std": float(raw.zcr_std),
        "zcr": float(raw.zcr_mean),
        "contrast": [float(v) for v in raw.contrast_bands],
        "mfcc": [float(v) for v in raw.mfccs],
        "beat_regularity": float(raw.beat_regularity)
        if raw.beat_regularity is not None
        else None,
        "beat_stability": float(raw.beat_stability),
        "dominant_pitch": raw.dominant_pitch,
    }

    if raw.tempogram_feat is not None:
        scalars["tempogram"] = {
            "mean": float(raw.tempogram_feat.mean),
            "std": float(raw.tempogram_feat.std),
            "peak": float(raw.tempogram_feat.peak),
            "entropy": float(raw.tempogram_feat.entropy),
        }
    if raw.section is not None:
        scalars["section"] = {
            "section_count": raw.section.section_count,
            "section_length_std": float(raw.section.section_length_std),
            "drop_position": float(raw.section.drop_position),
        }
    if raw.groove is not None:
        scalars["groove"] = {
            "swing_ratio": float(raw.groove.swing_ratio),
            "syncopation_index": float(raw.groove.syncopation_index),
            "groove_class": raw.groove.groove_class,
        }
    if raw.key_feat is not None:
        scalars["key"] = raw.key_feat.key
        scalars["scale"] = raw.key_feat.scale
        scalars["key_strength"] = float(raw.key_feat.key_strength)
    if raw.onset_feat is not None:
        scalars["onset"] = {
            "mean": float(raw.onset_feat.mean),
            "std": float(raw.onset_feat.std),
            "max": float(raw.onset_feat.max),
            "p25": float(raw.onset_feat.p25),
            "p50": float(raw.onset_feat.p50),
            "p75": float(raw.onset_feat.p75),
            "crest": float(raw.onset_feat.crest),
            "skew": float(raw.onset_feat.skew),
            "kurt": float(raw.onset_feat.kurt),
        }
    if raw.scipy_stats_feat is not None:
        scalars["scipy_skewness"] = {
            "mean": float(raw.scipy_stats_feat.skewness_mean),
            "std": float(raw.scipy_stats_feat.skewness_std),
            "peak": float(raw.scipy_stats_feat.skewness_peak),
            "min": float(raw.scipy_stats_feat.skewness_min),
        }
        scalars["scipy_kurtosis"] = {
            "mean": float(raw.scipy_stats_feat.kurtosis_mean),
            "std": float(raw.scipy_stats_feat.kurtosis_std),
            "peak": float(raw.scipy_stats_feat.kurtosis_peak),
            "min": float(raw.scipy_stats_feat.kurtosis_min),
        }
    if raw.hilbert_feat is not None:
        scalars["hilbert"] = {
            "env_mean": float(raw.hilbert_feat.env_mean),
            "env_std": float(raw.hilbert_feat.env_std),
            "env_peak": float(raw.hilbert_feat.env_peak),
            "env_min": float(raw.hilbert_feat.env_min),
            "inst_freq_mean": float(raw.hilbert_feat.inst_freq_mean),
            "inst_freq_std": float(raw.hilbert_feat.inst_freq_std),
            "inst_freq_peak": float(raw.hilbert_feat.inst_freq_peak),
            "inst_freq_min": float(raw.hilbert_feat.inst_freq_min),
        }
    if raw.peak_feat is not None:
        scalars["peaks"] = {
            "spectral_mean": float(raw.peak_feat.spectral_mean),
            "spectral_std": float(raw.peak_feat.spectral_std),
            "spectral_peak": float(raw.peak_feat.spectral_peak),
            "spectral_min": float(raw.peak_feat.spectral_min),
            "temporal_mean": float(raw.peak_feat.temporal_mean),
            "temporal_std": float(raw.peak_feat.temporal_std),
            "temporal_peak": float(raw.peak_feat.temporal_peak),
            "temporal_min": float(raw.peak_feat.temporal_min),
        }
    return scalars


def _stem_filter_sequences(raw: RawFeatures, track_id: str) -> dict[str, Any]:
    sequences: dict[str, Any] = {}

    if raw.rms_seq:
        sequences["rms"] = raw.rms_seq
    if raw.centroid_seq:
        sequences["centroid"] = raw.centroid_seq
    if raw.zcr_seq:
        sequences["zcr"] = raw.zcr_seq
    if raw.tempogram_tempo:
        sequences["tempogram_tempo"] = raw.tempogram_tempo
    if raw.centroid_delta_seq:
        sequences["centroid_delta"] = raw.centroid_delta_seq
    if raw.dynamics_range_seq:
        sequences["dynamics_range"] = raw.dynamics_range_seq
    if raw.key_strength_seq:
        sequences["key_strength"] = raw.key_strength_seq
    if raw.tonnetz:
        sequences["tonnetz"] = raw.tonnetz
    if raw.chroma:
        sequences["chroma"] = raw.chroma
    if raw.mfcc:
        sequences["mfcc"] = raw.mfcc
    if raw.chord_sequence:
        sequences["chord_sequence"] = raw.chord_sequence
    if raw.rolloff_seq:
        sequences["rolloff"] = raw.rolloff_seq
    if raw.vocal_f0_seq is not None:
        sequences["vocal_f0_seq"] = raw.vocal_f0_seq
    if raw.onset_feat is not None:
        sequences["onset_strength"] = raw.onset_feat.onset_strength_seq
        sequences["onset_autocorr"] = raw.onset_feat.autocorr
    if raw.scipy_stats_feat is not None:
        sequences["scipy_skewness"] = raw.scipy_stats_feat.skewness_seq
        sequences["scipy_kurtosis"] = raw.scipy_stats_feat.kurtosis_seq
    if raw.hilbert_feat is not None:
        sequences["hilbert_env"] = raw.hilbert_feat.env_seq
        sequences["hilbert_inst_freq"] = raw.hilbert_feat.inst_freq_seq
    if raw.peak_feat is not None:
        sequences["spectral_peaks"] = raw.peak_feat.spectral_seq
        sequences["temporal_peaks"] = raw.peak_feat.temporal_seq
    return sequences
