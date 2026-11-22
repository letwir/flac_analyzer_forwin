"""
Analyzer Stats Module
=====================
Scipy・信号処理・統計的特徴量抽出関数（Skewness, Kurtosis, Hilbert, Peak, Entropy）
を定義しますの。
"""

import logging
import numpy as np
import scipy.signal
import scipy.stats

from .core import AudioContext, _resample_to_fixed_frames
from .types import HilbertFeatures, PeakFeatures, ScipyStatsFeatures


def _calc_time_entropy(seq: np.ndarray | list[float]) -> float:
    """非負の時系列データ seq のシャノンエントロピーを算出しますわ。"""
    abs_seq = np.abs(np.asarray(seq))
    s = np.sum(abs_seq)
    if s < 1e-10:
        p = np.ones_like(abs_seq) / len(abs_seq)
    else:
        p = abs_seq / s
    return float(-np.sum(p * np.log2(p + 1e-10)))


def _calc_scipy_stats_features(ctx: AudioContext) -> ScipyStatsFeatures | None:
    """純粋な float32 ベクトル化演算により周波数スペクトルの Skewness, Kurtosis を高速算出しますわ！"""
    if ctx.spectro is None or ctx.spectro.size == 0:
        return None
    try:
        spectro = ctx.spectro  # (n_bins, n_frames) float32
        n_bins, n_frames = spectro.shape
        if n_bins < 2 or n_frames == 0:
            return None

        skew_vals = np.zeros(n_frames, dtype=np.float32)
        kurt_vals = np.zeros(n_frames, dtype=np.float32)

        chunk_size = 16384
        for start in range(0, n_frames, chunk_size):
            end = min(n_frames, start + chunk_size)
            sub = spectro[:, start:end]
            m1 = np.mean(sub, axis=0, dtype=np.float32)
            diff = sub - m1[np.newaxis, :]
            m2 = np.mean(diff**2, axis=0, dtype=np.float32)
            std = np.sqrt(m2, dtype=np.float32)
            valid_mask = std > 1e-8
            if np.any(valid_mask):
                diff_valid = diff[:, valid_mask]
                m2_valid = m2[valid_mask]
                std_valid = std[valid_mask]
                m3_valid = np.mean(diff_valid**3, axis=0, dtype=np.float32)
                m4_valid = np.mean(diff_valid**4, axis=0, dtype=np.float32)
                s_chunk = np.zeros(end - start, dtype=np.float32)
                k_chunk = np.zeros(end - start, dtype=np.float32)
                s_chunk[valid_mask] = m3_valid / (std_valid**3 + 1e-12)
                k_chunk[valid_mask] = (m4_valid / (m2_valid**2 + 1e-12)) - 3.0
                skew_vals[start:end] = s_chunk
                kurt_vals[start:end] = k_chunk

        skew_vals = np.nan_to_num(skew_vals, nan=0.0, posinf=0.0, neginf=0.0)
        kurt_vals = np.nan_to_num(kurt_vals, nan=0.0, posinf=0.0, neginf=0.0)

        skew_seq = _resample_to_fixed_frames(skew_vals)
        kurt_seq = _resample_to_fixed_frames(kurt_vals)

        return ScipyStatsFeatures(
            skewness_mean=float(np.mean(skew_vals)),
            skewness_std=float(np.std(skew_vals)),
            skewness_peak=float(np.max(skew_vals)),
            skewness_min=float(np.min(skew_vals)),
            skewness_seq=skew_seq,
            kurtosis_mean=float(np.mean(kurt_vals)),
            kurtosis_std=float(np.std(kurt_vals)),
            kurtosis_peak=float(np.max(kurt_vals)),
            kurtosis_min=float(np.min(kurt_vals)),
            kurtosis_seq=kurt_seq,
        )
    except Exception as e:
        logging.exception(
            f"[ScipyStats] Skew/Kurt 計算にてエラーが発生いたしましたわ (source: {ctx.source}): {e}"
        )
        return None


