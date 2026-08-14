"""
Analyzer Librosa DSP Module
===========================
Librosaベースの音響特徴量抽出関数、LibrosaFeaturesクラス、
および librosa_extractor_v4 / STEM_CONFIGS を定義しますの。
"""

import logging
from typing import Any

import librosa
import numpy as np

from constants import KEY_PROFILES, NOTES
from .core import (
    AudioContext,
    FeatureExtractor,
    FIXED_SEQ_FRAMES,
    LIBROSA_LOCK,
    _resample_to_fixed_frames,
    product_all,
)
from .essentia_dsp import _calc_chord_sequence, _calc_vocal_f0_seq
from .stats import (
    _calc_hilbert_features,
    _calc_peak_features,
    _calc_scipy_stats_features,
    _calc_time_entropy,
)
from .types import (
    ChromaFeatures,
    GrooveFeatures,
    KeyFeatures,
    MfccFeatures,
    OnsetFeatures,
    RawFeatures,
    RmsFeatures,
    SectionFeatures,
    SpectralCentroidFeatures,
    SpectralRolloffFeatures,
    StemFeatures,
    TempogramFeatures,
    TemporalSeqFeatures,
    TonnetzFeatures,
    ZcrFeatures,
    _safe_float_str,
    _safe_int,
)


def _calc_rms_stats(rms: np.ndarray) -> dict[str, float]:
    """RMS時系列から7スカラー統計量を算出しますわ！"""
    mean_val = float(np.mean(rms))
    std_val = float(np.std(rms))
    peak_val = float(np.max(rms))
    max_val = peak_val
    min_val = float(np.min(rms))
    median_val = float(np.median(rms))
    abs_rms = np.abs(rms)
    s = np.sum(abs_rms)
    if s < 1e-10:
        entropy_val = 0.0
    else:
        p = abs_rms / s
        entropy_val = float(-np.sum(p * np.log2(p + 1e-10)))
    return {
        "mean": mean_val,
        "std": std_val,
        "peak": peak_val,
        "max": max_val,
        "min": min_val,
        "median": median_val,
        "entropy": entropy_val,
    }


def _calc_centroid_stats(centroid: np.ndarray) -> dict[str, float]:
    """Spectral Centroid時系列から7スカラー統計量を算出しますわ！"""
    mean_val = float(np.mean(centroid))
    std_val = float(np.std(centroid))
    peak_val = float(np.max(centroid))
    max_val = peak_val
    min_val = float(np.min(centroid))
    median_val = float(np.median(centroid))
    abs_cent = np.abs(centroid)
    s = np.sum(abs_cent)
    if s < 1e-10:
        entropy_val = 0.0
    else:
        p = abs_cent / s
        entropy_val = float(-np.sum(p * np.log2(p + 1e-10)))
    return {
        "mean": mean_val,
        "std": std_val,
        "peak": peak_val,
        "max": max_val,
        "min": min_val,
        "median": median_val,
        "entropy": entropy_val,
    }


