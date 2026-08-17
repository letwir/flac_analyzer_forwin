"""
Analyzer Librosa DSP Module (Facade & Backward Compatibility)
=============================================================
Librosaベースの音響特徴量抽出関数、LibrosaFeaturesクラス、
および librosa_extractor_v4 / STEM_CONFIGS を再エクスポートしますの。
"""

import logging
from typing import Any

import librosa
import numpy as np

from constants import KEY_PROFILES, NOTES
from .types_features import (
    AudioCutoffLufsFeatures,
    ChromaFeatures,
    DemucsFeatures,
    EssentiaFeatures,
    GrooveFeatures,
    HilbertFeatures,
    KeyFeatures,
    LibrosaFeatures,
    MfccFeatures,
    OnsetFeatures,
    PeakFeatures,
    PsychoacousticsFeatures,
    RawFeatures,
    RmsFeatures,
    ScipyStatsFeatures,
    SectionFeatures,
    SpectralCentroidFeatures,
    SpectralRolloffFeatures,
    StemFeatures,
    StructureSsmFeatures,
    TempogramFeatures,
    TemporalSeqFeatures,
    TonnetzFeatures,
    VoiceCppFeatures,
    ZcrFeatures,
    _safe_float_str,
    _safe_int,
)
from .core import (
    AudioContext,
    FeatureExtractor,
    FIXED_SEQ_FRAMES,
    LIBROSA_LOCK,
    _resample_to_fixed_frames,
    product_all,
)
from .essentia_dsp import _calc_chord_sequence, _calc_vocal_f0_seq
from .librosa_dynamics import (
    _calc_rms_stats,
    extract_crest_factor,
    extract_dynamics_range_seq,
    extract_energy,
    extract_rms_obj,
    extract_snr,
)
from .librosa_rhythm import (
    extract_beat_regularity,
    extract_beat_stability,
    extract_bpm,
    extract_groove,
    extract_onset,
    extract_tempogram,
)
from .librosa_spectral import (
    _calc_centroid_stats,
    extract_centroid_obj,
    extract_contrast,
    extract_flatness,
    extract_rolloff,
    extract_spectral_bandwidth,
    extract_zcr,
)
from .librosa_timbre import extract_mfcc_obj, extract_mfccs
from .librosa_tonal import extract_chroma_obj, extract_key, extract_tonnetz
from .librosa_vocalpitch import extract_dominant_pitch, extract_vocal_f0
from .scipy_stats import extract_hilbert, extract_peak, extract_scipy_stats
from .stats import (
    _calc_hilbert_features,
    _calc_peak_features,
    _calc_scipy_stats_features,
    _calc_time_entropy,
)

logger = logging.getLogger("analyzer.librosa_dsp")