def _calc_hilbert_features(ctx: AudioContext) -> HilbertFeatures | None:
    """ScipyによるHilbert Envelope, Instantaneous Frequencyを計算しますわ！"""
    if ctx.y is None or ctx.y.size == 0:
        return None
    try:
        from .structure_ssm import _pool_features_max_frames

        y_dec = ctx.y[::10]
        if y_dec.size == 0:
            return None
        sr_dec = ctx.sr / 10.0
        chunk_len = 262144
        n_total = y_dec.size
        env_all = np.empty(n_total, dtype=np.float32)
        ifreq_all = np.empty(n_total, dtype=np.float32)

        for start in range(0, n_total, chunk_len):
            end = min(n_total, start + chunk_len)
            sub = y_dec[start:end]
            analytic = scipy.signal.hilbert(sub)
            env_c = np.abs(analytic).astype(np.float32)
            if sub.size > 1:
                phase_c = np.unwrap(np.angle(analytic))
                ifreq_diff = (np.diff(phase_c) / (2.0 * np.pi) * sr_dec).astype(np.float32)
                ifreq_c = np.append(ifreq_diff, ifreq_diff[-1])
            else:
                ifreq_c = np.zeros(sub.size, dtype=np.float32)
            env_all[start:end] = env_c
            ifreq_all[start:end] = ifreq_c

        if env_all.size > 32:
            env_seq = _pool_features_max_frames(env_all[np.newaxis, :], max_frames=32)[0].tolist()
            inst_freq_seq = _pool_features_max_frames(ifreq_all[np.newaxis, :], max_frames=32)[0].tolist()
        else:
            env_seq = _resample_to_fixed_frames(env_all)
            inst_freq_seq = _resample_to_fixed_frames(ifreq_all)

        return HilbertFeatures(
            env_mean=float(np.mean(env_all)),
            env_std=float(np.std(env_all)),
            env_peak=float(np.max(env_all)),
            env_min=float(np.min(env_all)),
            env_seq=env_seq,
            inst_freq_mean=float(np.mean(ifreq_all)),
            inst_freq_std=float(np.std(ifreq_all)),
            inst_freq_peak=float(np.max(ifreq_all)),
            inst_freq_min=float(np.min(ifreq_all)),
            inst_freq_seq=inst_freq_seq,
        )
    except Exception as e:
        logging.exception(
            f"[Hilbert] Hilbert変換計算にてエラーが発生いたしましたわ (source: {ctx.source}): {e}"
        )
        return None


def _calc_peak_features(ctx: AudioContext) -> PeakFeatures | None:
    """Scipyによるピーク(Spectral, Temporal)の数を計算しますわ！"""
    if ctx.spectro is None or ctx.y is None:
        return None
    try:
        spectro = ctx.spectro
        t_frames = spectro.shape[1]
        if t_frames == 0:
            spectral_peaks_count = np.zeros(1, dtype=np.float32)
        else:
            spectral_peaks_count = np.zeros(t_frames, dtype=np.float32)
            if spectro.shape[0] >= 3:
                chunk_size = 16384
                for start in range(0, t_frames, chunk_size):
                    end = min(t_frames, start + chunk_size)
                    s = spectro[:, start:end]
                    is_peak = (
                        (s[1:-1, :] > s[:-2, :])
                        & (s[1:-1, :] > s[2:, :])
                        & (s[1:-1, :] >= 0.01)
                    )
                    spectral_peaks_count[start:end] = np.sum(is_peak, axis=0).astype(np.float32)

        onset_env = ctx.onset_env
        if onset_env is not None and onset_env.size > 0:
            segment_len = max(1, len(onset_env) // 32)
            temporal_peaks_count = np.zeros(32)
            for i in range(32):
                segment = onset_env[i * segment_len : (i + 1) * segment_len]
                peaks, _ = scipy.signal.find_peaks(segment, prominence=0.1)
                temporal_peaks_count[i] = len(peaks)
        else:
            temporal_peaks_count = np.zeros(32)

        spectral_seq = _resample_to_fixed_frames(spectral_peaks_count)
        temporal_seq = temporal_peaks_count.tolist()

        return PeakFeatures(
            spectral_mean=float(np.mean(spectral_peaks_count)),
            spectral_std=float(np.std(spectral_peaks_count)),
            spectral_peak=float(np.max(spectral_peaks_count)),
            spectral_min=float(np.min(spectral_peaks_count)),
            spectral_seq=spectral_seq,
            temporal_mean=float(np.mean(temporal_peaks_count)),
            temporal_std=float(np.std(temporal_peaks_count)),
            temporal_peak=float(np.max(temporal_peaks_count)),
            temporal_min=float(np.min(temporal_peaks_count)),
            temporal_seq=temporal_seq,
        )
    except Exception as e:
        logging.exception(
            f"[Peak] ピーク特徴量計算にてエラーが発生いたしましたわ (source: {ctx.source}): {e}"
        )
        return None