class LibrosaFeatures:
    """Librosaによる生特徴量を保持し、FLACタグとPostgres辞書の双方を出力するクラスですわ！"""

    def __init__(
        self,
        rms_mean: float,
        rms_peak: float,
        energy: float,
        bpm: float,
        beat_regularity: float | None,
        dominant_pitch: str,
        spectral_centroid_mean: float,
        spectral_centroid_sd: float,
        spectral_bandwidth: float,
        flatness: float,
        rolloff: float,
        contrast_bands: list[float],
        zcr: float,
        snr: float | None,
        hnr: float,
        mfccs: list[float],
        tonnetz: TonnetzFeatures | None = None,
        section: SectionFeatures | None = None,
        groove: GrooveFeatures | None = None,
        crest_factor: float = 0.0,
        temporal_seq: TemporalSeqFeatures | None = None,
        key_feat: KeyFeatures | None = None,
        tempogram_feat: TempogramFeatures | None = None,
        onset_feat: OnsetFeatures | None = None,
        rms_obj: RmsFeatures | None = None,
        centroid_obj: SpectralCentroidFeatures | None = None,
        mfcc_obj: MfccFeatures | None = None,
        chroma_obj: ChromaFeatures | None = None,
    ):
        self.rms_mean = rms_mean
        self.rms_peak = rms_peak
        self.energy = energy
        self.bpm = bpm
        self.beat_regularity = beat_regularity
        self.dominant_pitch = dominant_pitch
        self.spectral_centroid_mean = spectral_centroid_mean
        self.spectral_centroid_sd = spectral_centroid_sd
        self.spectral_bandwidth = spectral_bandwidth
        self.flatness = flatness
        self.rolloff = rolloff
        self.contrast_bands = contrast_bands
        self.zcr = zcr
        self.snr = snr
        self.hnr = hnr
        self.mfccs = mfccs
        self.tonnetz = tonnetz
        self.section = section if section is not None else SectionFeatures()
        self.groove = groove if groove is not None else GrooveFeatures()
        self.crest_factor = crest_factor
        self.temporal_seq = temporal_seq
        self.key_feat = key_feat if key_feat is not None else KeyFeatures()
        self.tempogram_feat = (
            tempogram_feat if tempogram_feat is not None else TempogramFeatures()
        )
        self.onset_feat = onset_feat if onset_feat is not None else OnsetFeatures()
        self.rms_obj = rms_obj
        self.centroid_obj = centroid_obj
        self.mfcc_obj = mfcc_obj
        self.chroma_obj = chroma_obj

    def to_flac_tags(self, prefix: str = "") -> dict[str, str]:
        p = f"{prefix}_" if prefix else ""
        tags = {
            f"{p}LIBROSA_RMS_MEAN": str(_safe_int(self.rms_mean, 100)),
            f"{p}LIBROSA_RMS_PEAK": str(_safe_int(self.rms_peak, 100)),
            f"{p}LIBROSA_ENERGY": str(_safe_int(self.energy, 100)),
            f"{p}LIBROSA_BPM": str(_safe_int(self.bpm)),
            f"{p}LIBROSA_DOMINANT_PITCH": self.dominant_pitch,
            f"{p}LIBROSA_SPECTRAL_CENTROID_MEAN": str(
                _safe_int(self.spectral_centroid_mean)
            ),
            f"{p}LIBROSA_SPECTRAL_CENTROID_SD": str(
                _safe_int(self.spectral_centroid_sd)
            ),
            f"{p}LIBROSA_SPECTRAL_BANDWIDTH": str(_safe_int(self.spectral_bandwidth)),
            f"{p}LIBROSA_FLATNESS": str(_safe_int(self.flatness, 100)),
            f"{p}LIBROSA_ROLLOFF": _safe_float_str(self.rolloff),
            f"{p}LIBROSA_ZCR": _safe_float_str(self.zcr),
            f"{p}LIBROSA_HNR": _safe_float_str(self.hnr),
            f"{p}LIBROSA_SECTION_COUNT": str(self.section.section_count),
            f"{p}LIBROSA_SECTION_LENGTH_STD": str(
                _safe_int(self.section.section_length_std, 100)
            ),
            f"{p}LIBROSA_DROP_POSITION": str(
                _safe_int(self.section.drop_position, 1000)
            ),
            f"{p}LIBROSA_SWING_RATIO": str(_safe_int(self.groove.swing_ratio, 100)),
            f"{p}LIBROSA_SYNCOPATION_INDEX": str(
                _safe_int(self.groove.syncopation_index, 1000)
            ),
            f"{p}LIBROSA_GROOVE_CLASS": self.groove.groove_class,
            f"{p}LIBROSA_CREST_FACTOR": str(_safe_int(self.crest_factor, 100)),
        }
        if self.beat_regularity is not None:
            tags[f"{p}LIBROSA_BEAT_REGULARITY"] = str(
                _safe_int(self.beat_regularity, 100)
            )
        if self.snr is not None:
            tags[f"{p}LIBROSA_SNR"] = _safe_float_str(self.snr)
        for bi, val in enumerate(self.contrast_bands):
            tags[f"{p}LIBROSA_CONTRAST_B{bi}"] = str(_safe_int(val, 100))
        for ci, val in enumerate(self.mfccs):
            tags[f"{p}LIBROSA_MFCC{ci:02d}"] = str(_safe_int(val, 100))
        if self.temporal_seq is not None:
            ts = self.temporal_seq
            tags[f"{p}LIBROSA_CENTROID_SEQ_MEAN"] = str(_safe_int(ts.centroid_mean))
            tags[f"{p}LIBROSA_CENTROID_SEQ_STD"] = str(_safe_int(ts.centroid_std))
            tags[f"{p}LIBROSA_CHROMA_ENTROPY_MEAN"] = str(
                _safe_int(ts.chroma_entropy_mean, 1000)
            )
            tags[f"{p}LIBROSA_CHROMA_ENTROPY_STD"] = str(
                _safe_int(ts.chroma_entropy_std, 1000)
            )
            tags[f"{p}LIBROSA_CENTROID_DELTA_MEAN"] = str(
                _safe_int(ts.centroid_delta_mean)
            )
            tags[f"{p}LIBROSA_CENTROID_DELTA_STD"] = str(
                _safe_int(ts.centroid_delta_std)
            )
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

        if self.rms_obj is not None:
            tags[f"{p}LIBROSA_RMS_STD"] = str(_safe_int(self.rms_obj.std, 100))
            tags[f"{p}LIBROSA_RMS_ENTROPY"] = str(_safe_int(self.rms_obj.entropy, 1000))
        if self.centroid_obj is not None:
            tags[f"{p}LIBROSA_SPECTRAL_CENTROID_ENTROPY"] = str(
                _safe_int(self.centroid_obj.entropy, 1000)
            )
            tags[f"{p}LIBROSA_SPECTRAL_CENTROID_PEAK"] = str(
                _safe_int(self.centroid_obj.peak)
            )
        if self.mfcc_obj is not None:
            for i in range(len(self.mfcc_obj.mean)):
                tags[f"{p}LIBROSA_MFCC_MEAN_{i:02d}"] = str(
                    _safe_int(self.mfcc_obj.mean[i], 100)
                )
                tags[f"{p}LIBROSA_MFCC_STD_{i:02d}"] = str(
                    _safe_int(self.mfcc_obj.std[i], 100)
                )
                tags[f"{p}LIBROSA_MFCC_ENTROPY_{i:02d}"] = str(
                    _safe_int(self.mfcc_obj.entropy[i], 1000)
                )
        if self.chroma_obj is not None:
            for i in range(len(self.chroma_obj.mean)):
                tags[f"{p}LIBROSA_CHROMA_MEAN_{i:02d}"] = str(
                    _safe_int(self.chroma_obj.mean[i], 1000)
                )
                tags[f"{p}LIBROSA_CHROMA_STD_{i:02d}"] = str(
                    _safe_int(self.chroma_obj.std[i], 1000)
                )
                tags[f"{p}LIBROSA_CHROMA_ENTROPY_{i:02d}"] = str(
                    _safe_int(self.chroma_obj.entropy[i], 1000)
                )
                tags[f"{p}LIBROSA_CHROMA_PEAK_{i:02d}"] = str(
                    _safe_int(self.chroma_obj.peak[i], 1000)
                )
            tags[f"{p}LIBROSA_CHROMA_ENTROPY_ENTROPY"] = str(
                _safe_int(self.chroma_obj.entropy_entropy, 1000)
            )

        return tags

    def to_postgres_dict(self, track_id: str = "mix") -> dict[str, Any]:
        scalars: dict[str, Any] = {
            "rms_mean": float(self.rms_mean),
            "rms_peak": float(self.rms_peak),
            "energy": float(self.energy),
            "bpm": float(self.bpm),
            "beat_regularity": float(self.beat_regularity)
            if self.beat_regularity is not None
            else None,
            "dominant_pitch": self.dominant_pitch,
            "spectral_centroid_mean": float(self.spectral_centroid_mean),
            "spectral_centroid_sd": float(self.spectral_centroid_sd),
            "spectral_bandwidth": float(self.spectral_bandwidth),
            "flatness": float(self.flatness),
            "rolloff": float(self.rolloff),
            "zcr": float(self.zcr),
            "snr": float(self.snr) if self.snr is not None else None,
            "hnr": float(self.hnr),
            "contrast": [float(v) for v in self.contrast_bands],
            "mfcc": [float(v) for v in self.mfccs],
            "section_count": self.section.section_count,
            "section_length_std": float(self.section.section_length_std),
            "drop_position": float(self.section.drop_position),
            "swing_ratio": float(self.groove.swing_ratio),
            "syncopation_index": float(self.groove.syncopation_index),
            "groove_class": self.groove.groove_class,
            "crest_factor": float(self.crest_factor),
        }

        sequences: dict[str, Any] = {}

        if self.tonnetz is not None:
            scalars["tonnetz_mean"] = self.tonnetz.mean
            scalars["tonnetz_std"] = self.tonnetz.std
            scalars["tonnetz_delta_mean"] = self.tonnetz.delta_mean
            sequences["tonnetz"] = self.tonnetz.seq

        if self.temporal_seq is not None:
            ts = self.temporal_seq
            scalars["centroid_seq_mean"] = float(ts.centroid_mean)
            scalars["centroid_seq_std"] = float(ts.centroid_std)
            scalars["chroma_entropy_mean"] = float(ts.chroma_entropy_mean)
            scalars["chroma_entropy_std"] = float(ts.chroma_entropy_std)
            scalars["centroid_delta_mean"] = float(ts.centroid_delta_mean)
            scalars["centroid_delta_std"] = float(ts.centroid_delta_std)

            sequences["centroid"] = ts.centroid_seq
            sequences["rms"] = ts.rms_seq
            sequences["chroma_entropy"] = ts.chroma_entropy_seq
            sequences["centroid_delta"] = ts.centroid_delta_seq
            sequences["dynamics_range"] = ts.dynamics_range_seq

        if self.key_feat is not None:
            kf = self.key_feat
            scalars["key"] = kf.key
            scalars["scale"] = kf.scale
            scalars["key_strength"] = float(kf.key_strength)
            scalars["key_strength_mean"] = float(kf.key_strength_mean)
            scalars["key_strength_std"] = float(kf.key_strength_std)
            sequences["key_strength"] = kf.key_strength_seq

        if self.tempogram_feat is not None:
            tf = self.tempogram_feat
            scalars["tempogram_mean"] = float(tf.mean)
            scalars["tempogram_std"] = float(tf.std)
            scalars["tempogram_peak"] = float(tf.peak)
            scalars["tempogram_entropy"] = float(tf.entropy)
            sequences["tempogram_tempo"] = tf.tempo_seq

        if self.onset_feat is not None:
            of = self.onset_feat
            scalars["onset_mean"] = float(of.mean)
            scalars["onset_std"] = float(of.std)
            scalars["onset_max"] = float(of.max)
            scalars["onset_p25"] = float(of.p25)
            scalars["onset_p50"] = float(of.p50)
            scalars["onset_p75"] = float(of.p75)
            scalars["onset_crest"] = float(of.crest)
            sequences["onset_autocorr"] = of.autocorr

        if self.rms_obj is not None:
            scalars["rms"] = {
                "mean": float(self.rms_obj.mean),
                "std": float(self.rms_obj.std),
                "entropy": float(self.rms_obj.entropy),
                "peak": float(self.rms_obj.peak),
            }
            sequences["rms_detail"] = self.rms_obj.seq
        if self.centroid_obj is not None:
            scalars["spectral_centroid"] = {
                "mean": float(self.centroid_obj.mean),
                "std": float(self.centroid_obj.std),
                "entropy": float(self.centroid_obj.entropy),
                "peak": float(self.centroid_obj.peak),
            }
            sequences["spectral_centroid_detail"] = self.centroid_obj.seq
        if self.mfcc_obj is not None:
            scalars["mfcc_detail"] = {
                "mean": [float(v) for v in self.mfcc_obj.mean],
                "std": [float(v) for v in self.mfcc_obj.std],
                "entropy": [float(v) for v in self.mfcc_obj.entropy],
            }
            sequences["mfcc_detail"] = [
                [float(x) for x in dim_seq] for dim_seq in self.mfcc_obj.seq
            ]
        if self.chroma_obj is not None:
            scalars["chroma"] = {
                "mean": [float(v) for v in self.chroma_obj.mean],
                "std": [float(v) for v in self.chroma_obj.std],
                "entropy": [float(v) for v in self.chroma_obj.entropy],
                "peak": [float(v) for v in self.chroma_obj.peak],
                "entropy_mean": float(self.chroma_obj.entropy_mean),
                "entropy_std": float(self.chroma_obj.entropy_std),
                "entropy_entropy": float(self.chroma_obj.entropy_entropy),
            }
            sequences["chroma_detail"] = [
                [float(x) for x in dim_seq] for dim_seq in self.chroma_obj.seq
            ]
            sequences["chroma_entropy_detail"] = self.chroma_obj.entropy_seq

        return {"source": track_id, "scalars": scalars, "sequences": sequences}