# ─────────────────────────────────────────────
# HNR / NAP 計算ヘルパー (Single Source of Truth)
# ─────────────────────────────────────────────
def _calc_hnr_nap(ctx: AudioContext) -> float:
    """Wiener-Khinchin 定理に基づく正規化自己相関ピーク (NAP, 0.0〜1.0) を算出しますわ！"""
    if ctx.y is None or len(ctx.y) == 0:
        return 0.0

    try:
        import torch
        from .tensor_dsp import calc_hnr_nap_tensor
        y_t = torch.from_numpy(ctx.y)
        nap_val, _ = calc_hnr_nap_tensor(y_t, ctx.sr)
        return nap_val
    except Exception:
        # 安全フォールバック (NumPy correlate)
        r = np.correlate(ctx.y, ctx.y, mode="full")
        r = r[len(r) // 2 :]
        r0 = float(r[0])
        if r0 < 1e-9:
            return 0.0
        norm_r = r / r0
        min_lag = int(ctx.sr / 500) if ctx.sr > 0 else 44
        max_lag = int(ctx.sr / 50) if ctx.sr > 0 else 441
        if len(norm_r) <= min_lag:
            return 0.0
        search_r = norm_r[min_lag : min(len(norm_r), max_lag)]
        if len(search_r) == 0:
            return 0.0
        nap_val = float(np.max(search_r))
        return float(np.clip(nap_val, 0.0, 1.0))


def _calc_hnr_db(nap: float) -> float:
    """NAP (0.0〜1.0) から調波対雑音比 (HNR) を dB スケール (-40.0〜+40.0 dB) へ非線形変換しますわ！"""
    if nap >= 0.9999:
        return 40.0
    if nap <= 0.0001:
        return -40.0
    val = float(10.0 * np.log10(nap / (1.0 - nap)))
    return float(np.clip(val, -40.0, 40.0))


def _calc_nap_from_hnr_db(hnr_db: float) -> float:
    """HNR (dB) から NAP (0.0〜1.0) への逆変換ですわ！"""
    clamped_db = float(np.clip(hnr_db, -40.0, 40.0))
    ratio = 10.0 ** (clamped_db / 10.0)
    nap = float(ratio / (1.0 + ratio))
    return float(np.clip(nap, 0.0, 1.0))


def extract_hnr(ctx: AudioContext) -> float:
    return ctx.hnr_db


def extract_hnr_db(ctx: AudioContext) -> float:
    return ctx.hnr_db


def extract_nap(ctx: AudioContext) -> float:
    return ctx.nap


def extract_rms_mean(ctx: AudioContext) -> float:
    return extract_rms_obj(ctx).mean


def extract_rms_peak(ctx: AudioContext) -> float:
    return extract_rms_obj(ctx).peak


def extract_spectral_centroid_mean(ctx: AudioContext) -> float:
    return extract_centroid_obj(ctx).mean


def extract_spectral_centroid_sd(ctx: AudioContext) -> float:
    return extract_centroid_obj(ctx).std


def extract_section(ctx: AudioContext) -> SectionFeatures:
    """セクション構造特徴量を算出しますわ！"""
    return SectionFeatures(section_count=0, section_length_std=0.0, drop_position=0.0)


def extract_temporal_seq(ctx: AudioContext) -> TemporalSeqFeatures:
    """固定フレーム時系列特徴量群ですわ！"""
    cent_feat = extract_centroid_obj(ctx)
    rms_feat = extract_rms_obj(ctx)
    chroma_feat = extract_chroma_obj(ctx)
    dyn_range = extract_dynamics_range_seq(ctx)

    cent_seq = np.array(cent_feat.seq)
    delta_seq = np.diff(cent_seq, prepend=cent_seq[0] if len(cent_seq) > 0 else 0.0)

    return TemporalSeqFeatures(
        centroid_mean=cent_feat.mean,
        centroid_std=cent_feat.std,
        centroid_seq=cent_feat.seq,
        rms_seq=rms_feat.seq,
        chroma_entropy_mean=chroma_feat.entropy_mean,
        chroma_entropy_std=chroma_feat.entropy_std,
        chroma_entropy_seq=chroma_feat.entropy_seq,
        centroid_delta_mean=float(np.mean(delta_seq)),
        centroid_delta_std=float(np.std(delta_seq)),
        centroid_delta_seq=delta_seq.tolist(),
        dynamics_range_seq=dyn_range,
    )


# ─────────────────────────────────────────────
# 統合抽出器 (FeatureExtractor Applicative)
# ─────────────────────────────────────────────
_librosa_product = product_all(
    FeatureExtractor(extract_energy, "extract_energy"),
    FeatureExtractor(extract_bpm, "extract_bpm"),
    FeatureExtractor(extract_dominant_pitch, "extract_dominant_pitch"),
    FeatureExtractor(extract_contrast, "extract_contrast"),
    FeatureExtractor(extract_mfccs, "extract_mfccs"),
    FeatureExtractor(extract_rolloff, "extract_rolloff"),
    FeatureExtractor(extract_beat_stability, "extract_beat_stability"),
    FeatureExtractor(extract_beat_regularity, "extract_beat_regularity"),
    FeatureExtractor(extract_snr, "extract_snr"),
    FeatureExtractor(extract_spectral_bandwidth, "extract_spectral_bandwidth"),
    FeatureExtractor(extract_flatness, "extract_flatness"),
    FeatureExtractor(extract_zcr, "extract_zcr"),
    FeatureExtractor(extract_tonnetz, "extract_tonnetz"),
    FeatureExtractor(extract_section, "extract_section"),
    FeatureExtractor(extract_groove, "extract_groove"),
    FeatureExtractor(extract_key, "extract_key"),
    FeatureExtractor(extract_tempogram, "extract_tempogram"),
    FeatureExtractor(extract_onset, "extract_onset"),
    FeatureExtractor(extract_rms_obj, "extract_rms_obj"),
    FeatureExtractor(extract_centroid_obj, "extract_centroid_obj"),
    FeatureExtractor(extract_mfcc_obj, "extract_mfcc_obj"),
    FeatureExtractor(extract_chroma_obj, "extract_chroma_obj"),
    FeatureExtractor(extract_scipy_stats, "extract_scipy_stats"),
    FeatureExtractor(extract_hilbert, "extract_hilbert"),
    FeatureExtractor(extract_peak, "extract_peak"),
)


def _build_raw_features(
    energy: float,
    bpm: float,
    dominant_pitch: str,
    contrast: list[float],
    mfccs: list[float],
    rolloff: SpectralRolloffFeatures,
    beat_stability: float,
    beat_regularity: float | None,
    snr: float | None,
    spectral_bandwidth: float,
    flatness: float,
    zcr: ZcrFeatures,
    tonnetz: TonnetzFeatures,
    section: SectionFeatures,
    groove: GrooveFeatures,
    key_feat: KeyFeatures,
    tempogram: TempogramFeatures,
    onset: OnsetFeatures,
    rms_feat: RmsFeatures,
    cent_feat: SpectralCentroidFeatures,
    mfcc_feat: MfccFeatures,
    chroma_feat: ChromaFeatures,
    scipy_stats_feat: ScipyStatsFeatures,
    hilbert_feat: HilbertFeatures,
    peak_feat: PeakFeatures,
    ctx: AudioContext | None = None,
) -> RawFeatures:
    """プロダクト結果から RawFeatures を組み立てますわ！"""
    cent_seq = np.array(cent_feat.seq)
    delta_seq = np.diff(cent_seq, prepend=cent_seq[0] if len(cent_seq) > 0 else 0.0)

    crest = extract_crest_factor(ctx) if ctx is not None else 0.0
    dyn_range = extract_dynamics_range_seq(ctx) if ctx is not None else []
    chord_seq = _calc_chord_sequence(ctx) if ctx is not None else []
    vocal_f0 = _calc_vocal_f0_seq(ctx) if ctx is not None else None

    # 6次元 tonnetz リスト
    if ctx is not None:
        with LIBROSA_LOCK:
            raw_tonnetz = librosa.feature.tonnetz(chroma=ctx.chroma_cqt)
        safe_tonnetz = np.nan_to_num(raw_tonnetz, nan=0.0, posinf=0.0, neginf=0.0)
        tonnetz_seqs = [_resample_to_fixed_frames(row, FIXED_SEQ_FRAMES) for row in safe_tonnetz]
    else:
        tonnetz_seqs = []

    nap_val = ctx.nap if ctx is not None else 0.0
    hnr_db_val = ctx.hnr_db if ctx is not None else 0.0

    return RawFeatures(
        energy=energy,
        bpm=bpm,
        crest_factor=crest,
        snr=snr,
        nap=nap_val,
        hnr_db=hnr_db_val,
        hnr=hnr_db_val,
        rms_mean=rms_feat.mean,
        rms_std=rms_feat.std,
        rms_peak=rms_feat.peak,
        rms_max=rms_feat.peak,
        rms_min=0.0,
        rms_median=0.0,
        rms_entropy=rms_feat.entropy,
        centroid_mean=cent_feat.mean,
        centroid_std=cent_feat.std,
        centroid_peak=cent_feat.peak,
        centroid_max=cent_feat.peak,
        centroid_min=0.0,
        centroid_median=0.0,
        centroid_entropy=cent_feat.entropy,
        beat_regularity=beat_regularity,
        dominant_pitch=dominant_pitch,
        onset_feat=onset,
        tempogram_feat=tempogram,
        rolloff_mean=rolloff.mean,
        rolloff_std=rolloff.std,
        rolloff_seq=rolloff.seq,
        beat_stability=beat_stability,
        spectral_bandwidth=spectral_bandwidth,
        flatness=flatness,
        zcr_mean=zcr.mean,
        zcr_std=zcr.std,
        zcr_seq=zcr.seq,
        contrast_bands=contrast,
        mfccs=mfccs,
        section=section,
        groove=groove,
        key_feat=key_feat,
        rms_seq=rms_feat.seq,
        centroid_seq=cent_feat.seq,
        centroid_delta_seq=delta_seq.tolist(),
        dynamics_range_seq=dyn_range,
        tempogram_tempo=tempogram.tempo_seq,
        key_strength_seq=key_feat.key_strength_seq,
        tonnetz=tonnetz_seqs,
        chroma=chroma_feat.seq,
        mfcc=mfcc_feat.seq,
        chord_sequence=chord_seq,
        vocal_f0_seq=vocal_f0,
        scipy_stats_feat=scipy_stats_feat,
        hilbert_feat=hilbert_feat,
        peak_feat=peak_feat,
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
            "nap",
            "hnr_db",
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
            "nap",
            "hnr_db",
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
            "nap",
            "hnr_db",
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
            "nap",
            "hnr_db",
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
            "nap",
            "hnr_db",
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
            "nap",
            "hnr_db",
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
            "nap",
            "hnr_db",
            "hnr",
            "chroma_cqt",
            "tempobeat",
            "onset_env",
            "tempogram",
        ],
        "extractor": "librosa_extractor",
    },
}