def _calc_rms_features(ctx: AudioContext) -> RmsFeatures:
    if ctx.spectro is None or ctx.spectro.size == 0:
        return RmsFeatures()
    rms = np.sqrt(np.mean(ctx.spectro**2, axis=0, dtype=np.float32))
    mean = float(np.mean(rms))
    std = float(np.std(rms))
    seq = _resample_to_fixed_frames(rms)
    entropy = _calc_time_entropy(seq)
    peak = float(np.max(rms))
    return RmsFeatures(mean=mean, std=std, entropy=entropy, seq=seq, peak=peak)


def _calc_centroid_features(ctx: AudioContext) -> SpectralCentroidFeatures:
    if ctx.centroid is None or ctx.centroid.size == 0:
        return SpectralCentroidFeatures()
    cent = ctx.centroid
    mean = float(np.mean(cent))
    std = float(np.std(cent))
    seq = _resample_to_fixed_frames(cent)
    entropy = _calc_time_entropy(seq)
    peak = float(np.max(cent))
    return SpectralCentroidFeatures(
        mean=mean, std=std, entropy=entropy, seq=seq, peak=peak
    )


def _calc_mfcc_features(ctx: AudioContext) -> MfccFeatures:
    log_mel = librosa.power_to_db(ctx.mel, ref=np.max)
    mfcc = librosa.feature.mfcc(S=log_mel, n_mfcc=8)
    means = np.mean(mfcc, axis=1).tolist()
    stds = np.std(mfcc, axis=1).tolist()

    seqs = []
    entropies = []
    for i in range(8):
        dim_seq = _resample_to_fixed_frames(mfcc[i])
        seqs.append(dim_seq)
        entropies.append(_calc_time_entropy(dim_seq))

    return MfccFeatures(mean=means, std=stds, entropy=entropies, seq=seqs)


def _calc_chroma_features(ctx: AudioContext) -> ChromaFeatures:
    chroma = ctx.chroma
    means = np.mean(chroma, axis=1).tolist()
    stds = np.std(chroma, axis=1).tolist()

    seqs = []
    entropies = []
    peaks = []
    for i in range(12):
        dim_seq = _resample_to_fixed_frames(chroma[i])
        seqs.append(dim_seq)
        entropies.append(_calc_time_entropy(dim_seq))
        peaks.append(float(np.max(chroma[i])))

    p = chroma / (chroma.sum(axis=0, keepdims=True) + 1e-10)
    frame_entropies = -np.sum(p * np.log2(p + 1e-10), axis=0)

    entropy_mean = float(np.mean(frame_entropies))
    entropy_std = float(np.std(frame_entropies))
    entropy_seq = _resample_to_fixed_frames(frame_entropies)
    entropy_entropy = _calc_time_entropy(entropy_seq)

    return ChromaFeatures(
        mean=means,
        std=stds,
        entropy=entropies,
        seq=seqs,
        peak=peaks,
        entropy_mean=entropy_mean,
        entropy_std=entropy_std,
        entropy_entropy=entropy_entropy,
        entropy_seq=entropy_seq,
    )


def _calc_rms_mean(ctx: AudioContext) -> float:
    rms = librosa.feature.rms(S=ctx.spectro)
    return float(np.mean(rms))


def _calc_rms_peak(ctx: AudioContext) -> float:
    rms = librosa.feature.rms(S=ctx.spectro)
    return float(np.max(rms))


def _calc_energy(ctx: AudioContext) -> float:
    if len(ctx.y) == 0:
        return 0.0
    return float(np.sqrt(np.dot(ctx.y, ctx.y) / len(ctx.y)))


def _calc_bpm(ctx: AudioContext) -> float:
    return ctx.tempobeat[0]


def _calc_beat_regularity(ctx: AudioContext) -> float | None:
    bpm, beats = ctx.tempobeat
    if len(beats) > 1:
        ibi = np.diff(librosa.frames_to_time(beats, sr=ctx.sr))
        return float(np.std(ibi) / np.mean(ibi)) if ibi.mean() > 0 else 0.0
    return None


def _calc_beat_stability(ctx: AudioContext) -> float:
    bpm, beats = ctx.tempobeat
    if len(beats) > 1:
        ibi = np.diff(librosa.frames_to_time(beats, sr=ctx.sr))
        if len(ibi) > 0 and np.mean(ibi) > 0:
            cv = np.std(ibi) / np.mean(ibi)
            return float(1.0 / (1.0 + cv))
    return 0.0


def _calc_dominant_pitch(ctx: AudioContext) -> str:
    chroma_mean = np.mean(ctx.chroma, axis=1)
    return NOTES[int(np.argmax(chroma_mean))]


def _calc_spectral_centroid_mean(ctx: AudioContext) -> float:
    return float(np.mean(ctx.centroid))


def _calc_spectral_centroid_sd(ctx: AudioContext) -> float:
    return float(np.std(ctx.centroid))


def _calc_spectral_bandwidth(ctx: AudioContext) -> float:
    if ctx.spectro is None or ctx.spectro.size == 0 or ctx.centroid is None:
        return 0.0

    spectro = ctx.spectro
    sr = ctx.sr
    n_fft = (spectro.shape[0] - 1) * 2
    freqs = librosa.fft_frequencies(sr=sr, n_fft=n_fft).astype(np.float32)

    total_energy = np.sum(spectro, axis=0, dtype=np.float32)
    valid_mask = total_energy > 1e-8

    if not np.any(valid_mask):
        return 0.0

    # (f - c)^2 の加重平均を展開: E[(f - c)^2] = E[f^2] - c^2
    # 巨大な (n_bins, n_frames) の中間ブロードキャスト配列を作らず O(K * N) 行列ベクトル積で float32 直射計算
    freqs_sq = freqs ** 2
    second_moment = np.dot(freqs_sq, spectro) / (total_energy + 1e-12)
    bandwidth_sq = second_moment - (ctx.centroid ** 2)
    bandwidth = np.sqrt(np.maximum(bandwidth_sq, 0.0, dtype=np.float32), dtype=np.float32)

    return float(np.mean(bandwidth[valid_mask])) if np.any(valid_mask) else 0.0


def _calc_flatness(ctx: AudioContext) -> float:
    if ctx.power is None or ctx.power.size == 0:
        return 0.0
    power = ctx.power
    log_p = np.log(power + 1e-12)
    geom_mean = np.exp(np.mean(log_p, axis=0, dtype=np.float32))
    arith_mean = np.mean(power, axis=0, dtype=np.float32)
    flatness_seq = geom_mean / (arith_mean + 1e-12)
    return float(np.mean(flatness_seq))


def _calc_rolloff_features(ctx: AudioContext) -> SpectralRolloffFeatures:
    spectro = ctx.spectro
    total_energy = np.sum(spectro, axis=0)
    threshold = 0.85 * total_energy
    cum_energy = np.cumsum(spectro, axis=0)
    freqs = librosa.fft_frequencies(sr=ctx.sr, n_fft=2048).astype(np.float32)
    mask = cum_energy >= threshold[np.newaxis, :]
    idx = np.argmax(mask, axis=0)
    rolloff = freqs[idx]
    rolloff = np.where(total_energy <= 0.0, 0.0, rolloff)

    if len(rolloff) == 0:
        return SpectralRolloffFeatures()
    mean_val = float(np.mean(rolloff))
    std_val = float(np.std(rolloff))
    seq = _resample_to_fixed_frames(rolloff, n=32)
    return SpectralRolloffFeatures(mean=mean_val, std=std_val, seq=seq)


def _calc_contrast(ctx: AudioContext) -> list[float]:
    contrast = librosa.feature.spectral_contrast(S=ctx.spectro, sr=ctx.sr)
    return [float(val) for val in np.mean(contrast, axis=1)]


def _calc_zcr_features(ctx: AudioContext) -> ZcrFeatures:
    if ctx.y is None or len(ctx.y) == 0:
        return ZcrFeatures()
    frames = librosa.util.frame(ctx.y, frame_length=2048, hop_length=512)
    zcr = np.mean(np.diff(np.signbit(frames), axis=0) != 0, axis=0, dtype=np.float32)
    if len(zcr) == 0:
        return ZcrFeatures()
    mean_val = float(np.mean(zcr))
    std_val = float(np.std(zcr))
    seq = _resample_to_fixed_frames(zcr, n=32)
    return ZcrFeatures(mean=mean_val, std=std_val, seq=seq)


def _calc_snr(ctx: AudioContext) -> float | None:
    if ctx.source != "mix" or ctx.y is None or len(ctx.y) == 0:
        return 0.0
    sig_pwr = float(np.dot(ctx.y, ctx.y) / len(ctx.y))
    noise_est = np.empty_like(ctx.y)
    noise_est[0] = ctx.y[0]
    noise_est[1:] = ctx.y[1:] - 0.97 * ctx.y[:-1]
    noise_pwr = float(np.dot(noise_est, noise_est) / len(noise_est))
    if noise_pwr <= 1e-12:
        return 100.0
    snr = 10.0 * np.log10(sig_pwr / (noise_pwr + 1e-12))
    return float(snr)


def _calc_mfccs(ctx: AudioContext) -> list[float]:
    log_mel = librosa.power_to_db(ctx.mel, ref=np.max)
    mfcc = librosa.feature.mfcc(S=log_mel, n_mfcc=8)
    return [float(val) for val in np.mean(mfcc, axis=1)]


def _calc_tonnetz(ctx: AudioContext) -> TonnetzFeatures | None:
    if ctx.source == "drums":
        return None

    with LIBROSA_LOCK:
        t = librosa.feature.tonnetz(chroma=ctx.chroma_cqt)
        delta_t = librosa.feature.delta(t)

    mean = np.mean(t, axis=1).tolist()
    std = np.std(t, axis=1).tolist()
    delta_mean = np.mean(delta_t, axis=1).tolist()

    T_len = t.shape[1]
    x_new = np.linspace(0, 1, FIXED_SEQ_FRAMES)
    x_old = np.linspace(0, 1, T_len)
    seq_2d = np.stack(
        [np.interp(x_new, x_old, t[i]) for i in range(6)]
    )
    seq = seq_2d.T.flatten().tolist()

    return TonnetzFeatures(mean=mean, std=std, delta_mean=delta_mean, seq=seq)


def _calc_hnr_nap(ctx: AudioContext) -> float:
    if len(ctx.y) < 2048:
        return 0.0

    with LIBROSA_LOCK:
        lag_min = int(ctx.sr / 2000)
        lag_max_val = int(ctx.sr / 50)

        frame_len = 2048
        hop_len = 1024

        if len(ctx.y) < frame_len:
            y_pad = np.pad(ctx.y, (0, frame_len - len(ctx.y)))
            frames = y_pad[:, np.newaxis]
        else:
            frames = librosa.util.frame(
                ctx.y, frame_length=frame_len, hop_length=hop_len
            )

        x = frames.T
        n_frames, N = x.shape
        n_fft = 2 * N

        CHUNK = 4096
        lag_max = min(lag_max_val, N - 1)
        if lag_min >= lag_max:
            return 0.0

        all_naps = []
        all_valid = []

        for start in range(0, n_frames, CHUNK):
            end = min(start + CHUNK, n_frames)
            x_chunk = x[start:end]

            X_chunk = np.fft.rfft(x_chunk, n=n_fft, axis=-1).astype(np.complex64, copy=False)
            S_chunk = (X_chunk * np.conj(X_chunk)).real.astype(np.float32, copy=False)
            r_chunk = np.fft.irfft(S_chunk, n=n_fft, axis=-1)[:, :N].astype(np.float32, copy=False)

            r_0_chunk = r_chunk[:, 0:1]
            valid_mask = r_0_chunk[:, 0] > 1e-10

            r_norm_chunk = np.zeros_like(r_chunk)
            r_norm_chunk[valid_mask] = r_chunk[valid_mask] / r_0_chunk[valid_mask]

            r_search = r_norm_chunk[:, lag_min : lag_max + 1]
            naps_chunk = np.max(r_search, axis=-1)
            naps_chunk = np.clip(naps_chunk, 0.0, 1.0)

            all_naps.append(naps_chunk)
            all_valid.append(valid_mask)

        all_naps_arr = np.concatenate(all_naps)
        all_valid_arr = np.concatenate(all_valid)

        if np.any(all_valid_arr):
            hnr_val = float(np.mean(all_naps_arr[all_valid_arr]))
        else:
            hnr_val = 0.0

        return hnr_val


def _calc_section_features(ctx: AudioContext) -> SectionFeatures:
    if ctx.source != "mix":
        return SectionFeatures()

    try:
        onset_env = ctx.onset_env

        if len(onset_env) < 20:
            return SectionFeatures()

        drop_position = float(np.argmax(onset_env)) / max(len(onset_env), 1)

        window = max(int(len(onset_env) * 0.05), 10)
        n_frames = len(onset_env)

        local_rms = np.array(
            [
                np.sqrt(np.mean(onset_env[max(0, i - window) : i + window] ** 2))
                for i in range(0, n_frames, window // 2)
            ]
        )

        if len(local_rms) < 3:
            return SectionFeatures(drop_position=drop_position)

        diffs = np.abs(np.diff(local_rms))
        duration_sec = len(ctx.y) / ctx.sr

        max_sections = max(1, int(duration_sec / 20))
        k = min(max_sections, len(diffs))

        if k < 1:
            return SectionFeatures(
                section_count=1,
                section_length_std=0.0,
                drop_position=drop_position,
            )

        boundary_indices = np.argsort(diffs)[-k:]
        boundary_indices = np.sort(boundary_indices)

        hop_frames = window // 2
        hop_length = 512
        sec_per_frame = hop_frames * hop_length / ctx.sr

        boundary_secs = [float(b * sec_per_frame) for b in boundary_indices]
        boundary_secs = [b for b in boundary_secs if 0 < b < duration_sec]

        times = [0.0] + boundary_secs + [duration_sec]
        lengths = [times[i + 1] - times[i] for i in range(len(times) - 1)]

        section_count = len(lengths)
        section_length_std = float(np.std(lengths)) if len(lengths) > 1 else 0.0

        return SectionFeatures(
            section_count=section_count,
            section_length_std=section_length_std,
            drop_position=drop_position,
        )

    except Exception as e:
        logging.exception(
            f"[Section] セクション特徴量算出エラー (source: {ctx.source}): {e}"
        )
        return SectionFeatures()


def _calc_groove_features(ctx: AudioContext) -> GrooveFeatures:
    if ctx.source != "mix":
        return GrooveFeatures()

    try:
        bpm, beats = ctx.tempobeat

        if len(beats) < 4:
            return GrooveFeatures()

        beat_times = librosa.frames_to_time(beats, sr=ctx.sr)
        ibi = np.diff(beat_times)

        if len(ibi) >= 2:
            d1 = ibi[0::2]
            d2 = ibi[1::2]
            min_len = min(len(d1), len(d2))
            if min_len > 0:
                SR = float(np.mean(d1[:min_len]) / (np.mean(d2[:min_len]) + 1e-10))
                SR = float(np.clip(SR, 0.5, 4.0))
            else:
                SR = 1.0
        else:
            SR = 1.0

        with LIBROSA_LOCK:
            onset_frames = librosa.onset.onset_detect(
                onset_envelope=ctx.onset_env, sr=ctx.sr
            )

        SI = 0.0
        if len(beats) > 1 and len(onset_frames) > 0:
            beat_period = float(np.mean(np.diff(beats)))
            if beat_period > 0:
                distances = np.array(
                    [
                        np.min(np.abs(onset_frames[i] - beats))
                        for i in range(len(onset_frames))
                    ],
                    dtype=float,
                )
                phase = distances / beat_period
                SI = float(np.mean(np.minimum(phase, 1.0 - phase)))
                SI = float(np.clip(SI, 0.0, 0.5))

        if SR < 1.15:
            gc = "straight"
        elif SR < 1.7:
            gc = "swing"
        else:
            gc = "heavy_swing"

        return GrooveFeatures(
            swing_ratio=SR,
            syncopation_index=SI,
            groove_class=gc,
        )

    except Exception as e:
        logging.exception(f"[Groove] Groove特徴量算出にてエラーが発生いたしましたわ (source: {ctx.source}): {e}")
        return GrooveFeatures()


def _calc_crest_factor(ctx: AudioContext) -> float:
    if ctx.y is None or len(ctx.y) == 0:
        return 0.0
    eps = 1e-10
    peak = float(np.max(np.abs(ctx.y)))
    rms = float(np.sqrt(np.dot(ctx.y, ctx.y) / len(ctx.y)) + eps)
    return float(np.clip(peak / rms, 0.0, 100.0))


def _calc_temporal_seq(ctx: AudioContext) -> TemporalSeqFeatures | None:
    try:
        if ctx.source == "mix":
            rms = librosa.feature.rms(S=ctx.spectro)[0]
        else:
            with LIBROSA_LOCK:
                rms = librosa.feature.rms(y=ctx.y, frame_length=2048, hop_length=512)[0]

        y_pad = np.pad(ctx.y, 1024, mode="constant")
        from numpy.lib.stride_tricks import sliding_window_view

        y_frames = sliding_window_view(y_pad, 2048)[::512]

        peaks = np.max(np.abs(y_frames), axis=1)
        min_len = min(len(peaks), len(rms))
        peaks = peaks[:min_len]
        rms_aligned = rms[:min_len]

        crest_seq = peaks / (rms_aligned + 1e-10)
        crest_seq = np.clip(crest_seq, 0.0, 100.0)
        dynamics_range_seq = _resample_to_fixed_frames(crest_seq)

        centroid = ctx.centroid

        chroma = ctx.chroma
        p = chroma / (chroma.sum(axis=0, keepdims=True) + 1e-10)
        entropy = -np.sum(p * np.log2(p + 1e-10), axis=0)

        with LIBROSA_LOCK:
            delta = librosa.feature.delta(centroid)

        centroid_mean = float(np.mean(centroid))
        centroid_std = float(np.std(centroid))
        centroid_seq = _resample_to_fixed_frames(centroid)
        chroma_entropy_mean = float(np.mean(entropy))
        chroma_entropy_std = float(np.std(entropy))
        chroma_entropy_seq = _resample_to_fixed_frames(entropy)
        centroid_delta_mean = float(np.mean(delta))
        centroid_delta_std = float(np.std(delta))
        centroid_delta_seq = _resample_to_fixed_frames(delta)

        return TemporalSeqFeatures(
            centroid_mean=centroid_mean,
            centroid_std=centroid_std,
            centroid_seq=centroid_seq,
            rms_seq=_resample_to_fixed_frames(rms),
            chroma_entropy_mean=chroma_entropy_mean,
            chroma_entropy_std=chroma_entropy_std,
            chroma_entropy_seq=chroma_entropy_seq,
            centroid_delta_mean=centroid_delta_mean,
            centroid_delta_std=centroid_delta_std,
            centroid_delta_seq=centroid_delta_seq,
            dynamics_range_seq=dynamics_range_seq,
        )

    except Exception as e:
        logging.exception(
            f"[TemporalSeq] 時系列特徴量算出エラー (source: {ctx.source}): {e}"
        )
        return None


def _calc_key_features(ctx: AudioContext) -> KeyFeatures:
    if ctx.source != "mix":
        return KeyFeatures()

    try:
        chroma = ctx.chroma_cqt
        if chroma.shape[1] == 0:
            return KeyFeatures()

        chroma_mean = np.mean(chroma, axis=1)

        chroma_std = np.std(chroma_mean)
        if chroma_std > 1e-10:
            chroma_norm = (chroma_mean - np.mean(chroma_mean)) / chroma_std
        else:
            chroma_norm = chroma_mean - np.mean(chroma_mean)

        best_corr = -2.0
        best_key = 0
        best_scale = "major"

        for scale_name in ["major", "minor"]:
            profile = np.array(KEY_PROFILES[scale_name])
            prof_norm = (profile - np.mean(profile)) / (np.std(profile) + 1e-10)

            for shift in range(12):
                t_rotated = np.roll(prof_norm, shift)
                corr = float(np.dot(chroma_norm, t_rotated) / 12.0)
                if corr > best_corr:
                    best_corr = corr
                    best_key = shift
                    best_scale = scale_name

        key_name = NOTES[best_key]

        target_profile = np.array(KEY_PROFILES[best_scale])
        target_prof_norm = np.roll(
            (target_profile - np.mean(target_profile))
            / (np.std(target_profile) + 1e-10),
            best_key,
        )

        T_len = chroma.shape[1]
        corrs_t = []
        for t in range(T_len):
            c_t = chroma[:, t]
            c_t_std = np.std(c_t)
            if c_t_std > 1e-10:
                c_t_norm = (c_t - np.mean(c_t)) / c_t_std
            else:
                c_t_norm = c_t - np.mean(c_t)
            corr_t = float(np.dot(c_t_norm, target_prof_norm) / 12.0)
            corrs_t.append(corr_t)

        corrs_t_arr = np.array(corrs_t)
        seq = _resample_to_fixed_frames(corrs_t_arr, n=FIXED_SEQ_FRAMES)
        mean_seq = float(np.mean(corrs_t_arr))
        std_seq = float(np.std(corrs_t_arr))

        return KeyFeatures(
            key=key_name,
            scale=best_scale,
            key_strength=float(np.clip(best_corr, -1.0, 1.0)),
            key_strength_mean=mean_seq,
            key_strength_std=std_seq,
            key_strength_seq=seq,
        )

    except Exception as e:
        logging.exception(f"[Key] キー特徴量算出にてエラーが発生いたしましたわ (source: {ctx.source}): {e}")
        return KeyFeatures()


def _calc_tempogram_features(ctx: AudioContext) -> TempogramFeatures:
    try:
        tempogram = ctx.tempogram

        if tempogram.size == 0:
            return TempogramFeatures()

        mean_val = float(np.mean(tempogram))
        std_val = float(np.std(tempogram))

        peak_val = float(np.mean(np.max(tempogram, axis=0)))

        p = tempogram / (np.sum(tempogram, axis=0, keepdims=True) + 1e-10)
        entropy = -np.sum(p * np.log2(p + 1e-10), axis=0)
        entropy_val = float(np.mean(entropy))

        best_bins = np.argmax(tempogram[1:], axis=0) + 1
        with LIBROSA_LOCK:
            frequencies = librosa.tempo_frequencies(
                tempogram.shape[0], sr=ctx.sr, hop_length=512
            )
        tempo_seq_bpm = frequencies[best_bins]
        tempo_seq = _resample_to_fixed_frames(tempo_seq_bpm)

        return TempogramFeatures(
            mean=mean_val,
            std=std_val,
            peak=peak_val,
            entropy=entropy_val,
            tempo_seq=tempo_seq,
        )

    except Exception as e:
        logging.exception(
            f"[Tempogram] テンポグラム特徴量算出エラー (source: {ctx.source}): {e}"
        )
        return TempogramFeatures()


def _calc_onset_features(ctx: AudioContext) -> OnsetFeatures:
    if ctx.source != "mix":
        return OnsetFeatures()

    try:
        onset_env = ctx.onset_env

        if len(onset_env) == 0:
            return OnsetFeatures()

        mean_val = float(np.mean(onset_env))
        std_val = float(np.std(onset_env))
        max_val = float(np.max(onset_env))

        p25 = float(np.percentile(onset_env, 25))
        p50 = float(np.percentile(onset_env, 50))
        p75 = float(np.percentile(onset_env, 75))

        crest = max_val / (mean_val + 1e-10)

        diff = onset_env - mean_val
        if std_val > 1e-10:
            skew_val = float(np.mean(diff**3) / (std_val**3))
            kurt_val = float(np.mean(diff**4) / (std_val**4) - 3.0)
        else:
            skew_val = 0.0
            kurt_val = 0.0

        with LIBROSA_LOCK:
            ac = librosa.autocorrelate(onset_env, max_size=160)

        autocorr_seq = _resample_to_fixed_frames(ac, n=16)
        onset_strength_seq = _resample_to_fixed_frames(onset_env, n=32)

        return OnsetFeatures(
            mean=mean_val,
            std=std_val,
            max=max_val,
            p25=p25,
            p50=p50,
            p75=p75,
            crest=crest,
            autocorr=autocorr_seq,
            skew=skew_val,
            kurt=kurt_val,
            onset_strength_seq=onset_strength_seq,
        )

    except Exception as e:
        logging.exception(
            f"[Onset] オンセット強度特徴量算出エラー (source: {ctx.source}): {e}"
        )
        return OnsetFeatures()


# ─────────────────────────────────────────────
# FeatureExtractor インスタンス群
# ─────────────────────────────────────────────
extract_rms_mean = FeatureExtractor(_calc_rms_mean, "rms_mean")
extract_rms_peak = FeatureExtractor(_calc_rms_peak, "rms_peak")
extract_energy = FeatureExtractor(_calc_energy, "energy")
extract_bpm = FeatureExtractor(_calc_bpm, "bpm")
extract_beat_regularity = FeatureExtractor(_calc_beat_regularity, "beat_regularity")
extract_beat_stability = FeatureExtractor(_calc_beat_stability, "beat_stability")
extract_dominant_pitch = FeatureExtractor(_calc_dominant_pitch, "dominant_pitch")
extract_spectral_centroid_mean = FeatureExtractor(
    _calc_spectral_centroid_mean, "spectral_centroid_mean"
)
extract_spectral_centroid_sd = FeatureExtractor(
    _calc_spectral_centroid_sd, "spectral_centroid_sd"
)
extract_spectral_bandwidth = FeatureExtractor(
    _calc_spectral_bandwidth, "spectral_bandwidth"
)
extract_flatness = FeatureExtractor(_calc_flatness, "flatness")
extract_rolloff = FeatureExtractor(_calc_rolloff_features, "rolloff")
extract_contrast = FeatureExtractor(_calc_contrast, "contrast")
extract_zcr = FeatureExtractor(_calc_zcr_features, "zcr")
extract_snr = FeatureExtractor(_calc_snr, "snr")
extract_hnr = FeatureExtractor(lambda ctx: ctx.hnr, "hnr")
extract_mfccs = FeatureExtractor(_calc_mfccs, "mfccs")
extract_tonnetz = FeatureExtractor(_calc_tonnetz, "tonnetz")
extract_section = FeatureExtractor(_calc_section_features, "section")
extract_groove = FeatureExtractor(_calc_groove_features, "groove")
extract_crest_factor = FeatureExtractor(_calc_crest_factor, "crest_factor")
extract_temporal_seq = FeatureExtractor(_calc_temporal_seq, "temporal_seq")
extract_key = FeatureExtractor(_calc_key_features, "key")
extract_tempogram = FeatureExtractor(_calc_tempogram_features, "tempogram")
extract_onset = FeatureExtractor(_calc_onset_features, "onset")
extract_rms_obj = FeatureExtractor(_calc_rms_features, "rms_obj")
extract_centroid_obj = FeatureExtractor(_calc_centroid_features, "centroid_obj")
extract_mfcc_obj = FeatureExtractor(_calc_mfcc_features, "mfcc_obj")
extract_chroma_obj = FeatureExtractor(_calc_chroma_features, "chroma_obj")
extract_scipy_stats = FeatureExtractor(_calc_scipy_stats_features, "scipy_stats")
extract_hilbert = FeatureExtractor(_calc_hilbert_features, "hilbert")
extract_peak = FeatureExtractor(_calc_peak_features, "peak")


# ─────────────────────────────────────────────
# librosa_extractor → RawFeatures 変換 (v4)
# ─────────────────────────────────────────────
def _build_raw_features(
    rms_mean,
    rms_peak,
    energy,
    bpm,
    beat_regularity,
    beat_stability,
    dominant_pitch,
    spectral_centroid_mean,
    spectral_centroid_sd,
    spectral_bandwidth,
    flatness,
    rolloff,
    contrast,
    zcr,
    snr,
    hnr,
    mfccs,
    tonnetz,
    section,
    groove,
    crest_factor,
    temporal_seq,
    key_feat,
    tempogram_feat,
    onset_feat,
    rms_obj,
    centroid_obj,
    mfcc_obj,
    chroma_obj,
    scipy_stats_feat,
    hilbert_feat,
    peak_feat,
    ctx: AudioContext,
) -> RawFeatures:
    """Product合成結果から RawFeatures を構築しますわ！"""
    rms_stats = _calc_rms_stats(rms_obj.seq) if rms_obj else {}
    centroid_stats = _calc_centroid_stats(centroid_obj.seq) if centroid_obj else {}

    tonnetz_seq_2d = []
    if tonnetz is not None:
        t = tonnetz.seq
        tonnetz_seq_2d = [t[i * 32 : (i + 1) * 32] for i in range(6)]

    chroma_seq_2d = []
    if chroma_obj is not None:
        chroma_seq_2d = chroma_obj.seq

    mfcc_seq_2d = []
    if mfcc_obj is not None:
        mfcc_seq_2d = mfcc_obj.seq

    chord_seq = _calc_chord_sequence(ctx)
    f0_seq = _calc_vocal_f0_seq(ctx) if ctx.source == "vocals" else None

    rolloff_mean_val = rolloff.mean if rolloff else 0.0
    rolloff_std_val = rolloff.std if rolloff else 0.0
    rolloff_seq_val = rolloff.seq if rolloff else []

    zcr_mean_val = zcr.mean if zcr else 0.0
    zcr_std_val = zcr.std if zcr else 0.0
    zcr_seq_val = zcr.seq if zcr else []

    return RawFeatures(
        energy=energy,
        bpm=float(bpm) if bpm else 0.0,
        crest_factor=crest_factor,
        snr=snr,
        hnr=hnr,
        rms_mean=rms_stats.get("mean", float(rms_mean)),
        rms_std=rms_stats.get("std", float(np.std(rms_obj.seq) if rms_obj else 0)),
        rms_peak=rms_stats.get("peak", float(rms_peak)),
        rms_max=rms_stats.get("max", float(rms_peak)),
        rms_min=rms_stats.get("min", 0.0),
        rms_median=rms_stats.get("median", 0.0),
        rms_entropy=rms_stats.get("entropy", 0.0),
        centroid_mean=centroid_stats.get("mean", float(spectral_centroid_mean)),
        centroid_std=centroid_stats.get("std", float(spectral_centroid_sd)),
        centroid_peak=centroid_stats.get("peak", 0.0),
        centroid_max=centroid_stats.get("max", 0.0),
        centroid_min=centroid_stats.get("min", 0.0),
        centroid_median=centroid_stats.get("median", 0.0),
        centroid_entropy=centroid_stats.get("entropy", 0.0),
        beat_regularity=beat_regularity,
        dominant_pitch=dominant_pitch,
        onset_feat=onset_feat,
        tempogram_feat=tempogram_feat,
        rolloff_mean=rolloff_mean_val,
        rolloff_std=rolloff_std_val,
        rolloff_seq=rolloff_seq_val,
        beat_stability=beat_stability,
        spectral_bandwidth=float(spectral_bandwidth),
        flatness=float(flatness),
        zcr_mean=zcr_mean_val,
        zcr_std=zcr_std_val,
        zcr_seq=zcr_seq_val,
        contrast_bands=[float(val) for val in contrast],
        mfccs=[float(val) for val in mfccs],
        section=section if isinstance(section, SectionFeatures) else SectionFeatures(),
        groove=groove if isinstance(groove, GrooveFeatures) else GrooveFeatures(),
        key_feat=key_feat if isinstance(key_feat, KeyFeatures) else KeyFeatures(),
        rms_seq=temporal_seq.rms_seq if temporal_seq else [],
        centroid_seq=temporal_seq.centroid_seq if temporal_seq else [],
        centroid_delta_seq=temporal_seq.centroid_delta_seq if temporal_seq else [],
        dynamics_range_seq=temporal_seq.dynamics_range_seq if temporal_seq else [],
        tempogram_tempo=tempogram_feat.tempo_seq
        if isinstance(tempogram_feat, TempogramFeatures)
        else [],
        key_strength_seq=key_feat.key_strength_seq
        if isinstance(key_feat, KeyFeatures)
        else [],
        tonnetz=tonnetz_seq_2d,
        chroma=chroma_seq_2d,
        mfcc=mfcc_seq_2d,
        chord_sequence=chord_seq,
        vocal_f0_seq=f0_seq,
        scipy_stats_feat=scipy_stats_feat,
        hilbert_feat=hilbert_feat,
        peak_feat=peak_feat,
    )


# ─────────────────────────────────────────────
# librosa_extractor: Product合成 (v4 → RawFeatures)
# ─────────────────────────────────────────────
_librosa_product = product_all(
    extract_rms_mean,
    extract_rms_peak,
    extract_energy,
    extract_bpm,
    extract_beat_regularity,
    extract_beat_stability,
    extract_dominant_pitch,
    extract_spectral_centroid_mean,
    extract_spectral_centroid_sd,
    extract_spectral_bandwidth,
    extract_flatness,
    extract_rolloff,
    extract_contrast,
    extract_zcr,
    extract_snr,
    extract_hnr,
    extract_mfccs,
    extract_tonnetz,
    extract_section,
    extract_groove,
    extract_crest_factor,
    extract_temporal_seq,
    extract_key,
    extract_tempogram,
    extract_onset,
    extract_rms_obj,
    extract_centroid_obj,
    extract_mfcc_obj,
    extract_chroma_obj,
    extract_scipy_stats,
    extract_hilbert,
    extract_peak,
)

librosa_extractor_v4: FeatureExtractor[RawFeatures] = FeatureExtractor(
    lambda ctx: _build_raw_features(*_librosa_product.run(ctx), ctx=ctx),
    "librosa_extractor_v4",
)

librosa_extractor: FeatureExtractor[RawFeatures] = librosa_extractor_v4
stem_extractor: FeatureExtractor[RawFeatures] = librosa_extractor


# ─────────────────────────────────────────────
# STEM_CONFIGS
# ─────────────────────────────────────────────
STEM_CONFIGS: dict[str, dict[str, Any]] = {
    "mix": {
        "warmup": [
            "stft",
            "spectro",
            "power",
            "chroma",
            "mel",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
    "drums": {
        "warmup": [
            "stft",
            "spectro",
            "power",
            "chroma",
            "mel",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
    "bass": {
        "warmup": [
            "stft",
            "spectro",
            "power",
            "chroma",
            "mel",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
    "vocals": {
        "warmup": [
            "stft",
            "spectro",
            "power",
            "chroma",
            "mel",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
    "other": {
        "warmup": [
            "stft",
            "spectro",
            "power",
            "chroma",
            "mel",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
    "guitar": {
        "warmup": [
            "stft",
            "spectro",
            "power",
            "chroma",
            "mel",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
    "piano": {
        "warmup": [
            "stft",
            "spectro",
            "power",
            "chroma",
            "mel",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
}
